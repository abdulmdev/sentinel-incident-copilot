// Command kubectl-sentinel is the kubectl plugin for Sentinel Guard:
//
//	kubectl sentinel requests            pending and approved agent actions (--all for everything)
//	kubectl sentinel describe <id>       one request, its preconditions and audit trail
//	kubectl sentinel approve <id> [-m]   approve exactly that change, once
//	kubectl sentinel deny <id> [-m]      deny it
//	kubectl sentinel audit verify        verify the tamper-evident audit chain yourself
//
// Approvals go through the Kubernetes API with your own credentials: RBAC decides who may approve,
// and the guard records the identity the API server authenticated.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/kube"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/store"
)

var version = "dev"

const usage = `kubectl sentinel: approve AI agent actions guarded by Sentinel Guard

Usage:
  kubectl sentinel requests [--all] [-o json]
  kubectl sentinel describe <id>
  kubectl sentinel approve <id> [-m "note"]
  kubectl sentinel deny <id> [-m "note"]
  kubectl sentinel audit verify
  kubectl sentinel version

Flags:
  --guard-namespace   where Sentinel Guard runs (default sentinel-system, or $SENTINEL_GUARD_NAMESPACE)
  --context, --kubeconfig   as for kubectl
`

type opts struct {
	kubeconfig, context, namespace, note, output string
	all                                          bool
	args                                         []string
}

func parse(argv []string) (opts, error) {
	o := opts{namespace: os.Getenv("SENTINEL_GUARD_NAMESPACE")}
	if o.namespace == "" {
		o.namespace = "sentinel-system"
	}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		name, value, hasValue := strings.Cut(a, "=")
		takes := map[string]*string{"--kubeconfig": &o.kubeconfig, "--context": &o.context, "--guard-namespace": &o.namespace,
			"-m": &o.note, "--note": &o.note, "-o": &o.output, "--output": &o.output}
		if dst, ok := takes[name]; ok {
			if !hasValue {
				if i+1 >= len(argv) {
					return o, fmt.Errorf("%s needs a value", name)
				}
				i++
				value = argv[i]
			}
			*dst = value
			continue
		}
		switch {
		case a == "--all" || a == "-A":
			o.all = true
		case a == "-h" || a == "--help":
			o.args = append([]string{"help"}, o.args...)
		case strings.HasPrefix(a, "-"):
			return o, fmt.Errorf("unknown flag %s", a)
		default:
			o.args = append(o.args, a)
		}
	}
	return o, nil
}

type client struct {
	dyn  dynamic.Interface
	core corev1client.CoreV1Interface
	ns   string
	kube *store.Kube
}

func connect(o opts) (*client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if o.kubeconfig != "" {
		rules.ExplicitPath = o.kubeconfig
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules,
		&clientcmd.ConfigOverrides{CurrentContext: o.context}).ClientConfig()
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	core, err := corev1client.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &client{dyn: dyn, core: core, ns: o.namespace, kube: &store.Kube{Dynamic: dyn, Core: core, Namespace: o.namespace}}, nil
}

func main() {
	o, err := parse(os.Args[1:])
	if err == nil {
		err = run(context.Background(), o)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o opts) error {
	if len(o.args) == 0 || o.args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := o.args[0], o.args[1:]
	if cmd == "version" {
		fmt.Println("kubectl-sentinel", version)
		return nil
	}
	c, err := connect(o)
	if err != nil {
		return err
	}
	switch cmd {
	case "requests", "list", "ls":
		return c.requests(ctx, o)
	case "describe", "get":
		if len(rest) != 1 {
			return errors.New("usage: kubectl sentinel describe <id>")
		}
		return c.describe(ctx, rest[0])
	case "approve", "deny":
		if len(rest) != 1 {
			return fmt.Errorf("usage: kubectl sentinel %s <id> [-m note]", cmd)
		}
		return c.decide(ctx, rest[0], cmd, o.note)
	case "audit":
		if len(rest) != 1 || rest[0] != "verify" {
			return errors.New("usage: kubectl sentinel audit verify")
		}
		return c.verify(ctx)
	}
	return fmt.Errorf("unknown command %q\n\n%s", cmd, usage)
}

func shortAgent(user string) string {
	if rest, ok := strings.CutPrefix(user, "system:serviceaccount:"); ok {
		return strings.Replace(rest, ":", "/", 1)
	}
	return user
}

func age(t time.Time) string {
	d := time.Since(t).Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func changeText(r *guard.Request) string {
	keys := make([]string, 0, len(r.Change))
	for k := range r.Change {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+": "+r.Change[k])
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, "; ")
}

// phase shows a decision that the guard has not applied yet as such.
func phase(r *guard.Request) string {
	if r.Phase == guard.PhasePending && r.Decision != "" {
		return "Pending (" + r.Decision + "d)"
	}
	return string(r.Phase)
}

func (c *client) requests(ctx context.Context, o opts) error {
	reqs, err := c.kube.List(ctx)
	if err != nil {
		return err
	}
	if !o.all {
		open := reqs[:0]
		for _, r := range reqs {
			if r.Phase.Open() {
				open = append(open, r)
			}
		}
		reqs = open
	}
	if o.output == "json" {
		return json.NewEncoder(os.Stdout).Encode(reqs)
	}
	if len(reqs) == 0 {
		if o.all {
			fmt.Println("No action requests.")
		} else {
			fmt.Println("No pending or approved requests. Use --all to see the history.")
		}
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tAGE\tPHASE\tAGENT\tACTION\tTARGET\tCHANGE\tRISK")
	for i := len(reqs) - 1; i >= 0; i-- {
		r := reqs[i]
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, age(r.CreatedAt), phase(r), shortAgent(r.Agent),
			r.Action, r.Target, changeText(r), r.Risk)
	}
	return w.Flush()
}

func (c *client) describe(ctx context.Context, id string) error {
	r, err := c.kube.Get(ctx, id)
	if err != nil {
		return err
	}
	fmt.Printf("Request:   %s\nPhase:     %s\nAgent:     %s\nAction:    %s (risk %s, autonomy %s)\nTarget:    %s\nChange:    %s\n",
		r.ID, phase(r), r.Agent, r.Action, r.Risk, r.Autonomy, r.Target, changeText(r))
	if r.Intent != "" {
		fmt.Printf("Intent:    %s\n", r.Intent)
	}
	fmt.Printf("Requested: %s (%s ago)\n", r.CreatedAt.Local().Format(time.RFC1123), age(r.CreatedAt))
	if r.Phase.Open() {
		fmt.Printf("Expires:   %s\n", r.ExpiresAt.Local().Format(time.RFC1123))
	}
	for _, reason := range r.Reasons {
		fmt.Printf("Reason:    %s\n", reason)
	}
	for _, ch := range r.Preconditions {
		line := ch.Status
		if ch.Value != "" {
			line += " (value " + ch.Value + ")"
		}
		if ch.Error != "" {
			line += " (" + ch.Error + ")"
		}
		fmt.Printf("Precondition %q: %s\n", ch.Name, line)
	}
	if r.Decision != "" {
		fmt.Printf("Decision:  %s by %s", r.Decision, r.DecidedBy)
		if r.DecisionNote != "" {
			fmt.Printf(": %q", r.DecisionNote)
		}
		fmt.Println()
	}
	fmt.Println("\nAudit trail:")
	for _, e := range r.Audit {
		fmt.Printf("  #%-5d %-18s %-28s %s %s\n", e.Seq, e.Type, e.At, shortAgent(e.Actor), e.Detail)
	}
	return nil
}

func (c *client) decide(ctx context.Context, id, verb, note string) error {
	decision := map[string]string{"approve": "approve", "deny": "deny"}[verb]
	patch, _ := json.Marshal(map[string]any{"spec": map[string]any{"decision": decision, "decisionNote": note}})
	_, err := c.dyn.Resource(store.ActionRequests).Namespace(c.ns).Patch(ctx, id, types.MergePatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return err
	}
	// The guard applies the decision within a couple of seconds; wait so the answer is final.
	deadline := time.Now().Add(15 * time.Second)
	for {
		r, err := c.kube.Get(ctx, id)
		if err != nil {
			return err
		}
		switch r.Phase {
		case guard.PhaseApproved:
			fmt.Printf("Approved %s: %s of %s (%s), by %s.\nValid once, until %s. The agent's next identical attempt will be admitted.\n",
				r.ID, r.Action, r.Target, changeText(r), r.DecidedBy, r.ExpiresAt.Local().Format(time.Kitchen))
			return nil
		case guard.PhaseDenied:
			fmt.Printf("Denied %s: %s of %s, by %s.\n", r.ID, r.Action, r.Target, r.DecidedBy)
			return nil
		}
		if time.Now().After(deadline) {
			fmt.Printf("Recorded your decision on %s; the guard has not applied it yet (is it running?).\n", id)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (c *client) verify(ctx context.Context) error {
	reqs, err := c.kube.List(ctx)
	if err != nil {
		return err
	}
	head, err := c.kube.Head(ctx)
	if err != nil {
		return err
	}
	cm, err := c.core.ConfigMaps(c.ns).Get(ctx, kube.JWKSConfigMap, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("reading the guard's public keys: %w", err)
	}
	keys, err := guard.ParseJWKS([]byte(cm.Data["jwks.json"]))
	if err != nil {
		return err
	}
	rep := guard.VerifyAudit(reqs, head, keys)
	if rep.OK {
		voided := ""
		if rep.Voided > 0 {
			voided = fmt.Sprintf(" (%d signed voids: places claimed by a replica that lost a race)", rep.Voided)
		}
		fmt.Printf("Audit chain OK: %d events across %d requests, head #%d%s, every signature valid.\n",
			rep.Events, rep.Requests, rep.HeadSeq, voided)
		return nil
	}
	fmt.Printf("Audit chain FAILED (%d events, %d requests, head #%d):\n", rep.Events, rep.Requests, rep.HeadSeq)
	for _, p := range rep.Problems {
		fmt.Println("  -", p)
	}
	os.Exit(2)
	return nil
}
