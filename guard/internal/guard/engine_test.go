package guard_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/store"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/policies"
)

const agent = "system:serviceaccount:agents:fixer"

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

type fakeProber struct{ status string }

func (f fakeProber) Check(_ context.Context, s guard.PreconditionSpec, _ guard.Target) guard.Check {
	return guard.Check{Name: s.Name, Status: f.status}
}

func newEngine(t *testing.T, policyYAML string) (*guard.Engine, *store.Memory, *clock) {
	t.Helper()
	p, err := guard.ParsePolicy([]byte(policyYAML))
	if err != nil {
		t.Fatal(err)
	}
	key, _ := guard.GenerateKeyPEM()
	signer, err := guard.NewSigner(key, "sentinel-guard-test")
	if err != nil {
		t.Fatal(err)
	}
	mem := store.NewMemory()
	e := guard.NewEngine(mem, signer, nil, p)
	c := &clock{t: time.Now()}
	e.Now = c.now
	return e, mem, c
}

func defaultEngine(t *testing.T) (*guard.Engine, *store.Memory, *clock) {
	return newEngine(t, string(policies.Default))
}

func scaleTo(n string) guard.Proposal {
	return guard.Proposal{Source: guard.SourceAdmission, Agent: agent, Action: "scale",
		Target:      guard.Target{Group: "apps", Resource: "deployments", Namespace: "shop", Name: "checkout"},
		Change:      map[string]string{"replicas": "2 → " + n},
		Fingerprint: "replicas=" + n}
}

func mustEval(t *testing.T, e *guard.Engine, p guard.Proposal, want guard.Verdict) guard.Outcome {
	t.Helper()
	out, err := e.Evaluate(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if out.Verdict != want {
		t.Fatalf("verdict = %s, want %s (%s)", out.Verdict, want, out.Message)
	}
	return out
}

func TestApprovalAdmitsExactlyOnce(t *testing.T) {
	e, mem, _ := defaultEngine(t)
	ctx := context.Background()

	first := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	if !strings.Contains(first.Message, "kubectl sentinel approve "+first.Request.ID) {
		t.Fatalf("message should tell the human how to approve: %s", first.Message)
	}
	again := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	if again.Request.ID != first.Request.ID {
		t.Fatal("a retry while pending must not create a second request")
	}
	if _, err := e.Decide(ctx, first.Request.ID, "alice@example.com", nil, "", true, "looks right"); err != nil {
		t.Fatal(err)
	}
	admitted := mustEval(t, e, scaleTo("3"), guard.VerdictAllow)
	if admitted.Request.Phase != guard.PhaseExecuted {
		t.Fatalf("phase = %s", admitted.Request.Phase)
	}
	replay := mustEval(t, e, scaleTo("3"), guard.VerdictPending) // the approval is spent
	if replay.Request.ID == first.Request.ID {
		t.Fatal("a replay must open a new request")
	}
	reqs, _ := mem.List(ctx)
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
}

func TestApprovalIsBoundToTheExactChange(t *testing.T) {
	e, _, _ := defaultEngine(t)
	out := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	if _, err := e.Decide(context.Background(), out.Request.ID, "alice", nil, "", true, ""); err != nil {
		t.Fatal(err)
	}
	other := mustEval(t, e, scaleTo("5"), guard.VerdictPending)
	if other.Request.ID == out.Request.ID {
		t.Fatal("approving replicas=3 must not admit replicas=5")
	}
	otherTarget := scaleTo("3")
	otherTarget.Target.Name = "payments"
	mustEval(t, e, otherTarget, guard.VerdictPending)
	mustEval(t, e, scaleTo("3"), guard.VerdictAllow)
}

func TestWhoMayDecide(t *testing.T) {
	e, _, _ := defaultEngine(t)
	ctx := context.Background()
	out := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	if _, err := e.Decide(ctx, out.Request.ID, "system:serviceaccount:agents:other", []string{"system:serviceaccounts:agents"}, "", true, ""); !errors.Is(err, guard.ErrForbidden) {
		t.Fatalf("another agent approved: %v", err)
	}
	if _, err := e.Decide(ctx, out.Request.ID, agent, nil, "", true, ""); !errors.Is(err, guard.ErrForbidden) {
		t.Fatalf("the requester approved its own request: %v", err)
	}
	if _, err := e.Decide(ctx, out.Request.ID, "alice", nil, "", false, "not now"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Decide(ctx, out.Request.ID, "bob", nil, "", true, ""); !errors.Is(err, guard.ErrConflict) {
		t.Fatalf("a decided request was decided again: %v", err)
	}
	denied := mustEval(t, e, scaleTo("3"), guard.VerdictBlock)
	if !strings.Contains(denied.Message, "alice denied") {
		t.Fatalf("a human denial should stand: %s", denied.Message)
	}
}

func TestPolicyLimitsBlockAndCannotBeApproved(t *testing.T) {
	e, mem, _ := defaultEngine(t)
	ctx := context.Background()
	zero := mustEval(t, e, scaleTo("0"), guard.VerdictBlock)
	if !strings.Contains(zero.Message, "below the minimum 1") {
		t.Fatal(zero.Message)
	}
	if _, err := e.Decide(ctx, zero.Request.ID, "alice", nil, "", true, ""); !errors.Is(err, guard.ErrConflict) {
		t.Fatalf("a blocked request was approved: %v", err)
	}
	mustEval(t, e, scaleTo("0"), guard.VerdictBlock)
	reqs, _ := mem.List(ctx)
	if len(reqs) != 1 {
		t.Fatalf("repeated blocks should reuse one record, got %d", len(reqs))
	}
	sys := guard.Proposal{Source: guard.SourceAdmission, Agent: agent, Action: "delete", Fingerprint: "delete",
		Target: guard.Target{Resource: "namespaces", Name: "kube-system"}}
	mustEval(t, e, sys, guard.VerdictBlock)
}

func TestExpiry(t *testing.T) {
	e, _, c := defaultEngine(t)
	ctx := context.Background()
	first := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	c.advance(61 * time.Minute)
	second := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	if second.Request.ID == first.Request.ID {
		t.Fatal("an expired request must not be reused")
	}
	if _, err := e.Decide(ctx, second.Request.ID, "alice", nil, "", true, ""); err != nil {
		t.Fatal(err)
	}
	c.advance(11 * time.Minute) // approvals are usable for 10 minutes
	mustEval(t, e, scaleTo("3"), guard.VerdictPending)
}

func TestObserveAndAutoAutonomy(t *testing.T) {
	policy := string(policies.Default) + `
  restart:
    autonomy: auto
    preconditions:
      - {name: memory growth, query: 'up'}
`
	policy = strings.Replace(policy, "  restart:   { risk: medium }\n", "", 1)
	e, _, _ := newEngine(t, policy)
	restart := guard.Proposal{Source: guard.SourceAdmission, Agent: agent, Action: "restart", Fingerprint: "restart",
		Target: guard.Target{Group: "apps", Resource: "deployments", Namespace: "shop", Name: "checkout"}}

	e.Prober = fakeProber{guard.CheckNotObserved}
	pending := mustEval(t, e, restart, guard.VerdictPending)
	if len(pending.Request.Preconditions) != 1 || !strings.Contains(pending.Message, "not observed") {
		t.Fatalf("the human should see the failed precondition: %+v", pending)
	}
	e2, _, _ := newEngine(t, policy)
	e2.Prober = fakeProber{guard.CheckObserved}
	allowed := mustEval(t, e2, restart, guard.VerdictAllow)
	if allowed.Request.DecidedBy != "policy:auto" || allowed.Request.Phase != guard.PhaseExecuted {
		t.Fatalf("%+v", allowed.Request)
	}
	create := guard.Proposal{Source: guard.SourceAdmission, Agent: agent, Action: "create", Fingerprint: "create:x",
		Target: guard.Target{Group: "apps", Resource: "deployments", Namespace: "shop", Name: "new"}}
	observed := mustEval(t, e2, create, guard.VerdictAllow)
	if observed.Request.DecidedBy != "policy:observe" {
		t.Fatalf("observe mode should still record the action: %+v", observed.Request)
	}
}

func TestDryRunRecordsNothing(t *testing.T) {
	e, mem, _ := defaultEngine(t)
	p := scaleTo("3")
	p.DryRun = true
	out := mustEval(t, e, p, guard.VerdictPending)
	if out.Request != nil {
		t.Fatal("dry run created a request")
	}
	p = scaleTo("0")
	p.DryRun = true
	mustEval(t, e, p, guard.VerdictBlock)
	if reqs, _ := mem.List(context.Background()); len(reqs) != 0 {
		t.Fatalf("dry runs created %d requests", len(reqs))
	}
}

func TestAPICapabilityIsSingleUse(t *testing.T) {
	e, _, _ := defaultEngine(t)
	ctx := context.Background()
	p := guard.Proposal{Source: guard.SourceAPI, Agent: "refund-bot", Action: "refund", Intent: "duplicate charge",
		Target: guard.Target{Name: "order-1042"}, Change: map[string]string{"amount": "42.00 EUR"}, Fingerprint: "amount=42.00"}
	out := mustEval(t, e, p, guard.VerdictPending)
	if _, err := e.Capability(ctx, out.Request.ID, "someone-else"); !errors.Is(err, guard.ErrNotFound) {
		t.Fatalf("another agent saw the request: %v", err)
	}
	waiting, _ := e.Capability(ctx, out.Request.ID, "refund-bot")
	if waiting.Verdict != guard.VerdictPending || waiting.Capability != "" {
		t.Fatalf("capability before approval: %+v", waiting)
	}
	if _, err := e.Decide(ctx, out.Request.ID, "alice", nil, "", true, ""); err != nil {
		t.Fatal(err)
	}
	granted, err := e.Capability(ctx, out.Request.ID, "refund-bot")
	if err != nil || granted.Capability == "" {
		t.Fatalf("no capability after approval: %v %+v", err, granted)
	}
	if again, _ := e.Capability(ctx, out.Request.ID, "refund-bot"); again.Capability != "" {
		t.Fatal("the capability was issued twice")
	}
	r, claims, err := e.Spend(ctx, granted.Capability, "payments-executor")
	if err != nil || r.Phase != guard.PhaseExecuted || claims.Action != "refund" || claims.ApprovedBy != "alice" {
		t.Fatalf("spend: %v %+v %+v", err, r, claims)
	}
	before, _ := e.Store.Head(ctx)
	for range 3 {
		if _, _, err := e.Spend(ctx, granted.Capability, "payments-executor"); !errors.Is(err, guard.ErrConflict) {
			t.Fatalf("a capability was spent twice: %v", err)
		}
	}
	if after, _ := e.Store.Head(ctx); after.Seq != before.Seq || len(after.Voids) != 0 {
		t.Fatal("replaying a spent capability must not write to the audit chain")
	}
	forger, _, _ := defaultEngine(t) // a different key
	fake, _, _ := forger.Signer.MintCapability(r, time.Now().Add(time.Minute))
	if _, _, err := e.Spend(ctx, fake, "x"); !errors.Is(err, guard.ErrForbidden) {
		t.Fatalf("a forged capability was accepted: %v", err)
	}
	if _, _, err := e.Spend(ctx, granted.Capability[:len(granted.Capability)-4]+"AAAA", "x"); !errors.Is(err, guard.ErrForbidden) {
		t.Fatalf("a tampered capability was accepted: %v", err)
	}
}

func TestConcurrentRetriesAdmitOnce(t *testing.T) {
	e, mem, _ := defaultEngine(t)
	out := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	if _, err := e.Decide(context.Background(), out.Request.ID, "alice", nil, "", true, ""); err != nil {
		t.Fatal(err)
	}
	var allowed atomic.Int32
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if o, err := e.Evaluate(context.Background(), scaleTo("3")); err == nil && o.Verdict == guard.VerdictAllow {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 1 {
		t.Fatalf("admitted %d times, want exactly 1", allowed.Load())
	}
	// The other 49 found the approval spent and opened one new request between them.
	if reqs, _ := mem.List(context.Background()); len(reqs) != 2 {
		t.Fatalf("parallel identical attempts created %d requests, want 2", len(reqs))
	}
	if rep, err := e.Verify(context.Background()); err != nil || !rep.OK {
		t.Fatalf("after the race: %+v %v", rep, err)
	}
}

// loseRace makes the next state change fail as if another replica had made it first.
type loseRace struct {
	*store.Memory
	failIn int // fail the n-th Update from now (1 = the next one)
}

func (l *loseRace) Update(ctx context.Context, id string, fn func(*guard.Request) error) (*guard.Request, error) {
	if l.failIn > 0 {
		if l.failIn--; l.failIn == 0 {
			return nil, guard.ErrConflict
		}
	}
	return l.Memory.Update(ctx, id, fn)
}

func TestALostRaceLeavesASignedVoidNotAGap(t *testing.T) {
	e, mem, _ := defaultEngine(t)
	racy := &loseRace{Memory: mem}
	e.Store = racy
	ctx := context.Background()
	out := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	racy.failIn = 2 // Decide records the decision, then applies it: the second write loses
	if _, err := e.Decide(ctx, out.Request.ID, "alice", nil, "", true, ""); err == nil {
		t.Fatal("expected the injected conflict")
	}
	mustEval(t, e, scaleTo("4"), guard.VerdictPending)
	rep, _ := e.Verify(ctx)
	if !rep.OK || rep.Voided != 1 || rep.HeadSeq != 3 {
		t.Fatalf("%+v", rep)
	}
}

func TestVoidsCannotHideADeletion(t *testing.T) {
	e, mem, _ := defaultEngine(t)
	ctx := context.Background()
	a := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	mustEval(t, e, scaleTo("4"), guard.VerdictPending)
	mem.Delete(a.Request.ID) // event #1 is gone
	// Someone with write access to the chain head marks #1 void, without the guard's key.
	other, _, _ := defaultEngine(t)
	forged := guard.Void{Seq: 1, Hash: a.Request.Audit[0].Hash, Kid: e.Signer.KeyID()}
	forged.Sig, _ = other.Signer.SignHash(forged.SigningHash())
	_, _ = mem.AdvanceHead(ctx, func(h guard.Head) (guard.Head, error) {
		h.Voids = append(h.Voids, forged)
		return h, nil
	})
	rep, _ := e.Verify(ctx)
	if rep.OK || !strings.Contains(strings.Join(rep.Problems, ";"), "void #1 has an invalid signature") {
		t.Fatalf("a forged void hid a deletion: %+v", rep)
	}
}

func TestAuditChainDetectsTampering(t *testing.T) {
	e, mem, _ := defaultEngine(t)
	ctx := context.Background()
	a := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	_, _ = e.Decide(ctx, a.Request.ID, "alice", nil, "", true, "")
	mustEval(t, e, scaleTo("3"), guard.VerdictAllow)
	b := mustEval(t, e, scaleTo("4"), guard.VerdictPending)
	mustEval(t, e, scaleTo("0"), guard.VerdictBlock)

	rep, err := e.Verify(ctx)
	if err != nil || !rep.OK || rep.Events != 5 || rep.HeadSeq != 5 {
		t.Fatalf("clean chain: %+v %v", rep, err)
	}
	// Rewriting what was approved after the fact.
	_, _ = mem.Update(ctx, a.Request.ID, func(r *guard.Request) error { r.Change["replicas"] = "2 → 20"; return nil })
	if rep, _ = e.Verify(ctx); rep.OK || !strings.Contains(strings.Join(rep.Problems, ";"), "modified after") {
		t.Fatalf("edit not detected: %+v", rep)
	}
	_, _ = mem.Update(ctx, a.Request.ID, func(r *guard.Request) error { r.Change["replicas"] = "2 → 3"; return nil })
	// Deleting a record.
	mem.Delete(b.Request.ID)
	if rep, _ = e.Verify(ctx); rep.OK || !strings.Contains(strings.Join(rep.Problems, ";"), "missing events #4") {
		t.Fatalf("deletion not detected: %+v", rep)
	}
}

func TestReconcileAppliesDecisionsAndExpiry(t *testing.T) {
	e, mem, c := defaultEngine(t)
	ctx := context.Background()
	a := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	b := mustEval(t, e, scaleTo("4"), guard.VerdictPending)
	// A decision written straight into the store, as the Kubernetes approval webhook does.
	now := c.now()
	_, _ = mem.Update(ctx, a.Request.ID, func(r *guard.Request) error {
		r.Decision, r.DecidedBy, r.DecidedAt = "approve", "alice", &now
		return nil
	})
	if err := e.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if r, _ := mem.Get(ctx, a.Request.ID); r.Phase != guard.PhaseApproved {
		t.Fatalf("decision not applied: %s", r.Phase)
	}
	c.advance(2 * time.Hour)
	if err := e.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.Request.ID, b.Request.ID} {
		if r, _ := mem.Get(ctx, id); r.Phase != guard.PhaseExpired {
			t.Fatalf("%s not expired: %s", id, r.Phase)
		}
	}
	if rep, _ := e.Verify(ctx); !rep.OK {
		t.Fatalf("%+v", rep)
	}
}

func TestAPIServerRetryIsReadmittedButNewAttemptsAreNot(t *testing.T) {
	e, _, c := defaultEngine(t)
	gen := int64(7)
	restart := func(exact string) guard.Proposal {
		return guard.Proposal{Source: guard.SourceAdmission, Agent: agent, Action: "restart", Fingerprint: "restart", Exact: exact, Generation: gen,
			Target: guard.Target{Group: "apps", Resource: "deployments", Namespace: "shop", Name: "web"}}
	}
	out := mustEval(t, e, restart("exact:t1"), guard.VerdictPending)
	if _, err := e.Decide(context.Background(), out.Request.ID, "alice", nil, "", true, ""); err != nil {
		t.Fatal(err)
	}
	mustEval(t, e, restart("exact:t1"), guard.VerdictAllow)
	// The API server's write conflicted and it re-ran admission with the identical object.
	again := mustEval(t, e, restart("exact:t1"), guard.VerdictAllow)
	if again.Request.ID != out.Request.ID || !strings.Contains(again.Message, "re-admitted") {
		t.Fatalf("%+v", again)
	}
	// A new restart (kubectl stamps a new time) is a new action.
	mustEval(t, e, restart("exact:t2"), guard.VerdictPending)
	// The identical write after the change landed and was reverted (generation moved on) is new too.
	gen = 9
	mustEval(t, e, restart("exact:t1"), guard.VerdictPending)
	gen = 7
	for range guard.MaxReadmits - 1 {
		mustEval(t, e, restart("exact:t1"), guard.VerdictAllow)
	}
	mustEval(t, e, restart("exact:t1"), guard.VerdictPending) // retries are bounded
	c.advance(guard.ReadmitWindow)
	if rep, _ := e.Verify(context.Background()); !rep.OK {
		t.Fatalf("%+v", rep)
	}
}
