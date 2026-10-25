// Package server exposes the guard over HTTP: the action API for agents and executors, the two
// Kubernetes admission webhooks, the public keys and metrics.
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
	// GuardUser is the guard's own Kubernetes identity: the only one allowed to manage ActionRequests.
	GuardUser string
	// Generation reads a workload's metadata.generation (for Scale requests, which lack one).
	Generation func(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (int64, error)
	Metrics    *Metrics
	Log        *slog.Logger
	Ready      func() bool
	Version    string
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
	mux.HandleFunc("GET /v1/actions", s.adminOnly(s.list))
	mux.HandleFunc("POST /v1/actions/{id}/approve", s.deciders(s.decide(true)))
	mux.HandleFunc("POST /v1/actions/{id}/deny", s.deciders(s.decide(false)))
	mux.HandleFunc("POST /v1/capabilities/spend", s.spend)
	mux.HandleFunc("GET /v1/audit/verify", s.auditors(s.verify))
	mux.HandleFunc("GET /v1/audit/export", s.auditors(s.export))
	if admission {
		mux.HandleFunc("POST /admission/workloads", s.timed("workloads", s.admitWorkload))
		mux.HandleFunc("POST /admission/approvals", s.timed("approvals", s.admitApproval))
	}
	return mux
}

type identity struct {
	user   string
	groups []string
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
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, identity{user, groups})))
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
		if token != "" && s.Agents != nil {
			if user, groups, err := s.Agents.Authenticate(r.Context(), token); err == nil && s.Engine.Policy().IsDelegate(user, groups) {
				next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, identity{user, groups})))
				return
			}
		}
		if s.AdminToken == "" {
			writeError(w, 403, "only a configured delegate may decide over HTTP here: people approve with `kubectl sentinel approve <id>`")
			return
		}
		writeError(w, 401, "the admin token or a delegate's token is required")
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

func (s *Server) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.AdminToken == "" {
			writeError(w, 404, "HTTP approvals are disabled here: approve with `kubectl sentinel approve <id>`")
			return
		}
		if subtle.ConstantTimeCompare([]byte(bearer(r)), []byte(s.AdminToken)) != 1 {
			writeError(w, 401, "the admin token is required")
			return
		}
		next(w, r)
	}
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
		if err := decodeBody(r, &body); err != nil || body.Approver == "" {
			writeError(w, 400, "approver (who is deciding, for the audit trail) is required")
			return
		}
		via := r.Context().Value(ctxKey{}).(identity).user // empty for the admin token
		req, err := s.Engine.Decide(r.Context(), r.PathValue("id"), body.Approver, nil, via, approve, body.Note)
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
