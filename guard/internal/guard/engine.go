package guard

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Verdict is the guard's answer to a proposal.
type Verdict string

const (
	VerdictAllow   Verdict = "allow"   // go ahead (approved, or policy lets it run on its own)
	VerdictPending Verdict = "pending" // a human must approve first
	VerdictBlock   Verdict = "block"   // not allowed (policy limit, or a human denied it)
)

// Readmission of an identical result is limited to the API server's retry pattern: a few
// attempts within seconds (it retries a conflicting patch up to 5 times).
const (
	ReadmitWindow = 10 * time.Second
	MaxReadmits   = 5
)

var (
	ErrForbidden = errors.New("forbidden")
	ErrInvalid   = errors.New("invalid")
	errStale     = errors.New("request changed state")
	errDuplicate = errors.New("request created concurrently")
)

type Outcome struct {
	Verdict    Verdict  `json:"verdict"`
	Request    *Request `json:"request,omitempty"`
	Reasons    []string `json:"reasons,omitempty"`
	Message    string   `json:"message"`
	Capability string   `json:"capability,omitempty"`
}

// Engine runs the request lifecycle over a Store. It is safe to run in several replicas at once:
// every state change is a compare-and-swap in the store, so an approval is consumed exactly once.
type Engine struct {
	Store  Store
	Signer *Signer
	Prober Prober
	Now    func() time.Time
	// Optional hooks for metrics and the structured audit log.
	OnVerdict func(p Proposal, v Verdict)
	OnEvent   func(e Event, r *Request)
	OnVoid    func(e Event, cause error)

	policy atomic.Pointer[Policy]
}

func NewEngine(store Store, signer *Signer, prober Prober, policy *Policy) *Engine {
	e := &Engine{Store: store, Signer: signer, Prober: prober, Now: time.Now}
	e.policy.Store(policy)
	return e
}

func (e *Engine) Policy() *Policy     { return e.policy.Load() }
func (e *Engine) SetPolicy(p *Policy) { e.policy.Store(p) }

// Evaluate decides what happens to a proposed action. For a Kubernetes change, "allow" means the
// API request is admitted; for an API agent, it comes with a single-use capability.
func (e *Engine) Evaluate(ctx context.Context, p Proposal) (Outcome, error) {
	out, err := e.evaluate(ctx, p)
	for attempt := 0; errors.Is(err, errDuplicate) && attempt < 5; attempt++ {
		out, err = e.evaluate(ctx, p) // a concurrent attempt created the request first: use it
	}
	if err == nil && e.OnVerdict != nil {
		e.OnVerdict(p, out.Verdict)
	}
	return out, err
}

func (e *Engine) evaluate(ctx context.Context, p Proposal) (Outcome, error) {
	pol := e.Policy()
	ap := pol.For(p.Action)
	key := p.Key()

	if v := pol.Violations(p.Action, p.Target, p.Change); len(v) > 0 {
		out := Outcome{Verdict: VerdictBlock, Reasons: v}
		if !p.DryRun {
			r, err := e.recent(ctx, key, PhaseBlocked)
			if err != nil {
				return out, err
			}
			if r == nil {
				if r, err = e.create(ctx, p, ap, PhaseBlocked, v, nil); err != nil {
					return out, err
				}
			}
			out.Request = r
		}
		out.Message = fmt.Sprintf("Sentinel Guard blocked %s of %s: %s.", p.Action, p.Target, strings.Join(v, "; "))
		return out, nil
	}

	if !p.DryRun {
		if out, done, err := e.useExisting(ctx, p, key); done || err != nil {
			return out, err
		}
	}

	var checks []Check
	if ap.Autonomy != AutonomyObserve {
		checks = e.check(ctx, ap, p.Target)
	}
	runsAlone := ap.Autonomy == AutonomyObserve || ap.Autonomy == AutonomyAuto && allObserved(checks)
	if p.DryRun {
		if runsAlone {
			return Outcome{Verdict: VerdictAllow, Message: "allowed by policy (" + ap.Autonomy + ")"}, nil
		}
		return Outcome{Verdict: VerdictPending, Message: fmt.Sprintf("Sentinel Guard: %s of %s would need human approval.", p.Action, p.Target)}, nil
	}
	if runsAlone {
		r, err := e.create(ctx, p, ap, PhaseApproved, nil, checks)
		if err != nil {
			return Outcome{}, err
		}
		return e.proceed(ctx, p, r)
	}

	var reasons []string
	if ap.Autonomy == AutonomyAuto {
		reasons = append(reasons, "not every precondition was observed, so a human must approve")
	}
	r, err := e.create(ctx, p, ap, PhasePending, reasons, checks)
	if err != nil {
		return Outcome{}, err
	}
	return e.pending(r), nil
}

// useExisting resolves a proposal against earlier requests for exactly the same change: an
// approval is consumed, a pending request is reported again (no duplicates while an agent
// retries), and a recent human denial stands.
func (e *Engine) useExisting(ctx context.Context, p Proposal, key string) (Outcome, bool, error) {
	reqs, err := e.Store.FindByKey(ctx, key)
	if err != nil {
		return Outcome{}, false, err
	}
	now := e.Now()
	// First: is this the API server retrying a write that was just admitted?
	for _, r := range reqs {
		if r.Phase == PhaseExecuted && p.Exact != "" && r.Exact == p.Exact && r.ExecutedAt != nil &&
			now.Sub(*r.ExecutedAt) < ReadmitWindow && readmits(r) < MaxReadmits && r.ExecGeneration != 0 &&
			e.generation(ctx, p) == r.ExecGeneration {
			out, err := e.readmit(ctx, r)
			if errors.Is(err, errStale) {
				continue
			}
			return out, true, err
		}
	}
	sort.Slice(reqs, func(i, j int) bool { return reqs[i].CreatedAt.After(reqs[j].CreatedAt) })
	for _, r := range reqs {
		if r.Phase.Open() && !now.Before(r.ExpiresAt) {
			_, _ = e.expire(ctx, r)
			continue
		}
		if r.Phase == PhasePending && r.Decision != "" {
			if r, err = e.finalize(ctx, r); err != nil {
				return Outcome{}, false, err
			}
		}
		switch r.Phase {
		case PhaseApproved:
			out, err := e.proceed(ctx, p, r)
			if errors.Is(err, errStale) {
				continue // another replica consumed it first: this attempt is not covered
			}
			return out, true, err
		case PhasePending:
			return e.pending(r), true, nil
		case PhaseDenied:
			if r.DecidedAt != nil && now.Sub(*r.DecidedAt) < e.Policy().PendingTTL.Duration {
				return Outcome{Verdict: VerdictBlock, Request: r, Message: fmt.Sprintf(
					"Sentinel Guard: %s denied %s of %s (request %s).", r.DecidedBy, r.Action, r.Target, r.ID)}, true, nil
			}
		}
	}
	return Outcome{}, false, nil
}

// proceed turns an approved request into an allowed action: admitted once for Kubernetes, or a
// single-use capability for an API agent.
func (e *Engine) proceed(ctx context.Context, p Proposal, r *Request) (Outcome, error) {
	if p.Source == SourceAdmission {
		r, err := e.execute(ctx, r, "", "kubernetes-admission", p.Exact, e.generation(ctx, p))
		if err != nil {
			return Outcome{}, err
		}
		return Outcome{Verdict: VerdictAllow, Request: r, Message: fmt.Sprintf(
			"Sentinel Guard: %s of %s admitted once (request %s, approved by %s).", r.Action, r.Target, r.ID, r.DecidedBy)}, nil
	}
	return e.issueCapability(ctx, r, p.Agent)
}

func (e *Engine) pending(r *Request) Outcome {
	msg := fmt.Sprintf("Sentinel Guard: %s of %s needs human approval (request %s). Approve with: kubectl sentinel approve %s. "+
		"Then retry the same change; it will be admitted once.", r.Action, r.Target, r.ID, r.ID)
	if r.Source == SourceAPI {
		msg = fmt.Sprintf("Sentinel Guard: %s of %s needs human approval (request %s).", r.Action, r.Target, r.ID)
	}
	for _, c := range r.Preconditions {
		msg += fmt.Sprintf(" Precondition %q: %s.", c.Name, strings.ReplaceAll(c.Status, "_", " "))
	}
	return Outcome{Verdict: VerdictPending, Request: r, Reasons: r.Reasons, Message: msg}
}

func (e *Engine) check(ctx context.Context, ap ActionPolicy, t Target) []Check {
	if len(ap.Preconditions) == 0 || e.Prober == nil {
		return nil
	}
	out := make([]Check, 0, len(ap.Preconditions))
	for _, spec := range ap.Preconditions {
		out = append(out, e.Prober.Check(ctx, spec, t))
	}
	return out
}

func (e *Engine) recent(ctx context.Context, key string, phase Phase) (*Request, error) {
	reqs, err := e.Store.FindByKey(ctx, key)
	if err != nil {
		return nil, err
	}
	for _, r := range reqs {
		if r.Phase == phase && e.Now().Sub(r.CreatedAt) < e.Policy().PendingTTL.Duration {
			return r, nil
		}
	}
	return nil, nil
}

func (e *Engine) create(ctx context.Context, p Proposal, ap ActionPolicy, phase Phase, reasons []string, checks []Check) (*Request, error) {
	now := e.Now().UTC()
	id, err := e.nextID(ctx, p.Key())
	if err != nil {
		return nil, err
	}
	r := &Request{
		ID: id, Key: p.Key(), Source: p.Source, Agent: p.Agent, Action: p.Action, Target: p.Target,
		Change: p.Change, Fingerprint: p.Fingerprint, Intent: p.Intent, Risk: ap.Risk, Autonomy: ap.Autonomy,
		CreatedAt: now, Phase: phase, Reasons: reasons, Preconditions: checks,
		ExpiresAt: now.Add(e.Policy().PendingTTL.Duration),
	}
	typ, actor, detail := EventRequested, p.Agent, ""
	switch phase {
	case PhaseBlocked:
		typ, detail = EventBlocked, strings.Join(reasons, "; ")
	case PhaseApproved: // policy lets it run on its own: decided at creation
		actor = "policy:" + ap.Autonomy
		typ, detail = EventApproved, "allowed by policy"
		r.Decision, r.DecidedBy, r.DecidedAt = "approve", actor, &now
		r.ExpiresAt = now.Add(e.Policy().ApprovalTTL.Duration)
	}
	ev, err := e.chain(ctx, r, typ, actor, detail)
	if err != nil {
		return nil, err
	}
	r.Audit = []Event{ev}
	if err := e.Store.Create(ctx, r); err != nil {
		e.void(ctx, ev)
		if errors.Is(err, ErrConflict) {
			return nil, errDuplicate
		}
		return nil, err
	}
	e.emit(ev, r)
	return r, nil
}

// nextID names requests after their approval key and a counter (ar-<key>-<n>). Concurrent
// identical attempts therefore pick the same name and the store's create-if-absent keeps one,
// so an agent retrying in parallel can't flood approvers with duplicates.
func (e *Engine) nextID(ctx context.Context, key string) (string, error) {
	reqs, err := e.Store.FindByKey(ctx, key)
	if err != nil {
		return "", err
	}
	n := 0
	now := e.Now()
	for _, r := range reqs {
		if r.Phase.Open() && now.Before(r.ExpiresAt) ||
			r.Phase == PhaseBlocked && now.Sub(r.CreatedAt) < e.Policy().PendingTTL.Duration {
			return "", errDuplicate // created since we looked: go back and use it
		}
		if i := strings.LastIndex(r.ID, "-"); i > 0 {
			if k, err := strconv.Atoi(r.ID[i+1:]); err == nil && k > n {
				n = k
			}
		}
	}
	return fmt.Sprintf("ar-%s-%d", key[:8], n+1), nil
}

// transition changes a request's state and records the event in one atomic write: the event's
// place in the chain is claimed first, then fn and the event are applied together. If fn then
// refuses (another replica got there in the meantime) or the write fails, the claimed place is
// recorded as a signed void, so the chain stays complete and a deleted record still shows as a gap.
func (e *Engine) transition(ctx context.Context, r *Request, typ, actor, detail string, fn func(*Request) error) (*Request, error) {
	// Check against the current state before claiming a place in the chain, so a refused attempt
	// (a replayed capability, a request already decided) costs no write and leaves no void.
	cur, err := e.Store.Get(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	if err := fn(cur); err != nil {
		return nil, err
	}
	ev, err := e.chain(ctx, r, typ, actor, detail)
	if err != nil {
		return nil, err
	}
	updated, err := e.Store.Update(ctx, r.ID, func(x *Request) error {
		if err := fn(x); err != nil {
			return err
		}
		x.Audit = append(x.Audit, ev)
		return nil
	})
	if err != nil {
		e.void(ctx, ev)
		if e.OnVoid != nil {
			e.OnVoid(ev, err)
		}
		return nil, err
	}
	e.emit(ev, updated)
	return updated, nil
}

func (e *Engine) void(ctx context.Context, ev Event) {
	v := Void{Seq: ev.Seq, Hash: ev.Hash, Kid: e.Signer.KeyID()}
	sig, err := e.Signer.SignHash(v.SigningHash())
	if err != nil {
		return
	}
	v.Sig = sig
	// Best effort: if this write fails too, verification reports the gap, which is the honest result.
	_, _ = e.Store.AdvanceHead(ctx, func(h Head) (Head, error) {
		h.Voids = append(append([]Void(nil), h.Voids...), v)
		return h, nil
	})
}

// chain claims the next sequence number in the global audit chain and signs the event.
func (e *Engine) chain(ctx context.Context, r *Request, typ, actor, detail string) (Event, error) {
	digest := r.Digest()
	var ev Event
	_, err := e.Store.AdvanceHead(ctx, func(h Head) (Head, error) {
		ev = Event{Seq: h.Seq + 1, Prev: h.Hash, Type: typ, At: e.Now().UTC().Format(time.RFC3339Nano), Actor: actor,
			Detail: detail, RequestID: r.ID, Digest: digest, Kid: e.Signer.KeyID()}
		ev.Hash = ev.ComputeHash()
		sig, err := e.Signer.SignHash(ev.Hash)
		ev.Sig = sig
		return Head{Seq: ev.Seq, Hash: ev.Hash, Voids: h.Voids}, err
	})
	return ev, err
}

func (e *Engine) emit(ev Event, r *Request) {
	if e.OnEvent != nil {
		e.OnEvent(ev, r)
	}
}

func readmits(r *Request) int {
	n := 0
	for _, ev := range r.Audit {
		if ev.Type == EventReadmitted {
			n++
		}
	}
	return n
}

// readmit admits the API server's retry of an executed request. The resulting object is identical,
// so applying it again cannot change anything a second time.
func (e *Engine) readmit(ctx context.Context, r *Request) (Outcome, error) {
	r, err := e.transition(ctx, r, EventReadmitted, "kubernetes-admission", "API server retried the same write", func(x *Request) error {
		if x.Phase != PhaseExecuted || readmits(x) >= MaxReadmits {
			return errStale
		}
		return nil
	})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Verdict: VerdictAllow, Request: r, Message: fmt.Sprintf(
		"Sentinel Guard: %s of %s re-admitted: the API server retried the write approved as %s.", r.Action, r.Target, r.ID)}, nil
}

// generation is the target's generation before the change, or 0 if it can't be known (then an
// API server retry is not re-admitted: safe, at the cost of a fresh approval in that rare case).
func (e *Engine) generation(ctx context.Context, p Proposal) int64 {
	if p.Generation != 0 || p.LookupGeneration == nil {
		return p.Generation
	}
	g, err := p.LookupGeneration(ctx)
	if err != nil {
		return 0
	}
	return g
}

func (e *Engine) execute(ctx context.Context, req *Request, jti, actor, exact string, gen int64) (*Request, error) {
	now := e.Now()
	return e.transition(ctx, req, EventExecuted, actor, "", func(r *Request) error {
		if r.Phase != PhaseApproved {
			return fmt.Errorf("%w: request %s is %s", errStale, r.ID, r.Phase)
		}
		if !now.Before(r.ExpiresAt) {
			return fmt.Errorf("%w: approval of %s expired", errStale, r.ID)
		}
		if jti != r.CapabilityJTI {
			return fmt.Errorf("%w: capability %s is not the one issued for %s", errStale, jti, r.ID)
		}
		r.Phase, r.ExecutedAt, r.Exact, r.ExecGeneration = PhaseExecuted, &now, exact, gen
		return nil
	})
}

func (e *Engine) issueCapability(ctx context.Context, r *Request, agent string) (Outcome, error) {
	if r.CapabilityJTI != "" {
		return Outcome{Verdict: VerdictBlock, Request: r, Message: "the capability for " + r.ID + " was already issued"}, nil
	}
	token, jti, err := e.Signer.MintCapability(r, r.ExpiresAt)
	if err != nil {
		return Outcome{}, err
	}
	r, err = e.transition(ctx, r, EventCapabilityIssued, agent, jti, func(x *Request) error {
		if x.Phase != PhaseApproved || x.CapabilityJTI != "" {
			return fmt.Errorf("%w: capability for %s already issued", errStale, x.ID)
		}
		x.CapabilityJTI = jti
		return nil
	})
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{Verdict: VerdictAllow, Request: r, Capability: token, Message: fmt.Sprintf(
		"Sentinel Guard: %s of %s approved by %s. The capability is valid once, until %s.", r.Action, r.Target,
		r.DecidedBy, r.ExpiresAt.UTC().Format(time.RFC3339))}, nil
}

// Capability returns the single-use capability for an approved API request (to its own agent only).
func (e *Engine) Capability(ctx context.Context, id, agent string) (Outcome, error) {
	r, err := e.Store.Get(ctx, id)
	if err != nil {
		return Outcome{}, err
	}
	if r.Agent != agent {
		return Outcome{}, ErrNotFound // don't reveal other agents' requests
	}
	if r.Phase == PhasePending && r.Decision != "" {
		if r, err = e.Finalize(ctx, id); err != nil {
			return Outcome{}, err
		}
	}
	if r.Phase.Open() && !e.Now().Before(r.ExpiresAt) {
		if r, err = e.expire(ctx, r); err != nil {
			return Outcome{}, err
		}
	}
	switch r.Phase {
	case PhaseApproved:
		return e.issueCapability(ctx, r, agent)
	case PhasePending:
		return e.pending(r), nil
	}
	return Outcome{Verdict: VerdictBlock, Request: r, Message: fmt.Sprintf("request %s is %s", r.ID, r.Phase)}, nil
}

// Spend verifies a capability and consumes it, atomically. An executor calls this immediately
// before acting; a second call with the same capability fails.
func (e *Engine) Spend(ctx context.Context, token, executor string) (*Request, *CapabilityClaims, error) {
	claims, err := e.Signer.VerifyCapability(token)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	req, err := e.Store.Get(ctx, claims.RequestID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: capability for an unknown request", ErrForbidden)
	}
	r, err := e.execute(ctx, req, claims.ID, executor, "", 0)
	if errors.Is(err, errStale) {
		return nil, nil, fmt.Errorf("%w: capability already used or no longer valid", ErrConflict)
	}
	return r, claims, err
}

// CanDecide is the rule for a human decision, shared by the HTTP API and the Kubernetes approval
// webhook: only an open, undecided, unexpired request, never by the agent that asked.
func CanDecide(r *Request, decider string, isAgent bool, now time.Time) error {
	switch {
	case isAgent:
		return fmt.Errorf("%w: agents cannot approve or deny actions", ErrForbidden)
	case decider == r.Agent:
		return fmt.Errorf("%w: the requesting identity cannot decide its own request", ErrForbidden)
	case r.Phase != PhasePending || r.Decision != "":
		return fmt.Errorf("%w: request %s is already %s", ErrConflict, r.ID, strings.ToLower(string(r.Phase)))
	case !now.Before(r.ExpiresAt):
		return fmt.Errorf("%w: request %s has expired", ErrConflict, r.ID)
	}
	return nil
}

// Decide records a human decision (HTTP API) and applies it. via is the delegate that
// authenticated the decider (empty when the decider called directly); neither may be the requester.
func (e *Engine) Decide(ctx context.Context, id, decider string, groups []string, via string, approve bool, note string) (*Request, error) {
	now := e.Now()
	isAgent := e.Policy().IsAgent(decider, groups)
	if _, err := e.Store.Update(ctx, id, func(r *Request) error {
		if err := CanDecide(r, decider, isAgent, now); err != nil {
			return err
		}
		if via != "" && via == r.Agent {
			return fmt.Errorf("%w: the requesting identity cannot decide its own request", ErrForbidden)
		}
		r.Decision, r.DecidedBy, r.DecidedVia, r.DecisionNote, r.DecidedAt = "deny", decider, via, note, &now
		if approve {
			r.Decision = "approve"
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return e.Finalize(ctx, id)
}

// Finalize applies a recorded decision: Pending becomes Approved (with its execution window) or
// Denied. In Kubernetes the decision is written by the approver through the API server (the
// approval webhook stamps who); any replica may then finalize it.
func (e *Engine) Finalize(ctx context.Context, id string) (*Request, error) {
	r, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return e.finalize(ctx, r)
}

func (e *Engine) finalize(ctx context.Context, r *Request) (*Request, error) {
	if r.Phase != PhasePending || r.Decision == "" {
		return r, nil
	}
	id := r.ID
	typ := EventDenied
	if r.Decision == "approve" {
		typ = EventApproved
	}
	detail := r.DecisionNote
	if r.DecidedVia != "" {
		detail = strings.TrimSpace(detail + " [via " + r.DecidedVia + "]")
	}
	r2, err := e.transition(ctx, r, typ, r.DecidedBy, detail, func(x *Request) error {
		if x.Phase != PhasePending {
			return errStale
		}
		if x.Decision == "approve" {
			x.Phase, x.ExpiresAt = PhaseApproved, e.Now().Add(e.Policy().ApprovalTTL.Duration)
		} else {
			x.Phase = PhaseDenied
		}
		return nil
	})
	if errors.Is(err, errStale) {
		return e.Store.Get(ctx, id) // another replica finalized it
	}
	return r2, err
}

func (e *Engine) expire(ctx context.Context, req *Request) (*Request, error) {
	id := req.ID
	r, err := e.transition(ctx, req, EventExpired, "guard", "", func(x *Request) error {
		if !x.Phase.Open() {
			return errStale
		}
		x.Phase = PhaseExpired
		return nil
	})
	if errors.Is(err, errStale) {
		return e.Store.Get(ctx, id)
	}
	return r, err
}

// Reconcile applies decisions and expiries. Each replica runs it periodically; the store's
// compare-and-swap makes concurrent runs safe.
func (e *Engine) Reconcile(ctx context.Context) error {
	reqs, err := e.Store.List(ctx)
	if err != nil {
		return err
	}
	now := e.Now()
	var errs []error
	for _, r := range reqs {
		switch {
		case r.Phase == PhasePending && r.Decision != "":
			_, err = e.Finalize(ctx, r.ID)
		case r.Phase.Open() && !now.Before(r.ExpiresAt):
			_, err = e.expire(ctx, r)
		default:
			continue
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Verify checks the whole audit chain with the guard's own public key.
func (e *Engine) Verify(ctx context.Context) (AuditReport, error) {
	reqs, err := e.Store.List(ctx)
	if err != nil {
		return AuditReport{}, err
	}
	head, err := e.Store.Head(ctx)
	if err != nil {
		return AuditReport{}, err
	}
	return VerifyAudit(reqs, head, e.Signer.PublicKeys()), nil
}
