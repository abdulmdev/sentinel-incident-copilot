// Package guard is Sentinel Guard's core: the request lifecycle, policy, change classification,
// capabilities and the tamper-evident audit chain. Transports (admission webhooks, HTTP API) and
// storage (memory, Kubernetes) live in other packages and only call into this one.
package guard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Phase is where an action request is in its lifecycle.
//
//	Pending ──approve──▶ Approved ──execute once──▶ Executed
//	   │                    │
//	   ├──deny──▶ Denied     └──ttl──▶ Expired
//	   └──ttl──▶ Expired
//
// Blocked means policy forbids the action outright: it can never be approved.
type Phase string

const (
	PhasePending  Phase = "Pending"
	PhaseApproved Phase = "Approved"
	PhaseDenied   Phase = "Denied"
	PhaseExecuted Phase = "Executed"
	PhaseExpired  Phase = "Expired"
	PhaseBlocked  Phase = "Blocked"
)

// Open reports whether the request can still lead to an execution.
func (p Phase) Open() bool { return p == PhasePending || p == PhaseApproved }

// Sources of action requests.
const (
	SourceAdmission = "admission" // a Kubernetes API request by an agent identity
	SourceAPI       = "api"       // an agent asking over HTTP before acting elsewhere
)

// Target is what an action changes. Kubernetes targets fill Group/Resource/Namespace/Name;
// API targets are a free-form Name (e.g. "s3://billing-archive").
type Target struct {
	Group     string `json:"group,omitempty"`
	Resource  string `json:"resource,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

func (t Target) String() string {
	if t.Resource == "" {
		return t.Name
	}
	kind := strings.TrimSuffix(t.Resource, "s")
	if t.Namespace == "" {
		return kind + "/" + t.Name
	}
	return t.Namespace + "/" + kind + "/" + t.Name
}

// Check is the guard's own observation of one precondition (never the agent's claim).
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"` // observed | not_observed | unknown
	Value  string `json:"value,omitempty"`
	Error  string `json:"error,omitempty"`
}

const (
	CheckObserved    = "observed"
	CheckNotObserved = "not_observed"
	CheckUnknown     = "unknown"
)

// Request is one proposed action and everything that happened to it.
type Request struct {
	ID          string            `json:"id"`
	Key         string            `json:"key"` // binds approval to exactly this agent, action, target and change
	Source      string            `json:"source"`
	Agent       string            `json:"agent"`
	Action      string            `json:"action"`
	Target      Target            `json:"target"`
	Change      map[string]string `json:"change,omitempty"` // human-readable, e.g. replicas: "3 → 0"
	Fingerprint string            `json:"fingerprint"`
	Intent      string            `json:"intent,omitempty"`
	Risk        string            `json:"risk"`
	Autonomy    string            `json:"autonomy"`
	CreatedAt   time.Time         `json:"createdAt"`

	// The human (or policy) decision.
	Decision     string     `json:"decision,omitempty"` // approve | deny
	DecidedBy    string     `json:"decidedBy,omitempty"`
	DecidedVia   string     `json:"decidedVia,omitempty"` // the delegate that attested DecidedBy, if any
	DecisionNote string     `json:"decisionNote,omitempty"`
	DecidedAt    *time.Time `json:"decidedAt,omitempty"`

	Phase          Phase      `json:"phase"`
	Reasons        []string   `json:"reasons,omitempty"`
	Preconditions  []Check    `json:"preconditions,omitempty"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	ExecutedAt     *time.Time `json:"executedAt,omitempty"`
	Exact          string     `json:"exact,omitempty"`          // the precise object admitted (see Proposal.Exact)
	ExecGeneration int64      `json:"execGeneration,omitempty"` // the target's generation when admitted
	CapabilityJTI  string     `json:"capabilityJti,omitempty"`
	Audit          []Event    `json:"audit,omitempty"`
}

// Digest covers the request's identity fields; every audit event carries it, so editing what
// was requested after the fact breaks verification.
func (r *Request) Digest() string {
	keys := make([]string, 0, len(r.Change))
	for k := range r.Change {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var change strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&change, "%s=%s;", k, r.Change[k])
	}
	return sha(strings.Join([]string{r.ID, r.Key, r.Source, r.Agent, r.Action, r.Target.Group, r.Target.String(),
		r.Fingerprint, r.Intent, change.String(), r.CreatedAt.UTC().Format(time.RFC3339Nano)}, "|"))
}

// RequestKey is the approval binding: an approval for one change never admits a different one.
func RequestKey(agent, action string, t Target, fingerprint string) string {
	return sha(strings.Join([]string{agent, action, t.Group, t.String(), fingerprint}, "|"))[:40]
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// Head is the tip of the global audit chain, plus any voided places (see Void).
type Head struct {
	Seq   int64  `json:"seq"`
	Hash  string `json:"hash"`
	Voids []Void `json:"voids,omitempty"`
}

// Void marks a claimed place in the chain whose event never landed (a replica lost a race, or a
// write failed). It is signed, so nobody without the guard's key can use voids to hide a deletion.
type Void struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"` // the unused event's hash: the next event links to it
	Kid  string `json:"kid"`
	Sig  string `json:"sig"`
}

func (v Void) SigningHash() string { return sha(fmt.Sprintf("void|%d|%s", v.Seq, v.Hash)) }

var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Store persists requests and the audit chain head. Implementations must make Update and
// AdvanceHead atomic (compare-and-swap, retried on conflict), because several guard replicas
// race on the same request: exactly one of them may move it from Approved to Executed.
type Store interface {
	Create(ctx context.Context, r *Request) error
	Get(ctx context.Context, id string) (*Request, error)
	List(ctx context.Context) ([]*Request, error)
	FindByKey(ctx context.Context, key string) ([]*Request, error)
	// Update applies fn to the latest stored version; an error from fn aborts without writing.
	Update(ctx context.Context, id string, fn func(*Request) error) (*Request, error)
	Head(ctx context.Context) (Head, error)
	// AdvanceHead applies fn to the current head and stores the result atomically.
	AdvanceHead(ctx context.Context, fn func(Head) (Head, error)) (Head, error)
}
