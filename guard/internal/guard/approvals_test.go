package guard_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
)

const twoPersonPolicy = `
agents: {users: ["system:serviceaccount:agents:fixer"]}
defaults: {risk: medium, autonomy: approve}
actions:
  scale: {risk: high, approvals: 2}
`

func verifies(t *testing.T, e *guard.Engine) guard.EvidenceReport {
	t.Helper()
	ev, err := e.Evidence(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	rep := guard.VerifyEvidence(roundTrip(t, ev), nil)
	if !rep.OK {
		t.Fatalf("evidence should verify: %v", rep.Problems)
	}
	return rep
}

func TestTwoPersonApproval(t *testing.T) {
	e, _, c := newEngine(t, twoPersonPolicy)
	ctx := context.Background()
	out := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	id := out.Request.ID
	if out.Request.RequiredApprovals != 2 {
		t.Fatalf("required approvals = %d, want 2", out.Request.RequiredApprovals)
	}

	c.advance(30 * time.Second)
	r, err := e.Decide(ctx, id, "alice", nil, "", true, "looks right")
	if err != nil {
		t.Fatal(err)
	}
	if r.Phase != guard.PhasePending || len(r.Approvals) != 1 || r.Approvals[0].By != "alice" || r.Decision != "" {
		t.Fatalf("one approval of two must leave the request pending: %+v", r)
	}
	if last := r.Audit[len(r.Audit)-1]; last.Type != guard.EventApprovalAdded || last.Actor != "alice" || last.Detail != "looks right" {
		t.Fatalf("the approval must be a signed event: %+v", last)
	}
	mustEval(t, e, scaleTo("3"), guard.VerdictPending) // not admitted on one approval

	if _, err := e.Decide(ctx, id, "alice", nil, "", true, "again"); !errors.Is(err, guard.ErrForbidden) {
		t.Fatalf("the same person approving twice: %v", err)
	}
	if _, err := e.Decide(ctx, id, agent, nil, "", true, ""); !errors.Is(err, guard.ErrForbidden) {
		t.Fatalf("the requesting agent must not count: %v", err)
	}
	if _, err := e.Decide(ctx, id, "bob", nil, "alice", true, ""); err != nil {
		t.Fatalf("a different person, via a delegate: %v", err)
	}

	r, _ = e.Finalize(ctx, id)
	if r.Phase != guard.PhaseApproved || r.DecidedBy != "bob" || r.DecidedVia != "alice" {
		t.Fatalf("the second person's approval decides: %+v", r)
	}
	mustEval(t, e, scaleTo("3"), guard.VerdictAllow)

	rep := verifies(t, e)
	if rep.Summary.MultiPersonApproved != 1 || rep.Summary.Deciders["alice"] != 1 || rep.Summary.Deciders["bob via alice"] != 1 {
		t.Fatalf("summary: %+v", rep.Summary)
	}
}

func TestTwoPersonCapabilityNamesBothApprovers(t *testing.T) {
	e, _, _ := newEngine(t, twoPersonPolicy)
	ctx := context.Background()
	p := scaleTo("3")
	p.Source = guard.SourceAPI
	id := mustEval(t, e, p, guard.VerdictPending).Request.ID
	for _, who := range []string{"alice", "bob"} {
		if _, err := e.Decide(ctx, id, who, nil, "", true, ""); err != nil {
			t.Fatal(err)
		}
	}
	out, err := e.Capability(ctx, id, agent)
	if err != nil || out.Verdict != guard.VerdictAllow {
		t.Fatalf("capability: %v %+v", err, out)
	}
	_, claims, err := e.Spend(ctx, out.Capability, "executor")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(claims.Approvers, ",") != "alice,bob" || claims.ApprovedBy != "bob" {
		t.Fatalf("an executor must see who approved: %+v", claims)
	}
}

func TestOneDenialEndsATwoPersonRequest(t *testing.T) {
	e, _, _ := newEngine(t, twoPersonPolicy)
	ctx := context.Background()
	id := mustEval(t, e, scaleTo("3"), guard.VerdictPending).Request.ID
	if _, err := e.Decide(ctx, id, "alice", nil, "", true, ""); err != nil {
		t.Fatal(err)
	}
	r, err := e.Decide(ctx, id, "carol", nil, "", false, "not during the sale")
	if err != nil || r.Phase != guard.PhaseDenied || r.DecidedBy != "carol" {
		t.Fatalf("a denial is final, whatever came before: %v %+v", err, r)
	}
	verifies(t, e)
}

// In Kubernetes the approval webhook writes each early approval into spec.approvals (as the
// authenticated user); the guard signs it. A plain "approve" written straight into the store when
// more people are needed becomes one approval, never the decision.
func TestApprovalsWrittenThroughTheAPIServerAreSigned(t *testing.T) {
	e, mem, c := newEngine(t, twoPersonPolicy)
	ctx := context.Background()
	id := mustEval(t, e, scaleTo("3"), guard.VerdictPending).Request.ID

	if _, err := mem.Update(ctx, id, func(r *guard.Request) error {
		r.Approvals = append(r.Approvals, guard.Approval{By: "alice", At: c.now()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	r, _ := mem.Get(ctx, id)
	if last := r.Audit[len(r.Audit)-1]; last.Type != guard.EventApprovalAdded || last.Actor != "alice" || r.Phase != guard.PhasePending {
		t.Fatalf("reconcile must sign the approval: %+v", r.Audit)
	}
	if err := e.Reconcile(ctx); err != nil { // idempotent
		t.Fatal(err)
	}
	if r, _ = mem.Get(ctx, id); len(r.Audit) != 2 {
		t.Fatalf("an approval is signed once: %d events", len(r.Audit))
	}

	decide := func(who string) {
		now := c.now()
		if _, err := mem.Update(ctx, id, func(r *guard.Request) error {
			r.Decision, r.DecidedBy, r.DecidedAt = "approve", who, &now
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	decide("alice") // the same person again: dropped
	if r, _ = mem.Get(ctx, id); r.Phase != guard.PhasePending || r.Decision != "" || len(r.Approvals) != 1 {
		t.Fatalf("a second approval by the same person must not decide: %+v", r)
	}
	decide("bob")
	if r, _ = mem.Get(ctx, id); r.Phase != guard.PhaseApproved || r.DecidedBy != "bob" {
		t.Fatalf("the second person decides: %+v", r)
	}
	verifies(t, e)
}

func TestTooFewApprovalsWrittenIntoTheStoreAreDemoted(t *testing.T) {
	e, mem, c := newEngine(t, `
agents: {users: ["system:serviceaccount:agents:fixer"]}
actions:
  scale: {approvals: 3}
`)
	ctx := context.Background()
	id := mustEval(t, e, scaleTo("3"), guard.VerdictPending).Request.ID
	now := c.now()
	if _, err := mem.Update(ctx, id, func(r *guard.Request) error {
		r.Decision, r.DecidedBy, r.DecidedAt, r.DecisionNote = "approve", "alice", &now, "first"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r, err := e.Finalize(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Phase != guard.PhasePending || r.Decision != "" || len(r.Approvals) != 1 || r.Approvals[0].Note != "first" {
		t.Fatalf("one approval of three must stay one approval: %+v", r)
	}
	verifies(t, e)
}

func TestConcurrentApproversDecideOnce(t *testing.T) {
	e, mem, _ := newEngine(t, twoPersonPolicy)
	ctx := context.Background()
	id := mustEval(t, e, scaleTo("3"), guard.VerdictPending).Request.ID
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = e.Decide(ctx, id, fmt.Sprintf("person-%d", i), nil, "", true, "")
		}()
	}
	wg.Wait()
	r, _ := mem.Get(ctx, id)
	if r.Phase != guard.PhaseApproved || len(r.Approvals) != 1 || r.Approvals[0].By == r.DecidedBy {
		t.Fatalf("exactly two different people approve: %+v", r)
	}
	verifies(t, e)
}

func TestTamperingWithApprovalsIsDetected(t *testing.T) {
	e, _, _ := newEngine(t, twoPersonPolicy)
	ctx := context.Background()
	id := mustEval(t, e, scaleTo("3"), guard.VerdictPending).Request.ID
	for _, who := range []string{"alice", "bob"} {
		if _, err := e.Decide(ctx, id, who, nil, "", true, ""); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		edit func(r *guard.Request)
		want string
	}{
		"fewer people required": {func(r *guard.Request) { r.RequiredApprovals = 1 }, "was modified after event"},
		"an approval removed":   {func(r *guard.Request) { r.Approvals = nil }, "does not list alice's signed approval"},
		"who approved first":    {func(r *guard.Request) { r.Approvals[0].By = "mallory" }, "does not list alice's signed approval"},
		"an approval added": {func(r *guard.Request) { r.Approvals = append(r.Approvals, guard.Approval{By: "mallory"}) },
			"lists 2 approval(s), but only 1 are signed"},
	} {
		t.Run(name, func(t *testing.T) {
			ev, err := e.Evidence(ctx, "test")
			if err != nil {
				t.Fatal(err)
			}
			ev = roundTrip(t, ev)
			for _, r := range ev.Requests {
				if r.ID == id {
					tc.edit(r)
				}
			}
			rep := guard.VerifyEvidence(ev, nil)
			if rep.OK || !strings.Contains(strings.Join(rep.Problems, "\n"), tc.want) {
				t.Fatalf("the edit must fail verification with %q: %v", tc.want, rep.Problems)
			}
		})
	}
}

func TestApprovalsPolicy(t *testing.T) {
	if _, err := guard.ParsePolicy([]byte("actions: {scale: {approvals: 6}}")); err == nil {
		t.Fatal("more than 5 approvals must be refused")
	}
	p, err := guard.ParsePolicy([]byte("defaults: {approvals: 2}\nactions: {scale: {risk: high}, restart: {approvals: 1}}"))
	if err != nil {
		t.Fatal(err)
	}
	if p.For("scale").Approvals != 2 || p.For("restart").Approvals != 1 || p.For("delete").Approvals != 2 {
		t.Fatalf("approvals default: %+v %+v", p.For("scale"), p.For("restart"))
	}
	// One approval (the default) keeps the policy's version as it was.
	a, _ := guard.ParsePolicy([]byte("actions: {scale: {risk: high}}"))
	b, _ := guard.ParsePolicy([]byte("actions: {scale: {risk: high, approvals: 0}}"))
	if va, _ := a.Canonical(); func() string { vb, _ := b.Canonical(); return vb }() != va {
		t.Fatal("an unset approvals must not change the policy version")
	}
}
