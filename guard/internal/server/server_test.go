package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	admissionv1 "k8s.io/api/admission/v1"
	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/store"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/policies"
)

const (
	agentUser = "system:serviceaccount:agents:fixer"
	agentsGrp = "system:serviceaccounts:agents"
	guardUser = "system:serviceaccount:sentinel-system:sentinel-guard"
)

func newServer(t *testing.T) (*Server, *store.Memory) {
	t.Helper()
	p, err := guard.ParsePolicy(policies.Default)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := guard.GenerateKeyPEM()
	signer, _ := guard.NewSigner(key, "test")
	mem := store.NewMemory()
	s := &Server{
		Engine: guard.NewEngine(mem, signer, nil, p), Agents: StaticTokens{"agent-token": "refund-bot"},
		AdminToken: "admin-token", GuardUser: guardUser, Metrics: NewMetrics(prometheus.NewRegistry()),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	s.Generation = func(context.Context, schema.GroupVersionResource, string, string) (int64, error) { return 4, nil }
	s.Hook()
	return s, mem
}

func raw(v any) runtime.RawExtension {
	b, _ := json.Marshal(v)
	return runtime.RawExtension{Raw: b}
}

func scaleReview(user string, groups []string, from, to int, dryRun bool) admissionv1.AdmissionReview {
	scale := func(n int) map[string]any {
		return map[string]any{"kind": "Scale", "spec": map[string]any{"replicas": n}}
	}
	return admissionv1.AdmissionReview{
		TypeMeta: metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"},
		Request: &admissionv1.AdmissionRequest{
			UID: types.UID("uid-1"), Operation: admissionv1.Update, SubResource: "scale",
			Resource:  metav1.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			Namespace: "shop", Name: "checkout", DryRun: &dryRun,
			UserInfo:  authnv1.UserInfo{Username: user, Groups: groups},
			OldObject: raw(scale(from)), Object: raw(scale(to)),
		}}
}

func post(t *testing.T, h http.Handler, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func admit(t *testing.T, h http.Handler, path string, review admissionv1.AdmissionReview) *admissionv1.AdmissionResponse {
	t.Helper()
	rec := post(t, h, path, review, "")
	var out admissionv1.AdmissionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Response == nil {
		t.Fatalf("bad admission response %d: %s", rec.Code, rec.Body)
	}
	if out.Response.UID != review.Request.UID {
		t.Fatal("the response must echo the request UID")
	}
	return out.Response
}

func TestWorkloadWebhook(t *testing.T) {
	s, mem := newServer(t)
	h := s.Handler()

	human := admit(t, h, "/admission/workloads", scaleReview("alice", []string{"system:authenticated"}, 2, 9, false))
	if !human.Allowed {
		t.Fatal("humans are never stopped by the guard")
	}
	denied := admit(t, h, "/admission/workloads", scaleReview(agentUser, []string{agentsGrp}, 2, 3, false))
	if denied.Allowed || !strings.Contains(denied.Result.Message, "kubectl sentinel approve ar-") {
		t.Fatalf("an agent change should wait for approval: %+v", denied.Result)
	}
	id := denied.AuditAnnotations["request"]
	preview := admit(t, h, "/admission/workloads", scaleReview(agentUser, []string{agentsGrp}, 2, 3, true))
	if !preview.Allowed || len(preview.Warnings) == 0 {
		t.Fatal("server-side dry runs are previews: admitted with a warning")
	}
	if _, err := s.Engine.Decide(t.Context(), id, "alice", nil, "", true, ""); err != nil {
		t.Fatal(err)
	}
	if ok := admit(t, h, "/admission/workloads", scaleReview(agentUser, []string{agentsGrp}, 2, 3, false)); !ok.Allowed {
		t.Fatalf("approved change not admitted: %+v", ok.Result)
	}
	// Within seconds, an identical write is the API server retrying the same request (admitted);
	// after that it is a replay (denied).
	if retry := admit(t, h, "/admission/workloads", scaleReview(agentUser, []string{agentsGrp}, 2, 3, false)); !retry.Allowed {
		t.Fatalf("an API server retry of the approved write was refused: %+v", retry.Result)
	}
	later := time.Now().Add(guard.ReadmitWindow + time.Second)
	s.Engine.Now = func() time.Time { return later }
	if again := admit(t, h, "/admission/workloads", scaleReview(agentUser, []string{agentsGrp}, 2, 3, false)); again.Allowed {
		t.Fatal("an approval admitted twice")
	}
	zero := admit(t, h, "/admission/workloads", scaleReview(agentUser, []string{agentsGrp}, 2, 0, false))
	if zero.Allowed || !strings.Contains(zero.Result.Message, "blocked") {
		t.Fatalf("scale to zero must be blocked by policy: %+v", zero.Result)
	}
	if rep, _ := s.Engine.Verify(t.Context()); !rep.OK {
		t.Fatalf("%+v", rep)
	}
	_ = mem
}

func TestWorkloadWebhookFailsClosedForAgents(t *testing.T) {
	s, _ := newServer(t)
	review := scaleReview(agentUser, []string{agentsGrp}, 2, 3, false)
	review.Request.Object = runtime.RawExtension{Raw: []byte(`"not an object"`)}
	if resp := admit(t, s.Handler(), "/admission/workloads", review); resp.Allowed {
		t.Fatal("an unreadable agent change must be denied")
	}
}

func approvalReview(user string, groups []string, before *guard.Request, after *guard.Request) admissionv1.AdmissionReview {
	o1, _ := store.ToObject(before)
	o2, _ := store.ToObject(after)
	return admissionv1.AdmissionReview{Request: &admissionv1.AdmissionRequest{
		UID: "uid-2", Operation: admissionv1.Update, Name: before.ID,
		Resource: metav1.GroupVersionResource{Group: "sentinel.dev", Version: "v1", Resource: "actionrequests"},
		UserInfo: authnv1.UserInfo{Username: user, Groups: groups}, OldObject: raw(o1.Object), Object: raw(o2.Object),
	}}
}

func TestApprovalWebhook(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	out, _ := s.Engine.Evaluate(t.Context(), guard.Proposal{Source: guard.SourceAdmission, Agent: agentUser, Action: "restart",
		Fingerprint: "restart", Target: guard.Target{Group: "apps", Resource: "deployments", Namespace: "shop", Name: "web"}})
	before := out.Request
	withDecision := func(d, decidedBy string) *guard.Request {
		c := *before
		c.Decision, c.DecidedBy = d, decidedBy
		return &c
	}

	if r := admit(t, h, "/admission/approvals", approvalReview(agentUser, []string{agentsGrp}, before, withDecision("approve", ""))); r.Allowed {
		t.Fatal("an agent approved its own request")
	}
	if r := admit(t, h, "/admission/approvals", approvalReview("system:serviceaccount:agents:helper", []string{agentsGrp}, before, withDecision("approve", ""))); r.Allowed {
		t.Fatal("another agent approved a request")
	}
	edited := withDecision("approve", "")
	edited.Target.Name = "payments"
	if r := admit(t, h, "/admission/approvals", approvalReview("alice", nil, before, edited)); r.Allowed {
		t.Fatal("an approver changed what was requested")
	}
	if r := admit(t, h, "/admission/approvals", approvalReview("alice", nil, before, withDecision("yes please", ""))); r.Allowed {
		t.Fatal("an invalid decision was accepted")
	}
	r := admit(t, h, "/admission/approvals", approvalReview("alice", nil, before, withDecision("approve", "mallory")))
	if !r.Allowed || r.PatchType == nil {
		t.Fatalf("a human approval should be admitted and stamped: %+v", r.Result)
	}
	var patch []map[string]any
	_ = json.Unmarshal(r.Patch, &patch)
	if patch[0]["path"] != "/spec/decidedBy" || patch[0]["value"] != "alice" {
		t.Fatalf("the approver must be the authenticated user, not what the client wrote: %s", r.Patch)
	}
	create := approvalReview("alice", nil, before, before)
	create.Request.Operation = admissionv1.Create
	if r := admit(t, h, "/admission/approvals", create); r.Allowed {
		t.Fatal("humans must not create ActionRequests")
	}
	del := approvalReview("cluster-admin", nil, before, before)
	del.Request.Operation = admissionv1.Delete
	if r := admit(t, h, "/admission/approvals", del); r.Allowed {
		t.Fatal("the audit record must not be deletable")
	}
	if r := admit(t, h, "/admission/approvals", approvalReview(guardUser, nil, before, before)); !r.Allowed {
		t.Fatal("the guard manages its own records")
	}
}

func TestActionAPI(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()
	body := ProposeRequest{Action: "refund", Target: guard.Target{Name: "order-1042"}, Change: map[string]string{"amount": "42.00 EUR"}}

	if rec := post(t, h, "/v1/actions", body, "wrong"); rec.Code != 401 {
		t.Fatalf("unauthenticated proposal: %d", rec.Code)
	}
	rec := post(t, h, "/v1/actions", body, "agent-token")
	if rec.Code != 202 {
		t.Fatalf("propose: %d %s", rec.Code, rec.Body)
	}
	var out guard.Outcome
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	id := out.Request.ID

	if rec := post(t, h, "/v1/actions/"+id+"/approve", map[string]string{"approver": "alice"}, "agent-token"); rec.Code != 401 {
		t.Fatalf("an agent token approved: %d", rec.Code)
	}
	if rec := post(t, h, "/v1/actions/"+id+"/approve", map[string]string{"approver": "alice", "note": "ok"}, "admin-token"); rec.Code != 200 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/actions/"+id, nil)
	get.Header.Set("Authorization", "Bearer agent-token")
	got := httptest.NewRecorder()
	h.ServeHTTP(got, get)
	_ = json.Unmarshal(got.Body.Bytes(), &out)
	if got.Code != 200 || out.Capability == "" {
		t.Fatalf("capability: %d %s", got.Code, got.Body)
	}
	if rec := post(t, h, "/v1/capabilities/spend", map[string]string{"capability": out.Capability, "executor": "payments"}, ""); rec.Code != 200 {
		t.Fatalf("spend: %d %s", rec.Code, rec.Body)
	}
	if rec := post(t, h, "/v1/capabilities/spend", map[string]string{"capability": out.Capability}, ""); rec.Code != 409 {
		t.Fatalf("second spend: %d", rec.Code)
	}
	if rec := post(t, h, "/v1/capabilities/spend", map[string]string{"capability": "eyJhbGciOiJub25lIn0.e30."}, ""); rec.Code != 403 {
		t.Fatalf("alg=none capability: %d", rec.Code)
	}
	changed := body
	changed.Change = map[string]string{"amount": "4200.00 EUR"}
	if rec := post(t, h, "/v1/actions", changed, "agent-token"); rec.Code != 202 {
		t.Fatalf("a different amount needs its own approval: %d", rec.Code)
	}
	verify := httptest.NewRequest(http.MethodGet, "/v1/audit/verify", nil)
	verify.Header.Set("Authorization", "Bearer admin-token")
	vrec := httptest.NewRecorder()
	h.ServeHTTP(vrec, verify)
	if !strings.Contains(vrec.Body.String(), `"ok":true`) {
		t.Fatalf("audit: %s", vrec.Body)
	}
	jwks := httptest.NewRecorder()
	h.ServeHTTP(jwks, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if keys, err := guard.ParseJWKS(jwks.Body.Bytes()); err != nil || len(keys) != 1 {
		t.Fatalf("jwks: %v %s", err, jwks.Body)
	}
}

func TestKubeObjectRoundTripKeepsAuditValid(t *testing.T) {
	s, mem := newServer(t)
	out, _ := s.Engine.Evaluate(t.Context(), guard.Proposal{Source: guard.SourceAdmission, Agent: agentUser, Action: "scale",
		Change: map[string]string{"replicas": "2 → 3"}, Fingerprint: "replicas=3",
		Target: guard.Target{Group: "apps", Resource: "deployments", Namespace: "shop", Name: "web"}})
	_, _ = s.Engine.Decide(t.Context(), out.Request.ID, "alice", nil, "", true, "fine")
	r, _ := mem.Get(t.Context(), out.Request.ID)
	obj, err := store.ToObject(r)
	if err != nil {
		t.Fatal(err)
	}
	// What the API server stores and returns is JSON.
	b, _ := json.Marshal(obj.Object)
	var back map[string]any
	_ = json.Unmarshal(b, &back)
	obj.Object = back
	r2, err := store.FromObject(obj)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := mem.Head(t.Context())
	if rep := guard.VerifyAudit([]*guard.Request{r2}, head, s.Engine.Signer.PublicKeys()); !rep.OK {
		t.Fatalf("the audit must survive the Kubernetes round trip: %+v", rep)
	}
}

func TestDelegatedDecisions(t *testing.T) {
	s, _ := newServer(t)
	s.Agents = StaticTokens{"agent-token": "sentinel-agent", "gw-token": "sentinel-gateway", "other-token": "someone"}
	s.Engine.Policy().Delegates.Users = []string{"sentinel-gateway"}
	s.Engine.Policy().Agents.Users = []string{"sentinel-agent"}
	h := s.Handler()
	propose := func(incident string) string {
		rec := post(t, h, "/v1/actions", ProposeRequest{Action: "rollback", Target: guard.Target{Namespace: "acme", Name: "checkout"},
			Change: map[string]string{"incident": incident}}, "agent-token")
		var out guard.Outcome
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Request.ID
	}
	id := propose("inc-1")

	if rec := post(t, h, "/v1/actions/"+id+"/approve", map[string]string{"approver": "alice@acme"}, "other-token"); rec.Code != 401 {
		t.Fatalf("a caller that is not a delegate decided: %d", rec.Code)
	}
	if rec := post(t, h, "/v1/actions/"+id+"/approve", map[string]string{"approver": "sentinel-agent"}, "gw-token"); rec.Code != 403 {
		t.Fatalf("a delegate approved on behalf of the requesting agent: %d", rec.Code)
	}
	rec := post(t, h, "/v1/actions/"+id+"/approve", map[string]string{"approver": "alice@acme", "note": "console"}, "gw-token")
	var r guard.Request
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	if rec.Code != 200 || r.DecidedBy != "alice@acme" || r.DecidedVia != "sentinel-gateway" || r.Phase != guard.PhaseApproved {
		t.Fatalf("delegated approval: %d %+v", rec.Code, r)
	}
	if d := r.Audit[len(r.Audit)-1]; d.Actor != "alice@acme" || !strings.Contains(d.Detail, "via sentinel-gateway") {
		t.Fatalf("the audit must show who decided and through whom: %+v", d)
	}
	get := httptest.NewRequest(http.MethodGet, "/v1/actions/"+id, nil)
	get.Header.Set("Authorization", "Bearer agent-token")
	got := httptest.NewRecorder()
	h.ServeHTTP(got, get)
	var out guard.Outcome
	_ = json.Unmarshal(got.Body.Bytes(), &out)
	claims, err := s.Engine.Signer.VerifyCapability(out.Capability)
	if err != nil || claims.Namespace != "acme" || claims.Target != "checkout" || claims.Change["incident"] != "inc-1" || claims.ApprovedBy != "alice@acme" {
		t.Fatalf("the capability must carry the scope and the approved parameters: %v %+v", err, claims)
	}

	// In Kubernetes there is no admin token: only delegates may decide over HTTP.
	s.AdminToken = ""
	id2 := propose("inc-2")
	if rec := post(t, h, "/v1/actions/"+id2+"/deny", map[string]string{"approver": "bob@acme"}, "agent-token"); rec.Code != 403 {
		t.Fatalf("an agent denied over HTTP: %d", rec.Code)
	}
	if rec := post(t, h, "/v1/actions/"+id2+"/deny", map[string]string{"approver": "bob@acme"}, "gw-token"); rec.Code != 200 {
		t.Fatalf("delegated denial: %d %s", rec.Code, rec.Body)
	}
}

func TestAPIHandlerHasNoWebhooks(t *testing.T) {
	s, _ := newServer(t)
	rec := post(t, s.APIHandler(), "/admission/workloads", scaleReview(agentUser, []string{agentsGrp}, 2, 3, false), "")
	if rec.Code != 404 {
		t.Fatalf("the plain-HTTP API listener must not serve admission: %d", rec.Code)
	}
	jwks := httptest.NewRecorder()
	s.APIHandler().ServeHTTP(jwks, httptest.NewRequest(http.MethodGet, "/.well-known/jwks.json", nil))
	if jwks.Code != 200 {
		t.Fatal(jwks.Code)
	}
}

func get(h http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestEvidenceExportIsForAuditorsAndVerifies(t *testing.T) {
	s, _ := newServer(t)
	s.Agents = StaticTokens{"agent-token": "refund-bot", "auditor-token": "evidence-archiver"}
	p := *s.Engine.Policy()
	p.Auditors.Users = []string{"evidence-archiver"}
	s.Engine.SetPolicy(&p)
	h := s.Handler()
	body := ProposeRequest{Action: "refund", Target: guard.Target{Name: "order-7"}, Change: map[string]string{"amount": "9.00 EUR"}}
	rec := post(t, h, "/v1/actions", body, "agent-token")
	var out guard.Outcome
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	post(t, h, "/v1/actions/"+out.Request.ID+"/deny", map[string]string{"approver": "alice", "note": "duplicate"}, "admin-token")

	for _, token := range []string{"", "agent-token"} {
		if rec := get(h, "/v1/audit/export", token); rec.Code != 401 {
			t.Fatalf("export with %q: %d", token, rec.Code)
		}
	}
	for _, token := range []string{"admin-token", "auditor-token"} {
		rec := get(h, "/v1/audit/export", token)
		if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "sentinel-guard-evidence-") {
			t.Fatalf("export with %s: %d %s", token, rec.Code, rec.Body)
		}
		var ev guard.Evidence
		if err := json.Unmarshal(rec.Body.Bytes(), &ev); err != nil {
			t.Fatal(err)
		}
		rep := guard.VerifyEvidence(&ev, s.Engine.Signer.PublicKeys())
		if !rep.OK || rep.Summary.Outcomes["denied"] != 1 || rep.Summary.Deciders["alice"] != 1 {
			t.Fatalf("%+v", rep)
		}
	}
	if rec := get(h, "/v1/audit/verify", "auditor-token"); !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("an auditor may verify too: %d %s", rec.Code, rec.Body)
	}
}

func TestGuardianMetrics(t *testing.T) {
	s, mem := newServer(t)
	h := s.Handler()
	propose := func(amount string) string {
		rec := post(t, h, "/v1/actions", ProposeRequest{Action: "refund", Target: guard.Target{Name: "order-1"},
			Change: map[string]string{"amount": amount}}, "agent-token")
		var out guard.Outcome
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Request.ID
	}
	propose("1.00")
	denied := propose("2.00")
	post(t, h, "/v1/actions/"+denied+"/deny", map[string]string{"approver": "alice"}, "admin-token")
	clk := time.Now().Add(20 * time.Minute)
	s.Engine.Now = func() time.Time { return clk }

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // one pass, then stop
	s.Monitor(ctx, time.Hour, time.Hour)

	m := s.Metrics
	if v := testutil.ToFloat64(m.Outcomes.WithLabelValues("refund-bot", "refund", guard.OutcomeProposed)); v != 2 {
		t.Fatalf("proposed = %v", v)
	}
	if v := testutil.ToFloat64(m.Outcomes.WithLabelValues("refund-bot", "refund", guard.OutcomeDenied)); v != 1 {
		t.Fatalf("denied = %v", v)
	}
	if v := testutil.ToFloat64(m.Open.WithLabelValues("Pending")); v != 1 {
		t.Fatalf("open pending = %v", v)
	}
	if v := testutil.ToFloat64(m.OldestPending); v < 20*60 || v > 21*60 {
		t.Fatalf("oldest pending = %v s", v)
	}
	if testutil.ToFloat64(m.ChainOK) != 1 || testutil.ToFloat64(m.HeadSeq) != 3 || testutil.ToFloat64(m.Verified) == 0 {
		t.Fatalf("chain ok %v, head %v", testutil.ToFloat64(m.ChainOK), testutil.ToFloat64(m.HeadSeq))
	}
	if testutil.ToFloat64(m.PolicyInfo.WithLabelValues(s.Engine.PolicyVersion()[:12])) != 1 {
		t.Fatal("policy info")
	}

	mem.Delete(denied) // tampering: a record disappears
	s.Monitor(ctx, time.Hour, time.Hour)
	if testutil.ToFloat64(m.ChainOK) != 0 {
		t.Fatal("self-verification must notice a deleted record")
	}
}
