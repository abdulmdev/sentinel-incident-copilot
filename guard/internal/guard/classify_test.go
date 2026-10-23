package guard

import (
	"encoding/json"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func deployment(replicas int, image, restarted string, extra map[string]any) map[string]any {
	annotations := map[string]any{}
	if restarted != "" {
		annotations[restartedAt] = restarted
	}
	spec := map[string]any{
		"replicas": replicas,
		"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
		"template": map[string]any{
			"metadata": map[string]any{"labels": map[string]any{"app": "web"}, "annotations": annotations},
			"spec":     map[string]any{"containers": []any{map[string]any{"name": "web", "image": image}}},
		},
	}
	for k, v := range extra {
		spec[k] = v
	}
	return map[string]any{"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": "web", "namespace": "shop", "resourceVersion": "7"}, "spec": spec}
}

func raw(v any) runtime.RawExtension {
	b, _ := json.Marshal(v)
	return runtime.RawExtension{Raw: b}
}

func update(oldObj, newObj any, sub string) *admissionv1.AdmissionRequest {
	return &admissionv1.AdmissionRequest{
		Operation:   admissionv1.Update,
		Resource:    metav1.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
		SubResource: sub, Namespace: "shop", Name: "web",
		UserInfo:  authnv1.UserInfo{Username: "system:serviceaccount:agents:fixer"},
		OldObject: raw(oldObj), Object: raw(newObj),
	}
}

func TestClassifyWorkloadChanges(t *testing.T) {
	base := deployment(2, "shop/web:v2", "", nil)
	cases := []struct {
		name, action, fingerprint string
		newObj                    map[string]any
		change                    map[string]string
	}{
		{"scale", "scale", "replicas=5", deployment(5, "shop/web:v2", "", nil), map[string]string{"replicas": "2 → 5"}},
		{"restart", "restart", "restart", deployment(2, "shop/web:v2", "2026-10-23T10:00:00Z", nil), map[string]string{"restart": "rolling restart"}},
		{"rollback by image", "set-image", "images=web=shop/web:v1", deployment(2, "shop/web:v1", "", nil),
			map[string]string{"images": "web=shop/web:v2 → web=shop/web:v1"}},
		{"strategy", "update", "", deployment(2, "shop/web:v2", "", map[string]any{"minReadySeconds": 30}), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := ClassifyAdmission(update(base, c.newObj, ""))
			if err != nil || p == nil {
				t.Fatalf("p=%v err=%v", p, err)
			}
			if p.Action != c.action {
				t.Fatalf("action = %s, want %s", p.Action, c.action)
			}
			if c.fingerprint != "" && p.Fingerprint != c.fingerprint {
				t.Fatalf("fingerprint = %s, want %s", p.Fingerprint, c.fingerprint)
			}
			for k, v := range c.change {
				if p.Change[k] != v {
					t.Fatalf("change[%s] = %q, want %q", k, p.Change[k], v)
				}
			}
			if p.Target.String() != "shop/deployment/web" {
				t.Fatal(p.Target.String())
			}
		})
	}
}

func TestRetriedRestartMatchesItsApproval(t *testing.T) {
	base := deployment(2, "shop/web:v2", "", nil)
	a, _ := ClassifyAdmission(update(base, deployment(2, "shop/web:v2", "2026-10-23T10:00:00Z", nil), ""))
	b, _ := ClassifyAdmission(update(base, deployment(2, "shop/web:v2", "2026-10-23T10:05:00Z", nil), ""))
	if a.Key() != b.Key() {
		t.Fatal("kubectl stamps a new time on every restart; the binding must not depend on it")
	}
}

func TestClassifyCombinedAndMetadataOnly(t *testing.T) {
	base := deployment(2, "shop/web:v2", "", nil)
	p, _ := ClassifyAdmission(update(base, deployment(4, "shop/web:v3", "", nil), ""))
	if p.Action != "update" || p.Change["replicas"] != "2 → 4" || p.Change["images"] == "" {
		t.Fatalf("combined change: %+v", p)
	}
	labelled := deployment(2, "shop/web:v2", "", nil)
	labelled["metadata"].(map[string]any)["labels"] = map[string]any{"team": "payments"}
	if p, _ := ClassifyAdmission(update(base, labelled, "")); p != nil {
		t.Fatalf("a metadata-only change is not an action: %+v", p)
	}
	if p, _ := ClassifyAdmission(update(base, deployment(9, "x", "", nil), "status")); p != nil {
		t.Fatal("status updates are not agent actions")
	}
}

func TestClassifyScaleSubresourceDeleteAndDryRun(t *testing.T) {
	scale := func(n int) map[string]any {
		return map[string]any{"apiVersion": "autoscaling/v1", "kind": "Scale", "spec": map[string]any{"replicas": n}}
	}
	p, _ := ClassifyAdmission(update(scale(2), scale(0), "scale"))
	if p.Action != "scale" || p.Fingerprint != "replicas=0" || p.Change["replicas"] != "2 → 0" {
		t.Fatalf("%+v", p)
	}
	if p, _ := ClassifyAdmission(update(scale(2), scale(2), "scale")); p != nil {
		t.Fatal("scaling to the same size is not a change")
	}
	dry := true
	del := &admissionv1.AdmissionRequest{Operation: admissionv1.Delete, Name: "shop", DryRun: &dry,
		Resource: metav1.GroupVersionResource{Version: "v1", Resource: "namespaces"}}
	p, _ = ClassifyAdmission(del)
	if p.Action != "delete" || !p.DryRun || p.Target.String() != "namespace/shop" {
		t.Fatalf("%+v %s", p, p.Target)
	}
}

func TestPolicyParsingAndLimits(t *testing.T) {
	if _, err := ParsePolicy([]byte("defaults: {autonomy: yolo}")); err == nil {
		t.Fatal("unknown autonomy accepted")
	}
	if _, err := ParsePolicy([]byte("actions: {restart: {autonomy: auto}}")); err == nil {
		t.Fatal("auto without preconditions accepted")
	}
	if _, err := ParsePolicy([]byte("agentz: {}")); err == nil {
		t.Fatal("unknown field accepted (typos must not silently weaken policy)")
	}
	p, err := ParsePolicy([]byte(`
agents: {groups: [system:serviceaccounts:agents], users: [ci-bot]}
protectedNamespaces: [kube-system]
actions:
  scale: {maxReplicas: 10}
  delete: {autonomy: deny}
  restart: {namespaces: [shop]}
`))
	if err != nil {
		t.Fatal(err)
	}
	if p.PendingTTL.Duration.Minutes() != 60 || p.For("anything").Autonomy != AutonomyApprove {
		t.Fatal("defaults not applied")
	}
	if !p.IsAgent("ci-bot", nil) || !p.IsAgent("x", []string{"system:serviceaccounts:agents"}) || p.IsAgent("alice", []string{"devs"}) {
		t.Fatal("agent identity")
	}
	t1 := Target{Resource: "deployments", Namespace: "shop", Name: "web"}
	if v := p.Violations("scale", t1, map[string]string{"replicas": "2 → 11"}); len(v) != 1 {
		t.Fatalf("max replicas: %v", v)
	}
	if v := p.Violations("delete", t1, nil); len(v) != 1 {
		t.Fatalf("deny: %v", v)
	}
	if v := p.Violations("restart", Target{Resource: "deployments", Namespace: "billing", Name: "x"}, nil); len(v) != 1 {
		t.Fatalf("namespace allowlist: %v", v)
	}
	if v := p.Violations("restart", Target{Resource: "deployments", Namespace: "kube-system", Name: "x"}, nil); len(v) < 1 {
		t.Fatal("protected namespace")
	}
}

func TestScaleToZeroOnTheWire(t *testing.T) {
	// autoscaling/v1 Scale has `replicas int32 json:",omitempty"`: the API server sends 0 as nothing.
	oldScale := map[string]any{"kind": "Scale", "spec": map[string]any{"replicas": 3}}
	newScale := map[string]any{"kind": "Scale", "spec": map[string]any{}}
	p, err := ClassifyAdmission(update(oldScale, newScale, "scale"))
	if err != nil || p == nil {
		t.Fatal(p, err)
	}
	if p.Change["replicas"] != "3 → 0" || p.Fingerprint != "replicas=0" {
		t.Fatalf("scale to zero misread: %+v", p)
	}
}

func TestExactDistinguishesRetriesFromNewAttempts(t *testing.T) {
	base := deployment(2, "shop/web:v2", "", nil)
	stamp := deployment(2, "shop/web:v2", "2026-10-23T10:00:00Z", nil)
	retry := deployment(2, "shop/web:v2", "2026-10-23T10:00:00Z", nil)
	retry["metadata"].(map[string]any)["resourceVersion"] = "8" // re-read after a conflict
	again := deployment(2, "shop/web:v2", "2026-10-23T10:05:00Z", nil)
	a, _ := ClassifyAdmission(update(base, stamp, ""))
	b, _ := ClassifyAdmission(update(base, retry, ""))
	c, _ := ClassifyAdmission(update(stamp, again, ""))
	if a.Exact != b.Exact {
		t.Fatal("an API server retry of the same write must have the same exact digest")
	}
	if a.Exact == c.Exact || a.Key() != c.Key() {
		t.Fatal("a new restart shares the approval binding but is a different exact write")
	}
}

func TestGenerationComesFromTheOldObject(t *testing.T) {
	old := deployment(2, "shop/web:v2", "", nil)
	old["metadata"].(map[string]any)["generation"] = 12
	p, _ := ClassifyAdmission(update(old, deployment(3, "shop/web:v2", "", nil), ""))
	if p.Generation != 12 {
		t.Fatalf("generation = %d", p.Generation)
	}
}
