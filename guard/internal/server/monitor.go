package server

import (
	"context"
	"time"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
)

// Monitor keeps the guardian gauges current: open requests and how long humans have left the
// oldest one waiting (every tick), and the audit chain verified end to end (every verifyEvery).
// Every replica runs it from its own watch cache, so no replica's gauges go stale. Prometheus
// alert rules on these series ship with the chart (deploy/helm/sentinel-guard/alerts).
func (s *Server) Monitor(ctx context.Context, tick, verifyEvery time.Duration) {
	if s.Metrics == nil {
		return
	}
	s.Metrics.PolicyInfo.Reset()
	s.Metrics.PolicyInfo.WithLabelValues(s.Engine.PolicyVersion()[:12]).Set(1)
	t := time.NewTicker(tick)
	defer t.Stop()
	var lastVerify time.Time
	for {
		s.observe(ctx)
		if time.Since(lastVerify) >= verifyEvery {
			s.selfVerify(ctx)
			lastVerify = time.Now()
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) observe(ctx context.Context) {
	reqs, err := s.Engine.Store.List(ctx)
	if err != nil {
		return
	}
	now := s.Engine.Now()
	open := map[guard.Phase]float64{guard.PhasePending: 0, guard.PhaseApproved: 0}
	var oldest time.Duration
	for _, r := range reqs {
		if !r.Phase.Open() || !now.Before(r.ExpiresAt) {
			continue
		}
		open[r.Phase]++
		if r.Phase == guard.PhasePending && r.Decision == "" && now.Sub(r.CreatedAt) > oldest {
			oldest = now.Sub(r.CreatedAt)
		}
	}
	for phase, n := range open {
		s.Metrics.Open.WithLabelValues(string(phase)).Set(n)
	}
	s.Metrics.OldestPending.Set(oldest.Seconds())
	if head, err := s.Engine.Store.Head(ctx); err == nil {
		s.Metrics.HeadSeq.Set(float64(head.Seq))
	}
}

func (s *Server) selfVerify(ctx context.Context) {
	rep, err := s.Engine.Verify(ctx)
	if err != nil {
		s.Log.Warn("audit self-verification could not run", "error", err)
		return
	}
	if rep.OK {
		s.Metrics.ChainOK.Set(1)
	} else {
		s.Metrics.ChainOK.Set(0)
		s.Log.Error("audit self-verification FAILED", "problems", rep.Problems, "events", rep.Events, "head", rep.HeadSeq)
	}
	s.Metrics.Verified.SetToCurrentTime()
}
