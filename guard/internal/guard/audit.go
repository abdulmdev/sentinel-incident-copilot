package guard

import (
	"crypto/ecdsa"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Event types in the audit chain.
const (
	EventRequested        = "requested"
	EventBlocked          = "blocked"
	EventApproved         = "approved"
	EventApprovalAdded    = "approval-added" // one person's approval of a request that needs more than one
	EventDenied           = "denied"
	EventCapabilityIssued = "capability-issued"
	EventExecuted         = "executed"
	EventReadmitted       = "readmitted" // the API server retried the very request that was executed
	EventExpired          = "expired"
)

// Event is one link in the guard's global audit chain. Each event commits to the previous one
// (Prev), to the request's identity fields (Digest) and is signed with the guard's key, so editing,
// reordering or deleting any record is detectable by anyone holding the public key.
type Event struct {
	Seq       int64  `json:"seq"`
	Prev      string `json:"prev"`
	Type      string `json:"type"`
	At        string `json:"at"` // RFC 3339, UTC; kept as a string so the hash survives round trips
	Actor     string `json:"actor"`
	Detail    string `json:"detail,omitempty"`
	RequestID string `json:"requestId"`
	Digest    string `json:"digest"`
	Kid       string `json:"kid"`
	Hash      string `json:"hash"`
	Sig       string `json:"sig"`
}

func (e *Event) ComputeHash() string {
	return sha(strings.Join([]string{strconv.FormatInt(e.Seq, 10), e.Prev, e.Type, e.At, e.Actor, e.Detail,
		e.RequestID, e.Digest, e.Kid}, "|"))
}

// AuditReport is the result of verifying the chain.
type AuditReport struct {
	OK       bool     `json:"ok"`
	Events   int      `json:"events"`
	Requests int      `json:"requests"`
	HeadSeq  int64    `json:"headSeq"`
	Voided   int      `json:"voided,omitempty"`
	Policies int      `json:"policies"`
	Problems []string `json:"problems,omitempty"`
	Warnings []string `json:"warnings,omitempty"` // worth knowing, but not evidence of tampering
}

// VerifyAudit checks every event: contiguous sequence up to the head (a gap means a deleted
// record or an interrupted write), hash links, signatures, that each request still matches
// what was signed, and the head's own signature (a checkpoint).
func VerifyAudit(requests []*Request, head Head, keys map[string]*ecdsa.PublicKey) AuditReport {
	rep := AuditReport{Requests: len(requests), HeadSeq: head.Seq}
	bySeq := map[int64]Event{}
	problem := func(f string, a ...any) { rep.Problems = append(rep.Problems, fmt.Sprintf(f, a...)) }

	for _, r := range requests {
		digest := r.Digest()
		for _, e := range r.Audit {
			rep.Events++
			if _, dup := bySeq[e.Seq]; dup {
				problem("event #%d appears twice", e.Seq)
			}
			bySeq[e.Seq] = e
			if e.RequestID != r.ID {
				problem("event #%d belongs to %s but is stored on %s", e.Seq, e.RequestID, r.ID)
			}
			if e.Digest != digest {
				problem("request %s was modified after event #%d (%s) was recorded", r.ID, e.Seq, e.Type)
			}
			if e.ComputeHash() != e.Hash {
				problem("event #%d content does not match its hash", e.Seq)
			}
			if k, ok := keys[e.Kid]; !ok {
				problem("event #%d is signed by unknown key %q", e.Seq, e.Kid)
			} else if !VerifyHashSignature(k, e.Hash, e.Sig) {
				problem("event #%d has an invalid signature", e.Seq)
			}
		}
	}
	seqs := make([]int64, 0, len(bySeq))
	for s := range bySeq {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	voids := map[int64]Void{}
	for _, v := range head.Voids {
		if k, ok := keys[v.Kid]; !ok || !VerifyHashSignature(k, v.SigningHash(), v.Sig) {
			problem("void #%d has an invalid signature", v.Seq)
			continue
		}
		voids[v.Seq] = v
	}
	var missing []string
	prev := ""
	for s := int64(1); s <= head.Seq; s++ {
		e, ok := bySeq[s]
		if !ok {
			if v, isVoid := voids[s]; isVoid {
				rep.Voided++
				prev = v.Hash
				continue
			}
			missing = append(missing, strconv.FormatInt(s, 10))
			prev = ""
			continue
		}
		if _, isVoid := voids[s]; isVoid {
			problem("event #%d is both recorded and voided", s)
		}
		if prev != "" && e.Prev != prev || s == 1 && e.Prev != "" {
			problem("event #%d does not link to event #%d", s, s-1)
		}
		prev = e.Hash
	}
	if len(missing) > 0 {
		problem("missing events #%s (a request was deleted, or a write was interrupted)", strings.Join(missing, ", #"))
	}
	if len(seqs) > 0 && seqs[len(seqs)-1] > head.Seq {
		problem("events beyond the chain head #%d", head.Seq)
	}
	if head.Seq > 0 {
		last, ok := bySeq[head.Seq]
		if v, isVoid := voids[head.Seq]; isVoid && !ok {
			last, ok = Event{Hash: v.Hash}, true
		}
		if ok && last.Hash != head.Hash {
			problem("chain head does not match event #%d", head.Seq)
		}
	}
	switch {
	case head.Sig != "":
		if k, ok := keys[head.Kid]; !ok || !VerifyHashSignature(k, head.SigningHash(), head.Sig) {
			problem("the chain head #%d has an invalid signature", head.Seq)
		}
	case head.Seq > 0:
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("the chain head #%d is not signed: it was last written "+
			"by a guard older than 2.0.0-alpha.3, so removing the newest events would go unnoticed until the next event", head.Seq))
	}
	rep.OK = len(rep.Problems) == 0
	return rep
}
