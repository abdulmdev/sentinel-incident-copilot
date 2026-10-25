package guard

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// EvidenceFormat names the evidence bundle's layout; verifiers refuse other formats.
const EvidenceFormat = "sentinel-guard-evidence/v1"

// Evidence is the complete record of what agents asked to do and what people and policy decided,
// in one file an auditor can verify offline. It needs no signature of its own: every request's
// events, every void, every policy version and the chain head are each signed by the guard, and
// the summary is recomputed by the verifier rather than trusted.
type Evidence struct {
	Format      string           `json:"format"`
	GeneratedAt string           `json:"generatedAt"`
	GeneratedBy string           `json:"generatedBy,omitempty"`
	JWKS        map[string]any   `json:"jwks"`
	Head        Head             `json:"head"`
	Policies    []PolicyRecord   `json:"policies"`
	Requests    []*Request       `json:"requests"`
	Summary     OversightSummary `json:"summary"`
}

func BuildEvidence(reqs []*Request, head Head, pols []PolicyRecord, jwks map[string]any, generatedBy string, now time.Time) *Evidence {
	reqs = slices.Clone(reqs)
	sort.SliceStable(reqs, func(i, j int) bool { return firstSeq(reqs[i]) < firstSeq(reqs[j]) })
	pols = slices.Clone(pols)
	sort.Slice(pols, func(i, j int) bool { return pols[i].LoadedAt < pols[j].LoadedAt })
	return &Evidence{Format: EvidenceFormat, GeneratedAt: now.UTC().Format(time.RFC3339), GeneratedBy: generatedBy,
		JWKS: jwks, Head: head, Policies: pols, Requests: reqs, Summary: Summarize(reqs)}
}

func firstSeq(r *Request) int64 {
	if len(r.Audit) == 0 {
		return 0
	}
	return r.Audit[0].Seq
}

// EvidenceReport is the verifier's answer: the audit report, plus what the evidence shows.
type EvidenceReport struct {
	AuditReport
	Keys    []string         `json:"keys"` // key IDs the records were checked against
	Summary OversightSummary `json:"summary"`
}

// VerifyEvidence checks a bundle with the given public keys, or with the keys the bundle carries
// when trusted is nil (then compare the reported key IDs with ones you recorded independently).
func VerifyEvidence(ev *Evidence, trusted map[string]*ecdsa.PublicKey) EvidenceReport {
	keys := trusted
	var extra []string
	if keys == nil {
		b, _ := json.Marshal(ev.JWKS)
		parsed, err := ParseJWKS(b)
		if err != nil {
			extra = append(extra, "the bundle's public keys can't be read: "+err.Error())
		}
		keys = parsed
	}
	rep := EvidenceReport{AuditReport: VerifyAudit(ev.Requests, ev.Head, keys), Summary: Summarize(ev.Requests)}
	problem := func(f string, a ...any) { rep.Problems = append(rep.Problems, fmt.Sprintf(f, a...)) }
	if ev.Format != EvidenceFormat {
		problem("unknown evidence format %q (expected %s)", ev.Format, EvidenceFormat)
	}
	rep.Problems = append(rep.Problems, extra...)
	for kid := range keys {
		rep.Keys = append(rep.Keys, kid)
	}
	sort.Strings(rep.Keys)

	versions := map[string]bool{}
	for _, p := range ev.Policies {
		rep.Policies++
		short := p.Version
		if len(short) > 12 {
			short = short[:12]
		}
		if sha(p.Text) != p.Version {
			problem("policy %s: its text does not match its version", short)
		}
		if k, ok := keys[p.Kid]; !ok || !VerifyHashSignature(k, p.SigningHash(), p.Sig) {
			problem("policy %s has an invalid signature", short)
		}
		versions[p.Version] = true
	}
	unversioned := 0
	for _, r := range ev.Requests {
		for _, msg := range consistency(r) {
			problem("request %s: %s", r.ID, msg)
		}
		switch {
		case r.PolicyVersion == "":
			unversioned++
		case !versions[r.PolicyVersion]:
			problem("request %s was evaluated under policy %.12s, which is not in the evidence", r.ID, r.PolicyVersion)
		}
	}
	if unversioned > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d request(s) predate policy versioning (guards older than 2.0.0-alpha.3)", unversioned))
	}
	got, _ := json.Marshal(ev.Summary)
	want, _ := json.Marshal(rep.Summary)
	if string(got) != string(want) {
		problem("the bundle's summary does not match its records (the verifier's own summary is reported)")
	}
	rep.OK = len(rep.Problems) == 0
	return rep
}

// Continues checks that a newer bundle extends an older, archived one: the chain only grows, and
// every event and void the older bundle recorded is still there, unchanged. Archived bundles are the
// anchor against the one thing a signed head can't show by itself: the chain rewound to an older
// signed head with the newest events removed.
func Continues(older, newer *Evidence) []string {
	var out []string
	if newer.Head.Seq < older.Head.Seq {
		out = append(out, fmt.Sprintf("the chain went back: head #%d, but the earlier bundle (%s) had reached #%d",
			newer.Head.Seq, older.GeneratedAt, older.Head.Seq))
	}
	have := map[int64]string{}
	for _, r := range newer.Requests {
		for _, e := range r.Audit {
			have[e.Seq] = e.Hash
		}
	}
	for _, v := range newer.Head.Voids {
		have[v.Seq] = v.Hash
	}
	var missing, changed []string
	for _, r := range older.Requests {
		for _, e := range r.Audit {
			switch h, ok := have[e.Seq]; {
			case !ok:
				missing = append(missing, fmt.Sprintf("#%d (%s %s)", e.Seq, e.Type, r.ID))
			case h != e.Hash:
				changed = append(changed, fmt.Sprintf("#%d", e.Seq))
			}
		}
	}
	if len(missing) > 0 {
		out = append(out, "events in the earlier bundle are gone: "+strings.Join(missing, ", "))
	}
	if len(changed) > 0 {
		out = append(out, "events differ from the earlier bundle: "+strings.Join(changed, ", "))
	}
	return out
}

// phaseAfter is the phase each event type leaves a request in.
var phaseAfter = map[string]Phase{
	EventRequested: PhasePending, EventApprovalAdded: PhasePending, EventBlocked: PhaseBlocked, EventApproved: PhaseApproved, EventDenied: PhaseDenied,
	EventCapabilityIssued: PhaseApproved, EventExecuted: PhaseExecuted, EventReadmitted: PhaseExecuted, EventExpired: PhaseExpired,
}

// consistency checks a request's unsigned state fields (phase, decision, decider) against its
// signed events, which are the record: editing who approved something is caught here.
func consistency(r *Request) []string {
	var out []string
	if len(r.Audit) == 0 {
		return []string{"has no audit events"}
	}
	if want, ok := phaseAfter[r.Audit[len(r.Audit)-1].Type]; ok && r.Phase != want {
		out = append(out, fmt.Sprintf("says %s, but its signed events end in %s", r.Phase, want))
	}
	var approvers []string // people whose approval is signed, in order
	for _, e := range r.Audit {
		switch e.Type {
		case EventApprovalAdded:
			i := len(approvers)
			if i >= len(r.Approvals) || r.Approvals[i].By != e.Actor {
				out = append(out, fmt.Sprintf("does not list %s's signed approval", e.Actor))
			}
			approvers = append(approvers, e.Actor)
		case EventApproved, EventDenied:
			decision := map[string]string{EventApproved: "approve", EventDenied: "deny"}[e.Type]
			if r.DecidedBy != e.Actor || r.Decision != decision {
				out = append(out, fmt.Sprintf("says %q by %s, but its signed record says %q by %s", r.Decision, r.DecidedBy, decision, e.Actor))
			}
			if e.Type == EventApproved && !strings.HasPrefix(e.Actor, "policy:") {
				distinct := map[string]bool{e.Actor: true}
				for _, a := range approvers {
					distinct[a] = true
				}
				if len(distinct) < r.Needed() || len(distinct) != len(approvers)+1 {
					out = append(out, fmt.Sprintf("needed %d different people to approve, but its signed record shows %d (%s)",
						r.Needed(), len(distinct), strings.Join(append(approvers, e.Actor), ", ")))
				}
			}
		}
	}
	// Approvals written through the Kubernetes API are signed by the guard within a second; until
	// then a Pending request may list more than its events show, never anything else.
	if len(r.Approvals) > len(approvers) && r.Phase != PhasePending {
		out = append(out, fmt.Sprintf("lists %d approval(s), but only %d are signed", len(r.Approvals), len(approvers)))
	}
	return out
}

// Outcomes of an agent's proposals, as counted in the summary and in the
// sentinel_guard_agent_outcomes_total metric.
const (
	OutcomeProposed     = "proposed"      // waiting for a human
	OutcomeBlocked      = "blocked"       // policy forbids it: can't be approved
	OutcomeAutoApproved = "auto_approved" // policy let it run without a human
	OutcomeApproved     = "approved"      // a human approved it
	OutcomeDenied       = "denied"        // a human denied it (an override of the agent)
	OutcomeExecuted     = "executed"
	OutcomeExpired      = "expired"
)

// EventOutcome maps an audit event to an outcome ("" for events that aren't one).
func EventOutcome(e Event) string {
	switch e.Type {
	case EventRequested:
		return OutcomeProposed
	case EventBlocked:
		return OutcomeBlocked
	case EventApproved:
		if strings.HasPrefix(e.Actor, "policy:") {
			return OutcomeAutoApproved
		}
		return OutcomeApproved
	case EventDenied:
		return OutcomeDenied
	case EventExecuted:
		return OutcomeExecuted
	case EventExpired:
		return OutcomeExpired
	}
	return ""
}

// OversightSummary is what the record shows about human oversight. It is derived from the signed
// events alone, so anyone can recompute it.
type OversightSummary struct {
	Requests int    `json:"requests"`
	From     string `json:"from,omitempty"` // first event
	To       string `json:"to,omitempty"`   // last event
	// Outcomes counts events by outcome, overall and per agent.
	Outcomes map[string]int            `json:"outcomes"`
	ByAgent  map[string]map[string]int `json:"byAgent"`
	// Deciders: who made the human decisions (a person, or a person via a delegate).
	Deciders map[string]int `json:"deciders"`
	// ExpiredUndecided: requests nobody decided before they expired (oversight not exercised).
	ExpiredUndecided int `json:"expiredUndecided"`
	// MultiPersonApproved: requests that took more than one person's approval (policy approvals).
	MultiPersonApproved int `json:"multiPersonApproved,omitempty"`
	// Time from request to a human decision.
	DecisionSecondsMedian float64 `json:"decisionSecondsMedian"`
	DecisionSecondsMax    float64 `json:"decisionSecondsMax"`
}

func Summarize(reqs []*Request) OversightSummary {
	s := OversightSummary{Requests: len(reqs), Outcomes: map[string]int{}, ByAgent: map[string]map[string]int{}, Deciders: map[string]int{}}
	var waits []float64
	for _, r := range reqs {
		var requested time.Time
		decided := false
		partial := 0
		for _, e := range r.Audit {
			if s.From == "" || e.At < s.From {
				s.From = e.At
			}
			if e.At > s.To {
				s.To = e.At
			}
			if e.Type == EventApprovalAdded {
				who := e.Actor
				if partial < len(r.Approvals) && r.Approvals[partial].Via != "" {
					who += " via " + r.Approvals[partial].Via
				}
				s.Deciders[who]++
				partial++
				continue
			}
			if e.Type == EventApproved && partial > 0 {
				s.MultiPersonApproved++
			}
			o := EventOutcome(e)
			if o == "" {
				continue
			}
			s.Outcomes[o]++
			if s.ByAgent[r.Agent] == nil {
				s.ByAgent[r.Agent] = map[string]int{}
			}
			s.ByAgent[r.Agent][o]++
			at, _ := time.Parse(time.RFC3339Nano, e.At)
			switch o {
			case OutcomeProposed:
				requested = at
			case OutcomeApproved, OutcomeDenied:
				decided = true
				who := e.Actor
				if r.DecidedVia != "" {
					who += " via " + r.DecidedVia
				}
				s.Deciders[who]++
				if !requested.IsZero() {
					waits = append(waits, at.Sub(requested).Seconds())
				}
			case OutcomeExpired:
				if !decided {
					s.ExpiredUndecided++
				}
			}
		}
	}
	if n := len(waits); n > 0 {
		sort.Float64s(waits)
		mid := waits[n/2]
		if n%2 == 0 {
			mid = (waits[n/2-1] + waits[n/2]) / 2
		}
		s.DecisionSecondsMedian = round3(mid)
		s.DecisionSecondsMax = round3(waits[n-1])
	}
	return s
}

func round3(f float64) float64 { return float64(int64(f*1000+0.5)) / 1000 }
