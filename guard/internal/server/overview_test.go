package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/store"
)

func TestOverviewShowsTheWholePicture(t *testing.T) {
	s, _ := newServer(t)
	s.Engine.Policy().Actions["delete-customer-data"] = guard.ActionPolicy{Risk: "high", Autonomy: guard.AutonomyDeny}
	h := s.Handler()

	approved := proposeAPI(t, h, "refund", "order-1")
	post(t, h, "/v1/actions/"+approved+"/approve", map[string]string{"approver": "alice"}, "admin-token")
	denied := proposeAPI(t, h, "refund", "order-2")
	post(t, h, "/v1/actions/"+denied+"/deny", map[string]string{"approver": "bob"}, "admin-token")
	proposeAPI(t, h, "refund", "order-3") // still waiting
	post(t, h, "/v1/actions", ProposeRequest{Action: "delete-customer-data", Target: guard.Target{Name: "c-1"}}, "agent-token")

	for token, want := range map[string]int{"": 401, "agent-token": 401, "admin-token": 200} {
		if r := get(h, "/v1/overview", token); r.Code != want {
			t.Fatalf("overview with %q: %d, want %d", token, r.Code, want)
		}
	}
	var ov Overview
	_ = json.Unmarshal(get(h, "/v1/overview", "admin-token").Body.Bytes(), &ov)
	if ov.Pending != 1 || ov.ApprovedUnspent != 1 || ov.OldestPendingSeconds < 0 || ov.PolicyVersion == "" {
		t.Fatalf("what waits: %+v", ov)
	}
	if o := ov.Summary.Outcomes; o[guard.OutcomeProposed] != 3 || o[guard.OutcomeApproved] != 1 || o[guard.OutcomeDenied] != 1 ||
		o[guard.OutcomeBlocked] != 1 {
		t.Fatalf("outcomes: %v", o)
	}
	if len(ov.Agents) != 1 || ov.Agents[0].Agent != "refund-bot" || ov.Agents[0].Waiting != 1 || ov.Agents[0].Outcomes[guard.OutcomeBlocked] != 1 {
		t.Fatalf("per agent: %+v", ov.Agents)
	}
	if ov.Chain != nil {
		t.Fatal("no self-verification has run yet")
	}
	s.selfVerify(t.Context())
	_ = json.Unmarshal(get(h, "/v1/overview", "admin-token").Body.Bytes(), &ov)
	if ov.Chain == nil || !ov.Chain.OK || ov.Chain.Events == 0 || ov.Chain.At.IsZero() {
		t.Fatalf("the record's health, from the last self-verification: %+v", ov.Chain)
	}
}

// headAheadOnce shows the chain head one step ahead on its first read, as a write in flight does
// (its place in the chain is claimed before the request is written).
type headAheadOnce struct {
	*store.Memory
	reads int
}

func (s *headAheadOnce) Head(ctx context.Context) (guard.Head, error) {
	h, err := s.Memory.Head(ctx)
	if s.reads++; s.reads == 1 {
		h.Seq++
	}
	return h, err
}

func TestSelfVerificationRereadsBeforeRaisingTheAlarm(t *testing.T) {
	s, mem := newServer(t)
	h := s.Handler()
	proposeAPI(t, h, "refund", "order-1")
	s.Engine.Store = &headAheadOnce{Memory: mem}
	s.selfVerify(t.Context())
	if testutil.ToFloat64(s.Metrics.ChainOK) != 1 {
		t.Fatal("a moment's inconsistency from a write in flight must not report the chain broken")
	}
	if v := s.lastVerify.Load(); v == nil || !v.OK {
		t.Fatalf("last verification: %+v", v)
	}
}
