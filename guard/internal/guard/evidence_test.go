package guard_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/store"
)

// history drives one of every outcome: approved and executed, denied, blocked by policy,
// and left to expire with nobody deciding.
func history(t *testing.T) (*guard.Engine, *store.Memory, *clock) {
	t.Helper()
	e, mem, c := defaultEngine(t)
	ctx := context.Background()
	a := mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	c.advance(90 * time.Second)
	if _, err := e.Decide(ctx, a.Request.ID, "alice", nil, "", true, "load test"); err != nil {
		t.Fatal(err)
	}
	mustEval(t, e, scaleTo("3"), guard.VerdictAllow)
	d := mustEval(t, e, scaleTo("4"), guard.VerdictPending)
	c.advance(30 * time.Second)
	if _, err := e.Decide(ctx, d.Request.ID, "bob", nil, "sentinel-gateway", false, "not now"); err != nil {
		t.Fatal(err)
	}
	mustEval(t, e, scaleTo("0"), guard.VerdictBlock)
	mustEval(t, e, scaleTo("5"), guard.VerdictPending)
	c.advance(2 * time.Hour)
	if err := e.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	return e, mem, c
}

// roundTrip is what an auditor holds: the bundle as a file.
func roundTrip(t *testing.T, ev *guard.Evidence) *guard.Evidence {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var out guard.Evidence
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func TestEvidenceVerifiesOfflineAndShowsOversight(t *testing.T) {
	e, _, _ := history(t)
	ev, err := e.Evidence(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	rep := guard.VerifyEvidence(roundTrip(t, ev), nil) // keys from the bundle itself
	if !rep.OK || len(rep.Warnings) != 0 {
		t.Fatalf("%+v", rep)
	}
	if rep.Policies != 1 || ev.Head.Sig == "" || rep.Keys[0] != e.Signer.KeyID() {
		t.Fatalf("policies %d, head sig %q, keys %v", rep.Policies, ev.Head.Sig, rep.Keys)
	}
	for _, r := range ev.Requests {
		if r.PolicyVersion != ev.Policies[0].Version {
			t.Fatalf("%s names policy %q", r.ID, r.PolicyVersion)
		}
	}
	s := rep.Summary
	want := map[string]int{"proposed": 3, "approved": 1, "executed": 1, "denied": 1, "blocked": 1, "expired": 1}
	for o, n := range want {
		if s.Outcomes[o] != n {
			t.Fatalf("outcome %s = %d, want %d (%v)", o, s.Outcomes[o], n, s.Outcomes)
		}
	}
	if s.Requests != 4 || s.ExpiredUndecided != 1 || s.Deciders["alice"] != 1 || s.Deciders["bob via sentinel-gateway"] != 1 {
		t.Fatalf("%+v", s)
	}
	if s.DecisionSecondsMedian != 60 || s.DecisionSecondsMax != 90 {
		t.Fatalf("decision times: median %v, max %v", s.DecisionSecondsMedian, s.DecisionSecondsMax)
	}
	if s.ByAgent[agent]["denied"] != 1 {
		t.Fatalf("per agent: %v", s.ByAgent)
	}
}

func TestEvidenceTamperingIsDetected(t *testing.T) {
	e, _, _ := history(t)
	clean, err := e.Evidence(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, want string
		tamper     func(ev *guard.Evidence)
	}{
		{"who approved", "signed record says", func(ev *guard.Evidence) { ev.Requests[0].DecidedBy = "mallory" }},
		{"what was asked", "was modified after event", func(ev *guard.Evidence) { ev.Requests[0].Change["replicas"] = "2 → 9" }},
		{"a record removed", "missing events", func(ev *guard.Evidence) { ev.Requests = ev.Requests[1:] }},
		{"the policy text", "does not match its version", func(ev *guard.Evidence) { ev.Policies[0].Text += "\n# looser" }},
		{"the policy a request names", "was modified after event", func(ev *guard.Evidence) { ev.Requests[1].PolicyVersion = strings.Repeat("0", 64) }},
		{"the phase", "signed events end in", func(ev *guard.Evidence) { ev.Requests[1].Phase = guard.PhaseApproved }},
		{"the summary", "summary does not match", func(ev *guard.Evidence) { ev.Summary.Outcomes["denied"] = 0 }},
		{"newest events cut, head rewound", "invalid signature", func(ev *guard.Evidence) {
			last := ev.Requests[len(ev.Requests)-1]
			ev.Requests = ev.Requests[:len(ev.Requests)-1]
			prev := last.Audit[0].Seq - 1
			for _, r := range ev.Requests {
				for _, x := range r.Audit {
					if x.Seq == prev {
						ev.Head.Seq, ev.Head.Hash = x.Seq, x.Hash
					}
				}
			}
			ev.Summary = guard.Summarize(ev.Requests)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := roundTrip(t, clean)
			tc.tamper(ev)
			rep := guard.VerifyEvidence(ev, nil)
			if rep.OK || !strings.Contains(strings.Join(rep.Problems, "\n"), tc.want) {
				t.Fatalf("want a problem containing %q, got %v", tc.want, rep.Problems)
			}
		})
	}
}

// A whole history forged and signed with another key verifies against the keys it carries, so
// the verifier reports which key it used; against the guard's real keys it fails.
func TestAForgedHistoryFailsAgainstTheRealKey(t *testing.T) {
	real, _, _ := defaultEngine(t)
	forger, _, _ := history(t)
	forged, _ := forger.Evidence(context.Background(), "test")
	if rep := guard.VerifyEvidence(forged, nil); !rep.OK || rep.Keys[0] == real.Signer.KeyID() {
		t.Fatalf("expected a self-consistent forgery reporting the forger's key: %+v", rep)
	}
	rep := guard.VerifyEvidence(forged, real.Signer.PublicKeys())
	if rep.OK || !strings.Contains(strings.Join(rep.Problems, "\n"), "signed by unknown key") {
		t.Fatalf("%v", rep.Problems)
	}
}

func TestANewerBundleMustExtendTheArchivedOne(t *testing.T) {
	e, _, _ := history(t)
	ctx := context.Background()
	archived, _ := e.Evidence(ctx, "archive")
	archived = roundTrip(t, archived)
	mustEval(t, e, scaleTo("6"), guard.VerdictPending)
	later, _ := e.Evidence(ctx, "later")
	if p := guard.Continues(archived, later); len(p) != 0 {
		t.Fatalf("an honest later export must extend the archive: %v", p)
	}
	// Someone restores the guard's old signed head and deletes what came after it (here: the
	// archive minus its newest request). Every remaining record and the head are genuinely signed...
	rewound := roundTrip(t, archived)
	rewound.Requests = rewound.Requests[:len(rewound.Requests)-1]
	// ...but not against the archive it must extend.
	if p := guard.Continues(archived, rewound); len(p) == 0 || !strings.Contains(strings.Join(p, "\n"), "are gone") {
		t.Fatalf("%v", p)
	}
	tail := roundTrip(t, archived)
	tail.Head.Seq--
	if p := guard.Continues(archived, tail); len(p) == 0 || !strings.Contains(p[0], "went back") {
		t.Fatalf("%v", p)
	}
}

func TestHeadIsSignedAndOldHeadsOnlyWarn(t *testing.T) {
	e, mem, _ := defaultEngine(t)
	ctx := context.Background()
	mustEval(t, e, scaleTo("3"), guard.VerdictPending)
	if rep, _ := e.Verify(ctx); !rep.OK || len(rep.Warnings) != 0 {
		t.Fatalf("%+v", rep)
	}
	// A head written by a guard before 2.0.0-alpha.3 has no signature: still valid, with a note.
	_, _ = mem.AdvanceHead(ctx, func(h guard.Head) (guard.Head, error) { h.At, h.Kid, h.Sig = "", "", ""; return h, nil })
	if rep, _ := e.Verify(ctx); !rep.OK || len(rep.Warnings) != 1 {
		t.Fatalf("%+v", rep)
	}
	// A forged signature is a problem.
	_, _ = mem.AdvanceHead(ctx, func(h guard.Head) (guard.Head, error) {
		h.At, h.Kid, h.Sig = "2026-01-01T00:00:00Z", e.Signer.KeyID(), "AAAA"
		return h, nil
	})
	if rep, _ := e.Verify(ctx); rep.OK {
		t.Fatalf("a forged head signature verified: %+v", rep)
	}
}

func TestPolicyVersionIsTheEffectivePolicy(t *testing.T) {
	e, _, _ := defaultEngine(t)
	v1 := e.PolicyVersion()
	p := *e.Policy()
	p.ProtectedNamespaces = append(append([]string(nil), p.ProtectedNamespaces...), "sentinel-system")
	e.SetPolicy(&p)
	if e.PolicyVersion() == v1 {
		t.Fatal("a change the guard makes to the policy must change its version")
	}
	mustEval(t, e, scaleTo("3"), guard.VerdictPending) // records the new version first
	pols, _ := e.Store.Policies(context.Background())
	if len(pols) != 1 || pols[0].Version != e.PolicyVersion() || !strings.Contains(pols[0].Text, "sentinel-system") {
		t.Fatalf("%+v", pols)
	}
}

// BenchmarkVerifyEvidence: the cost of self-verification (every replica, every 5 minutes by
// default) and of an auditor's check, for a chain of 1,000 approved-and-executed requests.
func BenchmarkVerifyEvidence(b *testing.B) {
	t := &testing.T{}
	e, _, _ := defaultEngine(t)
	ctx := context.Background()
	for i := range 1000 {
		p := scaleTo("3")
		p.Target.Name = "svc-" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + time.Duration(i).String()
		out, _ := e.Evaluate(ctx, p)
		_, _ = e.Decide(ctx, out.Request.ID, "alice", nil, "", true, "")
		_, _ = e.Evaluate(ctx, p)
	}
	ev, _ := e.Evidence(ctx, "bench")
	if rep := guard.VerifyEvidence(ev, e.Signer.PublicKeys()); !rep.OK || rep.Events != 3000 {
		b.Fatalf("%+v", rep.AuditReport)
	}
	raw, _ := json.Marshal(ev)
	for b.Loop() {
		guard.VerifyEvidence(ev, e.Signer.PublicKeys())
	}
	b.ReportMetric(float64(len(raw))/1000, "bytes/request")
}

func BenchmarkSignHash(b *testing.B) {
	t := &testing.T{}
	e, _, _ := defaultEngine(t)
	h := strings.Repeat("ab", 32)
	for b.Loop() {
		_, _ = e.Signer.SignHash(h)
	}
}
