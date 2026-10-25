package server

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
)

func proposeAPI(t *testing.T, h http.Handler, action, target string) string {
	t.Helper()
	rec := post(t, h, "/v1/actions", ProposeRequest{Action: action, Target: guard.Target{Name: target}}, "agent-token")
	var out guard.Outcome
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Request == nil {
		t.Fatalf("propose: %d %s", rec.Code, rec.Body)
	}
	return out.Request.ID
}

func decided(t *testing.T, rec *httptest.ResponseRecorder) guard.Request {
	t.Helper()
	var r guard.Request
	_ = json.Unmarshal(rec.Body.Bytes(), &r)
	return r
}

func TestApproverTokensDecideAsThemselves(t *testing.T) {
	s, _ := newServer(t)
	s.AdminToken = ""
	s.ApproverTokens = StaticTokens{"alice-token": "alice@example.com", "bob-token": "bob@example.com"}
	h := s.Handler()
	id := proposeAPI(t, h, "refund", "order-1")

	if r := get(h, "/v1/whoami", "alice-token"); r.Code != 200 || !strings.Contains(r.Body.String(), `"user":"alice@example.com"`) {
		t.Fatalf("whoami: %d %s", r.Code, r.Body)
	}
	if r := get(h, "/v1/actions", "bob-token"); r.Code != 200 {
		t.Fatalf("approvers see the requests they decide on: %d", r.Code)
	}
	if r := get(h, "/inbox/", ""); r.Code != 200 {
		t.Fatalf("the inbox is served for approvers without an admin token: %d", r.Code)
	}
	if rec := post(t, h, "/v1/actions/"+id+"/approve", map[string]string{"approver": "mallory@example.com"}, "alice-token"); rec.Code != 400 {
		t.Fatalf("a person can't decide under another name: %d", rec.Code)
	}
	rec := post(t, h, "/v1/actions/"+id+"/approve", map[string]string{"note": "ok"}, "alice-token")
	if r := decided(t, rec); rec.Code != 200 || r.DecidedBy != "alice@example.com" || r.DecidedVia != "" {
		t.Fatalf("the decision is the token's person's: %d %+v", rec.Code, r)
	}
	if rec := post(t, h, "/v1/actions/"+id+"/approve", map[string]string{"approver": "x"}, "nobody-token"); rec.Code != 401 {
		t.Fatalf("an unknown token decided: %d", rec.Code)
	}
	if r := get(h, "/v1/audit/verify", "bob-token"); r.Code != 200 {
		t.Fatalf("approvers may verify the record: %d", r.Code)
	}
}

// testIssuer is an OIDC provider: discovery, a JWKS with one RSA key, and tokens it signs.
type testIssuer struct {
	*httptest.Server
	key *rsa.PrivateKey
}

func newIssuer(t *testing.T) *testIssuer {
	t.Helper()
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	iss := &testIssuer{key: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]string{"issuer": iss.URL, "jwks_uri": iss.URL + "/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}})
	})
	iss.Server = httptest.NewServer(mux)
	t.Cleanup(iss.Close)
	return iss
}

func (i *testIssuer) token(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	base := jwt.MapClaims{"iss": i.URL, "aud": "sentinel-guard", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix()}
	for k, v := range claims {
		base[k] = v
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, base)
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(i.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestOIDCApprovers(t *testing.T) {
	iss := newIssuer(t)
	s, _ := newServer(t)
	s.AdminToken = ""
	s.OIDC = &OIDC{Issuer: iss.URL, Audience: "sentinel-guard"}
	s.Engine.Policy().Approvers = guard.Agents{Users: []string{"alice@example.com"}, Groups: []string{"sre"}}
	h := s.Handler()

	alice := iss.token(t, jwt.MapClaims{"email": "alice@example.com", "email_verified": true})
	id := proposeAPI(t, h, "refund", "order-1")
	rec := post(t, h, "/v1/actions/"+id+"/approve", map[string]string{}, alice)
	if r := decided(t, rec); rec.Code != 200 || r.DecidedBy != "alice@example.com" {
		t.Fatalf("an approver's OIDC token: %d %s", rec.Code, rec.Body)
	}

	byGroup := iss.token(t, jwt.MapClaims{"email": "carol@example.com", "groups": []string{"sre"}})
	if rec := post(t, h, "/v1/actions/"+proposeAPI(t, h, "refund", "order-2")+"/deny", map[string]string{}, byGroup); rec.Code != 200 {
		t.Fatalf("an approver by group: %d %s", rec.Code, rec.Body)
	}

	id3 := proposeAPI(t, h, "refund", "order-3")
	for name, tc := range map[string]struct {
		token string
		code  int
	}{
		"not an approver":     {iss.token(t, jwt.MapClaims{"email": "dave@example.com"}), 403},
		"unverified email":    {iss.token(t, jwt.MapClaims{"email": "alice@example.com", "email_verified": false}), 401},
		"another audience":    {iss.token(t, jwt.MapClaims{"email": "alice@example.com", "aud": "some-other-app"}), 401},
		"expired":             {iss.token(t, jwt.MapClaims{"email": "alice@example.com", "exp": time.Now().Add(-time.Hour).Unix()}), 401},
		"another issuer":      {iss.token(t, jwt.MapClaims{"email": "alice@example.com", "iss": "https://evil.example"}), 401},
		"unsigned (alg none)": {unsigned(t, iss.URL), 401},
	} {
		if rec := post(t, h, "/v1/actions/"+id3+"/approve", map[string]string{}, tc.token); rec.Code != tc.code {
			t.Errorf("%s: %d, want %d (%s)", name, rec.Code, tc.code, rec.Body)
		}
	}
}

func unsigned(t *testing.T, issuer string) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{"iss": issuer, "aud": "sentinel-guard", "email": "alice@example.com",
		"exp": time.Now().Add(time.Hour).Unix()})
	s, _ := tok.SignedString(jwt.UnsafeAllowNoneSignatureType)
	return s
}

func TestDelegateForwardsThePersonsToken(t *testing.T) {
	iss := newIssuer(t)
	s, _ := newServer(t)
	s.AdminToken = ""
	s.Agents = StaticTokens{"agent-token": "refund-bot", "gw-token": "sentinel-gateway"}
	s.Engine.Policy().Delegates.Users = []string{"sentinel-gateway"}
	h := s.Handler()
	id := proposeAPI(t, h, "refund", "order-1")

	vouch := func(body map[string]string, person string) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPost, "/v1/actions/"+id+"/approve", strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer gw-token")
		req.Header.Set(PersonTokenHeader, person)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	alice := iss.token(t, jwt.MapClaims{"email": "alice@example.com"})
	if rec := vouch(map[string]string{}, alice); rec.Code != 400 {
		t.Fatalf("a person token without the guard's OIDC settings must be refused, not ignored: %d", rec.Code)
	}
	s.OIDC = &OIDC{Issuer: iss.URL, Audience: "sentinel-guard"}
	if rec := vouch(map[string]string{}, "not-a-token"); rec.Code != 401 {
		t.Fatalf("an invalid person token: %d", rec.Code)
	}
	if rec := vouch(map[string]string{"approver": "mallory@example.com"}, alice); rec.Code != 400 {
		t.Fatalf("the delegate's claim must match the verified person: %d", rec.Code)
	}
	rec := vouch(map[string]string{"approver": "alice@example.com"}, alice)
	if r := decided(t, rec); rec.Code != 200 || r.DecidedBy != "alice@example.com" || r.DecidedVia != "sentinel-gateway" {
		t.Fatalf("verified person, via the delegate: %d %s", rec.Code, rec.Body)
	}
}

func TestTwoPersonApprovalThroughTheWebhook(t *testing.T) {
	s, _ := newServer(t)
	s.Engine.Policy().Actions["restart"] = guard.ActionPolicy{Risk: "high", Autonomy: guard.AutonomyApprove, Approvals: 2}
	h := s.Handler()
	out, _ := s.Engine.Evaluate(t.Context(), guard.Proposal{Source: guard.SourceAdmission, Agent: agentUser, Action: "restart",
		Fingerprint: "restart", Target: guard.Target{Group: "apps", Resource: "deployments", Namespace: "shop", Name: "web"}})
	before := out.Request
	if before.RequiredApprovals != 2 {
		t.Fatalf("required approvals: %d", before.RequiredApprovals)
	}
	approve := func(r *guard.Request, note string) *guard.Request {
		c := *r
		c.Decision, c.DecisionNote = "approve", note
		return &c
	}

	r := admit(t, h, "/admission/approvals", approvalReview("alice", nil, before, approve(before, "fine by me")))
	if !r.Allowed || len(r.Warnings) == 0 || !strings.Contains(r.Warnings[0], "approval 1 of 2") {
		t.Fatalf("the first approval is admitted as one of two: %+v %v", r.Result, r.Warnings)
	}
	var patch []map[string]any
	_ = json.Unmarshal(r.Patch, &patch)
	if patch[0]["path"] != "/spec/approvals" || patch[1]["op"] != "remove" || patch[1]["path"] != "/spec/decision" ||
		patch[2]["path"] != "/spec/decisionNote" {
		t.Fatalf("it must become an approval, not the decision: %s", r.Patch)
	}
	if !strings.Contains(string(r.Patch), `"by":"alice"`) || !strings.Contains(string(r.Patch), `"note":"fine by me"`) {
		t.Fatalf("the approval is the authenticated user's: %s", r.Patch)
	}

	once := *before
	once.Approvals = []guard.Approval{{By: "alice", At: time.Now()}}
	if r := admit(t, h, "/admission/approvals", approvalReview("alice", nil, &once, approve(&once, ""))); r.Allowed {
		t.Fatal("the same person approved twice")
	}
	forged := approve(&once, "")
	forged.Approvals = append(forged.Approvals, guard.Approval{By: "bob", At: time.Now()})
	if r := admit(t, h, "/admission/approvals", approvalReview("alice", nil, &once, forged)); r.Allowed {
		t.Fatal("a user wrote spec.approvals directly")
	}
	r = admit(t, h, "/admission/approvals", approvalReview("bob", nil, &once, approve(&once, "")))
	_ = json.Unmarshal(r.Patch, &patch)
	if !r.Allowed || patch[0]["path"] != "/spec/decidedBy" || patch[0]["value"] != "bob" {
		t.Fatalf("the second person's approval is the decision: %+v %s", r.Result, r.Patch)
	}
}

func TestNotifications(t *testing.T) {
	var mu sync.Mutex
	var got []Notice
	fail := 1 // the first delivery fails with a 503: the notifier retries
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if fail > 0 {
			fail--
			w.WriteHeader(503)
			return
		}
		var n Notice
		_ = json.NewDecoder(r.Body).Decode(&n)
		got = append(got, n)
	}))
	defer sink.Close()

	s, _ := newServer(t)
	s.Engine.Policy().Actions["refund"] = guard.ActionPolicy{Risk: "high", Autonomy: guard.AutonomyApprove, Approvals: 2}
	s.Notify = NewNotifier(sink.URL, "http://localhost:8080", slog.New(slog.NewTextHandler(io.Discard, nil)), prometheus.NewRegistry())
	go s.Notify.Run(t.Context())
	h := s.Handler()
	rec := post(t, h, "/v1/actions", ProposeRequest{Action: "refund", Target: guard.Target{Name: "order-7"},
		Change: map[string]string{"amount": "42.00 EUR"}, Intent: "charged twice"}, "agent-token")
	var out guard.Outcome
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	post(t, h, "/v1/actions/"+out.Request.ID+"/approve", map[string]string{"approver": "alice"}, "admin-token")

	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("two notices (asked, then one approval of two): %+v", got)
	}
	first, second := got[0], got[1]
	if !strings.Contains(first.Text, "refund-bot asks to refund order-7 (amount: 42.00 EUR), high risk") ||
		!strings.Contains(first.Text, "Needs 2 different people") || !strings.Contains(first.Text, "http://localhost:8080/inbox/") ||
		first.Content != first.Text || first.Event.Kind != "approval_needed" || first.Event.Needed != 2 {
		t.Fatalf("the first notice: %+v", first)
	}
	if !strings.Contains(second.Text, "Approved by alice; needs 1 more") || second.Event.Needed != 1 {
		t.Fatalf("the second notice: %+v", second)
	}
	// The sink records a notice before the notifier sees its answer and counts it: wait for the count.
	for deadline := time.Now().Add(2 * time.Second); testutil.ToFloat64(s.Notify.Sent.WithLabelValues("sent")) != 2; {
		if time.Now().After(deadline) {
			t.Fatalf("sent = %v", testutil.ToFloat64(s.Notify.Sent.WithLabelValues("sent")))
		}
		time.Sleep(10 * time.Millisecond)
	}
}
