// Command loadgen measures what Sentinel Guard adds to a Kubernetes API request, end to end
// through the API server (TLS, webhook call, guard work, response):
//
//	baseline  a human scales a deployment (the webhook's matchConditions skip the guard)
//	pending   an agent repeats a change that is waiting for approval (denied after a lookup)
//	approved  an agent's change after a human approved it (admitted once: the consume path)
//	approved, inline  the agent retries in the same instant the human approves, so the guard
//	                  also applies the approval on the request path (worst case)
//
// It prints p50/p95/p99 per scenario as JSON. Used by scripts/guard-e2e.sh.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	appsv1client "k8s.io/client-go/kubernetes/typed/apps/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/store"
)

func config(path string) *rest.Config {
	cfg, err := clientcmd.BuildConfigFromFlags("", path)
	if err != nil {
		fail(err)
	}
	cfg.QPS, cfg.Burst = 500, 1000
	cfg.WarningHandler = rest.NoWarnings{} // every admitted agent change carries a guard warning
	return cfg
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "loadgen:", err)
	os.Exit(1)
}

func scale(ctx context.Context, c appsv1client.AppsV1Interface, ns, name string, n int) (time.Duration, error) {
	patch := []byte(fmt.Sprintf(`{"spec":{"replicas":%d}}`, n))
	start := time.Now()
	_, err := c.Deployments(ns).Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}, "scale")
	return time.Since(start), err
}

type stats struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50_ms"`
	P95 float64 `json:"p95_ms"`
	P99 float64 `json:"p99_ms"`
	Max float64 `json:"max_ms"`
}

func summarize(d []time.Duration) stats {
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	q := func(p float64) float64 {
		i := int(p*float64(len(d))+0.5) - 1
		i = max(0, min(i, len(d)-1))
		return float64(d[i].Microseconds()) / 1000
	}
	return stats{N: len(d), P50: q(.50), P95: q(.95), P99: q(.99), Max: q(1)}
}

func main() {
	admin := flag.String("admin-kubeconfig", os.Getenv("KUBECONFIG"), "kubeconfig of a human (cluster admin)")
	agent := flag.String("agent-kubeconfig", "", "kubeconfig of the agent ServiceAccount")
	ns := flag.String("namespace", "shop", "namespace of the deployment")
	name := flag.String("deployment", "load", "deployment to scale")
	guardNS := flag.String("guard-namespace", "sentinel-system", "")
	n := flag.Int("n", 200, "requests per scenario (approved: n/4 each)")
	flag.Parse()
	ctx := context.Background()
	adminApps := appsv1client.NewForConfigOrDie(config(*admin))
	agentApps := appsv1client.NewForConfigOrDie(config(*agent))
	dyn := dynamic.NewForConfigOrDie(config(*admin))
	ars := dyn.Resource(store.ActionRequests).Namespace(*guardNS)

	if _, err := scale(ctx, adminApps, *ns, *name, 2); err != nil {
		fail(err)
	}
	var baseline, pending, approved, approvedInline []time.Duration
	for i := range *n {
		d, err := scale(ctx, adminApps, *ns, *name, 2+i%2)
		if err != nil {
			fail(fmt.Errorf("baseline: %w", err))
		}
		baseline = append(baseline, d)
	}
	_, _ = scale(ctx, adminApps, *ns, *name, 2)

	// One change, retried: the first call creates the request, the rest find it pending.
	if _, err := scale(ctx, agentApps, *ns, *name, 5); err == nil {
		fail(fmt.Errorf("an unapproved agent change was admitted"))
	}
	for range *n {
		d, err := scale(ctx, agentApps, *ns, *name, 5)
		if err == nil {
			fail(fmt.Errorf("an unapproved agent change was admitted"))
		}
		pending = append(pending, d)
	}

	// Request → human approves → the agent's retry is admitted (measured) → a replay is denied.
	approvalPatch := []byte(`{"spec":{"decision":"approve","decisionNote":"loadgen"}}`)
	for i := range max(*n/2, 2) {
		target := 3 - i%2 // alternate 3 and 2, so each round is a real change
		_, err := scale(ctx, agentApps, *ns, *name, target)
		if err == nil {
			fail(fmt.Errorf("round %d: admitted before approval", i))
		}
		reqs, err := ars.List(ctx, metav1.ListOptions{})
		if err != nil {
			fail(err)
		}
		id := ""
		for _, item := range reqs.Items {
			r, _ := store.FromObject(&item)
			if r.Phase == "Pending" && r.Decision == "" && r.Change["replicas"] != "" &&
				r.Fingerprint == fmt.Sprintf("replicas=%d", target) && r.Target.Name == *name {
				id = r.ID
			}
		}
		if id == "" {
			fail(fmt.Errorf("round %d: no pending request found", i))
		}
		if _, err := ars.Patch(ctx, id, types.MergePatchType, approvalPatch, metav1.PatchOptions{}); err != nil {
			fail(fmt.Errorf("approving %s: %w", id, err))
		}
		// Odd rounds: retry at once, so the guard applies the approval inline (worst case).
		// Even rounds: wait until the approval is applied, as `kubectl sentinel approve` does.
		inline := i%2 == 1
		for !inline {
			obj, err := ars.Get(ctx, id, metav1.GetOptions{})
			if err != nil {
				fail(err)
			}
			if r, _ := store.FromObject(obj); r.Phase == "Approved" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		d, err := scale(ctx, agentApps, *ns, *name, target)
		if err != nil {
			fail(fmt.Errorf("round %d: approved change not admitted: %w", i, err))
		}
		if inline {
			approvedInline = append(approvedInline, d)
		} else {
			approved = append(approved, d)
		}
		if _, err := scale(ctx, agentApps, *ns, *name, target%3+2); err == nil {
			fail(fmt.Errorf("round %d: a different change rode on the approval", i))
		}
	}
	out := map[string]stats{"baseline_human": summarize(baseline), "agent_pending": summarize(pending),
		"agent_approved_once": summarize(approved), "agent_approved_inline_worst_case": summarize(approvedInline)}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(out)
}
