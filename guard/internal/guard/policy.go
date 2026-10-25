package guard

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"sigs.k8s.io/yaml"
)

// Autonomy levels, from least to most independent.
const (
	AutonomyDeny    = "deny"    // never allowed for agents
	AutonomyApprove = "approve" // a human approves each action (the default)
	AutonomyAuto    = "auto"    // allowed when every precondition is observed; otherwise needs approval
	AutonomyObserve = "observe" // allowed and recorded: shadow mode for rolling the guard out
)

// Policy is the guard's whole configuration of what agents may do. It is plain YAML so it can
// live in Git and be reviewed like code.
type Policy struct {
	Agents              Agents                  `json:"agents"`
	Delegates           Agents                  `json:"delegates"`
	Auditors            Agents                  `json:"auditors"`
	Approvers           Agents                  `json:"approvers"`
	Defaults            ActionPolicy            `json:"defaults"`
	Actions             map[string]ActionPolicy `json:"actions"`
	ProtectedNamespaces []string                `json:"protectedNamespaces"`
	PendingTTL          Duration                `json:"pendingTTL"`
	ApprovalTTL         Duration                `json:"approvalTTL"`
	Prometheus          struct {
		URL string `json:"url"`
	} `json:"prometheus"`
}

// Agents identifies which callers are AI agents. Everyone else (humans, CI, controllers) is
// never stopped by the guard. The same shape lists Delegates: services trusted to record a
// decision on behalf of a person they authenticated (Sentinel's gateway, for its console users),
// Auditors: identities that may read the audit trail and export evidence, and Approvers: people
// who may decide over HTTP with their own OIDC identity, verified by the guard.
type Agents struct {
	Users  []string `json:"users"`
	Groups []string `json:"groups"`
}

type ActionPolicy struct {
	Risk          string             `json:"risk,omitempty"`     // low | medium | high
	Autonomy      string             `json:"autonomy,omitempty"` // see Autonomy*
	Preconditions []PreconditionSpec `json:"preconditions,omitempty"`
	MinReplicas   *int               `json:"minReplicas,omitempty"`
	MaxReplicas   *int               `json:"maxReplicas,omitempty"`
	Namespaces    []string           `json:"namespaces,omitempty"` // if set, only these namespaces
	// Approvals is how many distinct people must approve (default 1): two-person rules for the
	// riskiest actions. Neither the agent nor its delegate counts, and nobody counts twice.
	Approvals int `json:"approvals,omitempty"`
}

// PreconditionSpec is a condition the guard checks itself before an action may run on its own:
// a PromQL query that returns a non-empty result when the condition the action fixes is present.
// {{.Namespace}}, {{.Name}} and {{.Resource}} are substituted from the target.
type PreconditionSpec struct {
	Name  string `json:"name"`
	Query string `json:"query"`
}

// Duration reads "10m" style YAML values.
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	s, err := strconv.Unquote(string(b))
	if err != nil {
		return fmt.Errorf("duration must be a string like \"10m\": %s", b)
	}
	d.Duration, err = time.ParseDuration(s)
	return err
}

func (d Duration) MarshalJSON() ([]byte, error) { return []byte(strconv.Quote(d.String())), nil }

func LoadPolicy(path string) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParsePolicy(b)
}

func ParsePolicy(b []byte) (*Policy, error) {
	var p Policy
	if err := yaml.UnmarshalStrict(b, &p); err != nil {
		return nil, fmt.Errorf("policy: %w", err)
	}
	if p.Defaults.Autonomy == "" {
		p.Defaults.Autonomy = AutonomyApprove
	}
	if p.Defaults.Risk == "" {
		p.Defaults.Risk = "medium"
	}
	if p.PendingTTL.Duration == 0 {
		p.PendingTTL.Duration = time.Hour
	}
	if p.ApprovalTTL.Duration == 0 {
		p.ApprovalTTL.Duration = 10 * time.Minute
	}
	check := func(name string, a ActionPolicy) error {
		if a.Autonomy != "" && !slices.Contains([]string{AutonomyDeny, AutonomyApprove, AutonomyAuto, AutonomyObserve}, a.Autonomy) {
			return fmt.Errorf("policy: %s: unknown autonomy %q (deny | approve | auto | observe)", name, a.Autonomy)
		}
		if a.Risk != "" && !slices.Contains([]string{"low", "medium", "high"}, a.Risk) {
			return fmt.Errorf("policy: %s: unknown risk %q (low | medium | high)", name, a.Risk)
		}
		if a.Approvals < 0 || a.Approvals > 5 {
			return fmt.Errorf("policy: %s: approvals must be between 1 and 5", name)
		}
		if a.Autonomy == AutonomyAuto && len(a.Preconditions) == 0 {
			return fmt.Errorf("policy: %s: autonomy auto needs at least one precondition", name)
		}
		for _, pc := range a.Preconditions {
			if pc.Name == "" || pc.Query == "" {
				return fmt.Errorf("policy: %s: every precondition needs a name and a query", name)
			}
		}
		return nil
	}
	if err := check("defaults", p.Defaults); err != nil {
		return nil, err
	}
	for name, a := range p.Actions {
		if err := check(name, a); err != nil {
			return nil, err
		}
	}
	return &p, nil
}

// For returns the effective policy for an action: its own settings over the defaults.
func (p *Policy) For(action string) ActionPolicy {
	a, ok := p.Actions[action]
	if !ok {
		return p.Defaults
	}
	if a.Risk == "" {
		a.Risk = p.Defaults.Risk
	}
	if a.Autonomy == "" {
		a.Autonomy = p.Defaults.Autonomy
	}
	if a.Preconditions == nil {
		a.Preconditions = p.Defaults.Preconditions
	}
	if a.Approvals == 0 {
		a.Approvals = p.Defaults.Approvals
	}
	return a
}

func (p *Policy) IsAgent(user string, groups []string) bool { return p.Agents.matches(user, groups) }

func (p *Policy) IsDelegate(user string, groups []string) bool {
	return p.Delegates.matches(user, groups)
}

// IsApprover reports whether a person authenticated by OIDC may decide over HTTP.
func (p *Policy) IsApprover(user string, groups []string) bool {
	return p.Approvers.matches(user, groups)
}

// IsAuditor reports whether a caller may read the audit trail and export evidence over HTTP.
func (p *Policy) IsAuditor(user string, groups []string) bool {
	return p.Auditors.matches(user, groups)
}

// Canonical is the effective policy as the guard enforces it (after defaults and anything the
// guard adds, such as its own protected namespace), and its version: the SHA-256 of that text.
func (p *Policy) Canonical() (version, text string) {
	b, err := yaml.Marshal(p)
	if err != nil { // a Policy always marshals; keep the version well defined regardless
		b = []byte(err.Error())
	}
	return sha(string(b)), string(b)
}

func (a Agents) matches(user string, groups []string) bool {
	if slices.Contains(a.Users, user) {
		return true
	}
	for _, g := range groups {
		if slices.Contains(a.Groups, g) {
			return true
		}
	}
	return false
}

// Violations are hard limits: an action that breaks one is blocked and cannot be approved.
func (p *Policy) Violations(action string, t Target, change map[string]string) []string {
	a := p.For(action)
	var out []string
	if a.Autonomy == AutonomyDeny {
		out = append(out, fmt.Sprintf("policy denies %s for agents", action))
	}
	ns := t.Namespace
	if t.Resource == "namespaces" {
		ns = t.Name
	}
	if ns != "" && slices.Contains(p.ProtectedNamespaces, ns) {
		out = append(out, fmt.Sprintf("namespace %s is protected", ns))
	}
	if ns != "" && len(a.Namespaces) > 0 && !slices.Contains(a.Namespaces, ns) {
		out = append(out, fmt.Sprintf("%s is only allowed in %s", action, strings.Join(a.Namespaces, ", ")))
	}
	if to, ok := replicasTo(change); ok {
		if a.MinReplicas != nil && to < *a.MinReplicas {
			out = append(out, fmt.Sprintf("replicas %d is below the minimum %d", to, *a.MinReplicas))
		}
		if a.MaxReplicas != nil && to > *a.MaxReplicas {
			out = append(out, fmt.Sprintf("replicas %d is above the maximum %d", to, *a.MaxReplicas))
		}
	}
	return out
}

// replicasTo reads the new replica count from a change like {"replicas": "3 → 5"}.
func replicasTo(change map[string]string) (int, bool) {
	v, ok := change["replicas"]
	if !ok {
		return 0, false
	}
	if i := strings.LastIndex(v, "→"); i >= 0 {
		v = v[i+len("→"):]
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	return n, err == nil
}
