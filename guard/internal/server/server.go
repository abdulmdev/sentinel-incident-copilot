// Package server exposes the guard over HTTP: the action API for agents and executors, the two
// Kubernetes admission webhooks, the public keys and metrics, and (single-process mode) the
// approvals inbox.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
)

// Authenticator maps a bearer token to an identity.
type Authenticator interface {
	Authenticate(ctx context.Context, token string) (user string, groups []string, err error)
}

// StaticTokens authenticates agents by pre-shared tokens (name=token): for docker run and tests.
type StaticTokens map[string]string // token -> agent name

func (s StaticTokens) Authenticate(_ context.Context, token string) (string, []string, error) {
	for t, name := range s {
		if subtle.ConstantTimeCompare([]byte(t), []byte(token)) == 1 {
			return name, nil, nil
		}
	}
	return "", nil, errors.New("unknown token")
}

type Server struct {
	Engine *guard.Engine
	// Agents authenticates API callers (ServiceAccount tokens in Kubernetes, static tokens otherwise).
	Agents Authenticator
	// AdminToken enables HTTP approvals (single-process mode). In Kubernetes, humans approve
	// through the API server instead (kubectl sentinel approve), so this stays empty.
	AdminToken string
	// ApproverTokens are personal tokens for the people who decide over HTTP (single-process
	// mode): each decision is recorded as the token's person, never as a name typed in.
	ApproverTokens StaticTokens
	// OIDC verifies people's own OIDC tokens; they must be listed under the policy's approvers.
	// A delegate may also forward the person's token (X-Sentinel-Person-Token) for the guard to verify.
	OIDC *OIDC
	// Notify tells people when a request needs them (nil: off).
	Notify *Notifier
	// GuardUser is the guard's own Kubernetes identity: the only one allowed to manage ActionRequests.
	GuardUser string
	// Generation reads a workload's metadata.generation (for Scale requests, which lack one).
	Generation func(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (int64, error)
	Metrics    *Metrics
	Log        *slog.Logger
	Ready      func() bool
	Version    string

	// RecheckAfter: how long self-verification waits to re-read before reporting a failure (default 3 s).
	RecheckAfter time.Duration

	lastVerify atomic.Pointer[Verification] // the guardian monitor's last full verification
}

// Verification is the result of the last self-verification of the audit chain.
type Verification struct {
	OK       bool      `json:"ok"`
	Events   int       `json:"events"`
	Requests int       `json:"requests"`
	HeadSeq  int64     `json:"headSeq"`
	Problems []string  `json:"problems,omitempty"`
	At       time.Time `json:"at"`
}

func (s *Server) Handler() http.Handler { return s.handler(true) }

// APIHandler serves the action API without the admission webhooks, for an in-cluster
// plain-HTTP listener (clients in other namespaces can't trust the webhook's private CA).
func (s *Server) APIHandler() http.Handler { return s.handler(false) }

func (s *Server) handler(admission bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		if s.Ready != nil && !s.Ready() {
			writeJSON(w, 503, map[string]string{"status": "starting"})
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET /.well-known/jwks.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		writeJSON(w, 200, s.Engine.Signer.JWKS())
	})
	mux.HandleFunc("POST /v1/actions", s.agentOnly(s.propose))
	mux.HandleFunc("GET /v1/actions/{id}", s.agentOnly(s.status))
	mux.HandleFunc("GET /v1/actions", s.approversOnly(s.list))
	mux.HandleFunc("GET /v1/whoami", s.whoami)
	mux.HandleFunc("GET /v1/overview", s.auditors(s.overview))
	mux.HandleFunc("POST /v1/actions/{id}/approve", s.deciders(s.decide(true)))
	mux.HandleFunc("POST /v1/actions/{id}/deny", s.deciders(s.decide(false)))
	mux.HandleFunc("POST /v1/capabilities/spend", s.spend)
	mux.HandleFunc("GET /v1/audit/verify", s.auditors(s.verify))
	mux.HandleFunc("GET /v1/audit/export", s.auditors(s.export))
	s.mountInbox(mux)
	if admission {
		mux.HandleFunc("POST /admission/workloads", s.timed("workloads", s.admitWorkload))
		mux.HandleFunc("POST /admission/approvals", s.timed("approvals", s.admitApproval))
	}
	return mux
}

type identity struct {
	user   string
	groups []string
	person bool   // a person deciding with their own identity (approver token or OIDC)
	vouch  string // a delegate's call carrying the person's OIDC token: the verified person
}

// PersonTokenHeader carries a person's OIDC token on a delegate's call, so the guard verifies who
// decided instead of trusting the delegate's word for it.
const PersonTokenHeader = "X-Sentinel-Person-Token"

func (s *Server) peopleEnabled() bool { return len(s.ApproverTokens) > 0 || s.OIDC != nil }

// person authenticates a person deciding with their own identity. ok is false when the token is
// not a person's; err (with an HTTP status) when it is an OIDC token that fails verification or
// names someone who isn't an approver.
func (s *Server) person(ctx context.Context, token string) (id identity, ok bool, code int, err error) {
	if token == "" {
		return identity{}, false, 0, nil
	}
	if user, _, e := s.ApproverTokens.Authenticate(ctx, token); e == nil {
		return identity{user: user, person: true}, true, 0, nil
	}
	if s.OIDC == nil || strings.Count(token, ".") != 2 {
		return identity{}, false, 0, nil
	}
	user, groups, e := s.OIDC.Authenticate(ctx, token)
	if e != nil {
		return identity{}, false, 0, nil // perhaps an agent's (Kubernetes) token: let the next check decide
	}
	if !s.Engine.Policy().IsApprover(user, groups) {
		return identity{}, true, 403, fmt.Errorf("%s is not among the policy's approvers", user)
	}
	return identity{user: user, groups: groups, person: true}, true, 0, nil
}

type ctxKey struct{}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return h[7:]
	}
	return ""
}

func (s *Server) agentOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" || s.Agents == nil {
			writeError(w, 401, "an agent token is required (Authorization: Bearer ...)")
			return
		}
		user, groups, err := s.Agents.Authenticate(r.Context(), token)
		if err != nil {
			writeError(w, 401, "invalid agent token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, identity{user: user, groups: groups})))
	}
}

// deciders admits the two kinds of callers that may record a human decision over HTTP: the
// admin token (single-process mode), or a delegate (policy `delegates`), a service that
// authenticated the person itself (Sentinel's gateway, for its console users). A delegate's
// decision is recorded as made by that person, via the delegate.
func (s *Server) deciders(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if s.AdminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.AdminToken)) == 1 {
			next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, identity{})))
			return
		}
		if id, ok, code, err := s.person(r.Context(), token); ok {
			if err != nil {
				writeError(w, code, err.Error())
				return
			}
			next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
			return
		}
		if token != "" && s.Agents != nil {
			if user, groups, err := s.Agents.Authenticate(r.Context(), token); err == nil && s.Engine.Policy().IsDelegate(user, groups) {
				id := identity{user: user, groups: groups}
				if pt := r.Header.Get(PersonTokenHeader); pt != "" {
					if s.OIDC == nil {
						writeError(w, 400, PersonTokenHeader+" needs the guard's OIDC settings (SENTINEL_GUARD_OIDC_ISSUER)")
						return
					}
					person, _, err := s.OIDC.Authenticate(r.Context(), pt)
					if err != nil {
						writeError(w, 401, "the person's token: "+err.Error())
						return
					}
					id.vouch = person
				}
				next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
				return
			}
		}
		if s.AdminToken == "" && !s.peopleEnabled() {
			writeError(w, 403, "only a configured delegate may decide over HTTP here: people approve with `kubectl sentinel approve <id>`")
			return
		}
		writeError(w, 401, "an approver's token, the admin token or a delegate's token is required")
	}
}

// auditors admits the admin token, or an identity listed in the policy's auditors (for example a
// CronJob's ServiceAccount that archives evidence): read-only access to the audit trail.
func (s *Server) auditors(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if s.AdminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.AdminToken)) == 1 {
			next(w, r)
			return
		}
		if _, ok, _, err := s.person(r.Context(), token); ok && err == nil { // approvers see what they approve
			next(w, r)
			return
		}
		if token != "" && s.Agents != nil {
			if user, groups, err := s.Agents.Authenticate(r.Context(), token); err == nil && s.Engine.Policy().IsAuditor(user, groups) {
				next(w, r)
				return
			}
		}
		writeError(w, 401, "the admin token or an auditor's token (policy auditors) is required; "+
			"in Kubernetes, `kubectl sentinel audit export` reads the same record with your own RBAC")
	}
}

// approversOnly admits the people who decide over HTTP: the admin token, or a person's own token.
func (s *Server) approversOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.AdminToken == "" && !s.peopleEnabled() {
			writeError(w, 404, "HTTP approvals are disabled here: approve with `kubectl sentinel approve <id>`")
			return
		}
		token := bearer(r)
		if s.AdminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.AdminToken)) == 1 {
			next(w, r)
			return
		}
		if _, ok, code, err := s.person(r.Context(), token); ok {
			if err != nil {
				writeError(w, code, err.Error())
				return
			}
			next(w, r)
			return
		}
		writeError(w, 401, "an approver's token (or the admin token) is required")
	}
}

// Overview is the high-level picture behind the inbox: what each agent's proposals came to, what
// waits for people and for how long, and the state of the record. Everything is derived from the
// requests and their signed events (the same summary the evidence bundle carries).
type Overview struct {
	Summary              guard.OversightSummary `json:"summary"`
	Pending              int                    `json:"pending"`
	ApprovedUnspent      int                    `json:"approvedUnspent"`
	OldestPendingSeconds float64                `json:"oldestPendingSeconds"`
	Agents               []AgentOverview        `json:"agents"`
	PolicyVersion        string                 `json:"policyVersion"`
	Chain                *Verification          `json:"chain,omitempty"` // nil until the first self-verification
	GuardVersion         string                 `json:"guardVersion,omitempty"`
}

type AgentOverview struct {
	Agent    string         `json:"agent"`
	Outcomes map[string]int `json:"outcomes"`
	Waiting  int            `json:"waiting"` // pending a person right now
	LastSeen time.Time      `json:"lastSeen"`
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	reqs, err := s.Engine.Store.List(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	now := s.Engine.Now()
	ov := Overview{Summary: guard.Summarize(reqs), PolicyVersion: s.Engine.PolicyVersion(), Chain: s.lastVerify.Load(),
		GuardVersion: s.Version}
	agents := map[string]*AgentOverview{}
	for _, req := range reqs {
		a := agents[req.Agent]
		if a == nil {
			a = &AgentOverview{Agent: req.Agent, Outcomes: ov.Summary.ByAgent[req.Agent]}
			if a.Outcomes == nil {
				a.Outcomes = map[string]int{}
			}
			agents[req.Agent] = a
		}
		if req.CreatedAt.After(a.LastSeen) {
			a.LastSeen = req.CreatedAt
		}
		if !req.Phase.Open() || !now.Before(req.ExpiresAt) {
			continue
		}
		switch req.Phase {
		case guard.PhasePending:
			ov.Pending++
			a.Waiting++
			if age := now.Sub(req.CreatedAt).Seconds(); age > ov.OldestPendingSeconds {
				ov.OldestPendingSeconds = age
			}
		case guard.PhaseApproved:
			ov.ApprovedUnspent++
		}
	}
	for _, a := range agents {
		ov.Agents = append(ov.Agents, *a)
	}
	sort.Slice(ov.Agents, func(i, j int) bool { return ov.Agents[i].LastSeen.After(ov.Agents[j].LastSeen) })
	writeJSON(w, 200, ov)
}

// whoami tells a caller who the guard takes them for, and what they may do: the inbox shows it.
func (s *Server) whoami(w http.ResponseWriter, r *http.Request) {
	token := bearer(r)
	if s.AdminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(s.AdminToken)) == 1 {
		writeJSON(w, 200, map[string]any{"role": "admin", "note": "a shared token: decisions record the name you give"})
		return
	}
	if id, ok, code, err := s.person(r.Context(), token); ok {
		if err != nil {
			writeError(w, code, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"user": id.user, "role": "approver"})
		return
	}
	if token != "" && s.Agents != nil {
		if user, groups, err := s.Agents.Authenticate(r.Context(), token); err == nil {
			p, role := s.Engine.Policy(), "caller"
			switch {
			case p.IsAgent(user, groups):
				role = "agent"
			case p.IsDelegate(user, groups):
				role = "delegate"
			case p.IsAuditor(user, groups):
				role = "auditor"
			}
			writeJSON(w, 200, map[string]any{"user": user, "role": role})
			return
		}
	}
	writeError(w, 401, "unknown token")
}

// ProposeRequest is what an agent sends before acting.
type ProposeRequest struct {
	Action string            `json:"action"`
	Target guard.Target      `json:"target"`
	Change map[string]string `json:"change,omitempty"`
	Intent string            `json:"intent,omitempty"`
	DryRun bool              `json:"dryRun,omitempty"`
}

func (s *Server) propose(w http.ResponseWriter, r *http.Request) {
	id := r.Context().Value(ctxKey{}).(identity)
	var body ProposeRequest
	if err := decodeBody(r, &body); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	if body.Action == "" || body.Target.Name == "" {
		writeError(w, 400, "action and target.name are required")
		return
	}
	out, err := s.Engine.Evaluate(r.Context(), guard.Proposal{
		Source: guard.SourceAPI, Agent: id.user, Action: body.Action, Target: body.Target, Change: body.Change,
		Fingerprint: fingerprint(body.Change), Intent: body.Intent, DryRun: body.DryRun,
	})
	s.writeOutcome(w, out, err)
}

// fingerprint binds an API approval to the exact parameters the agent asked for.
func fingerprint(change map[string]string) string {
	keys := make([]string, 0, len(change))
	for k := range change {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%q=%q;", k, change[k])
	}
	return "change=" + hex.EncodeToString(h.Sum(nil))[:16]
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	id := r.Context().Value(ctxKey{}).(identity)
	out, err := s.Engine.Capability(r.Context(), r.PathValue("id"), id.user)
	s.writeOutcome(w, out, err)
}

func (s *Server) writeOutcome(w http.ResponseWriter, out guard.Outcome, err error) {
	if err != nil {
		s.writeErr(w, err)
		return
	}
	code := map[guard.Verdict]int{guard.VerdictAllow: 200, guard.VerdictPending: 202, guard.VerdictBlock: 403}[out.Verdict]
	writeJSON(w, code, out)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	reqs, err := s.Engine.Store.List(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"requests": reqs})
}

func (s *Server) decide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Approver string `json:"approver"`
			Note     string `json:"note"`
		}
		if err := decodeBody(r, &body); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		id := r.Context().Value(ctxKey{}).(identity)
		decider, via := body.Approver, id.user // via is empty for the admin token
		if verified := map[bool]string{true: id.user, false: id.vouch}[id.person]; verified != "" {
			// The guard authenticated the person: that is who decides, whatever the body says.
			if body.Approver != "" && body.Approver != verified {
				writeError(w, 400, fmt.Sprintf("you are deciding as %s (from your token), not %s", verified, body.Approver))
				return
			}
			decider = verified
			if id.person {
				via = ""
			}
		}
		if decider == "" {
			writeError(w, 400, "approver (who is deciding, for the audit trail) is required")
			return
		}
		var groups []string // the decider's groups: known only for a person the guard authenticated
		if id.person {
			groups = id.groups
		}
		req, err := s.Engine.Decide(r.Context(), r.PathValue("id"), decider, groups, via, approve, body.Note)
		if err != nil {
			s.writeErr(w, err)
			return
		}
		writeJSON(w, 200, req)
	}
}

func (s *Server) spend(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Capability string `json:"capability"`
		Executor   string `json:"executor"`
	}
	if err := decodeBody(r, &body); err != nil || body.Capability == "" {
		writeError(w, 400, "capability is required")
		return
	}
	if body.Executor == "" {
		body.Executor = "executor"
	}
	req, claims, err := s.Engine.Spend(r.Context(), body.Capability, body.Executor)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"request": req, "claims": claims})
}

func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	rep, err := s.Engine.Verify(r.Context())
	if err != nil {
		s.writeErr(w, err)
		return
	}
	writeJSON(w, 200, rep)
}

// export serves the evidence bundle: every request, the signed chain head, every policy version
// and the public keys. Verify it offline with `kubectl sentinel audit verify --bundle <file>`.
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	ev, err := s.Engine.Evidence(r.Context(), "sentinel-guard "+s.Version)
	if err != nil {
		s.writeErr(w, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="sentinel-guard-evidence-`+
		s.Engine.Now().UTC().Format("20060102T150405Z")+`.json"`)
	writeJSON(w, 200, ev)
}

func (s *Server) writeErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, guard.ErrNotFound):
		writeError(w, 404, err.Error())
	case errors.Is(err, guard.ErrForbidden):
		writeError(w, 403, err.Error())
	case errors.Is(err, guard.ErrConflict):
		writeError(w, 409, err.Error())
	case errors.Is(err, guard.ErrInvalid):
		writeError(w, 400, err.Error())
	default:
		s.Log.Error("request failed", "error", err)
		writeError(w, 500, "internal error")
	}
}

func decodeBody(r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid JSON body: %v", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// Metrics are the guard's Prometheus series.
type Metrics struct {
	Verdicts  *prometheus.CounterVec
	Admission *prometheus.HistogramVec
	Events    *prometheus.CounterVec
	// Guardian monitoring: what each agent's proposals came to (drift in these is the signal),
	// and the state of oversight and of the audit trail (see Monitor).
	Outcomes      *prometheus.CounterVec
	Open          *prometheus.GaugeVec
	OldestPending prometheus.Gauge
	ChainOK       prometheus.Gauge
	HeadSeq       prometheus.Gauge
	Verified      prometheus.Gauge
	PolicyInfo    *prometheus.GaugeVec
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		Verdicts: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sentinel_guard_verdicts_total",
			Help: "Decisions on proposed agent actions."}, []string{"source", "action", "verdict"}),
		Admission: prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "sentinel_guard_admission_duration_seconds",
			Help:    "Time to answer an admission review.",
			Buckets: []float64{.001, .0025, .005, .01, .025, .05, .1, .25, .5, 1, 2.5}}, []string{"webhook", "allowed"}),
		Events: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sentinel_guard_audit_events_total",
			Help: "Events appended to the audit chain."}, []string{"type"}),
		Outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "sentinel_guard_agent_outcomes_total",
			Help: "What agents' proposals came to: proposed (waiting for a human), blocked, auto_approved, approved, denied, executed, expired."},
			[]string{"agent", "action", "outcome"}),
		Open: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "sentinel_guard_requests_open",
			Help: "Requests that can still lead to an execution, by phase."}, []string{"phase"}),
		OldestPending: prometheus.NewGauge(prometheus.GaugeOpts{Name: "sentinel_guard_oldest_pending_seconds",
			Help: "Age of the oldest request waiting for a human decision (0 when none is)."}),
		ChainOK: prometheus.NewGauge(prometheus.GaugeOpts{Name: "sentinel_guard_audit_chain_ok",
			Help: "1 if the last self-verification of the audit chain passed, 0 if it found a problem."}),
		HeadSeq: prometheus.NewGauge(prometheus.GaugeOpts{Name: "sentinel_guard_audit_head_seq",
			Help: "Sequence number of the audit chain head. It only grows; a decrease means the chain was rewound."}),
		Verified: prometheus.NewGauge(prometheus.GaugeOpts{Name: "sentinel_guard_audit_last_verified_timestamp_seconds",
			Help: "When this replica last verified the audit chain (Unix time)."}),
		PolicyInfo: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "sentinel_guard_policy_info",
			Help: "The policy version in force (1 for the current version)."}, []string{"version"}),
	}
	reg.MustRegister(m.Verdicts, m.Admission, m.Events, m.Outcomes, m.Open, m.OldestPending, m.ChainOK, m.HeadSeq, m.Verified, m.PolicyInfo)
	return m
}

// Hook wires the engine's verdicts and audit events to metrics and the structured audit log.
func (s *Server) Hook() {
	s.Engine.OnVerdict = func(p guard.Proposal, v guard.Verdict) {
		if s.Metrics != nil {
			s.Metrics.Verdicts.WithLabelValues(p.Source, p.Action, string(v)).Inc()
		}
	}
	s.Engine.OnVoid = func(e guard.Event, cause error) {
		s.Log.Info("audit place voided", "seq", e.Seq, "type", e.Type, "request", e.RequestID, "cause", cause.Error())
	}
	s.Engine.OnEvent = func(e guard.Event, r *guard.Request) {
		if s.Metrics != nil {
			s.Metrics.Events.WithLabelValues(e.Type).Inc()
			if o := guard.EventOutcome(e); o != "" {
				s.Metrics.Outcomes.WithLabelValues(r.Agent, r.Action, o).Inc()
			}
		}
		if s.Notify != nil {
			s.Notify.Observe(e, r)
		}
		s.Log.Info("audit", "seq", e.Seq, "type", e.Type, "request", r.ID, "agent", r.Agent, "action", r.Action,
			"target", r.Target.String(), "actor", e.Actor, "detail", e.Detail, "hash", e.Hash)
	}
}

func (s *Server) timed(webhook string, next func(http.ResponseWriter, *http.Request) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		allowed := next(w, r)
		if s.Metrics != nil {
			s.Metrics.Admission.WithLabelValues(webhook, fmt.Sprint(allowed)).Observe(time.Since(start).Seconds())
		}
	}
}
