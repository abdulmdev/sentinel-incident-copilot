package guard

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	admissionv1 "k8s.io/api/admission/v1"
)

// Proposal is an action an agent is about to take, before policy has looked at it.
type Proposal struct {
	Source      string
	Agent       string
	Action      string
	Target      Target
	Change      map[string]string
	Fingerprint string
	Intent      string
	DryRun      bool
	// Exact identifies the precise resulting object (unlike Fingerprint, it includes values such as
	// kubectl's restart timestamp). The API server re-runs admission when it retries a conflicting
	// write; an identical result is the same request, and re-applying it can't change anything twice.
	Exact string
	// Generation is the target's metadata.generation before the change (0 if unknown). Kubernetes
	// bumps it on every spec change, so a retry sees the same generation and a later attempt
	// (after the change landed, even if it was then reverted) does not.
	Generation       int64
	LookupGeneration func(context.Context) (int64, error)
}

func (p Proposal) Key() string { return RequestKey(p.Agent, p.Action, p.Target, p.Fingerprint) }

const restartedAt = "kubectl.kubernetes.io/restartedAt"

// ClassifyAdmission turns a Kubernetes API request into a precise action. The fingerprint pins the
// exact change, so approving "scale web to 3" never admits "scale web to 0". It returns nil when
// the request changes nothing that matters (metadata or status only).
func ClassifyAdmission(req *admissionv1.AdmissionRequest) (*Proposal, error) {
	p := &Proposal{
		Source: SourceAdmission,
		Agent:  req.UserInfo.Username,
		Target: Target{Group: req.Resource.Group, Resource: req.Resource.Resource, Namespace: req.Namespace, Name: req.Name},
		DryRun: req.DryRun != nil && *req.DryRun,
	}
	switch req.Operation {
	case admissionv1.Delete:
		p.Action, p.Fingerprint, p.Exact = "delete", "delete", "delete"
		if old, err := decode(req.OldObject.Raw); err == nil {
			if uid, ok := nested(old, "metadata", "uid").(string); ok {
				p.Exact = "delete:" + uid
			}
			p.Generation = generation(old)
		}
		return p, nil
	case admissionv1.Create:
		obj, err := decode(req.Object.Raw)
		if err != nil {
			return nil, err
		}
		if p.Target.Name == "" {
			p.Target.Name, _ = nested(obj, "metadata", "name").(string)
		}
		p.Action, p.Fingerprint = "create", "create:"+hashOf(withoutMeta(obj))
		p.Exact = p.Fingerprint
		return p, nil
	case admissionv1.Update:
	default:
		return nil, nil // CONNECT and anything else is not a change
	}
	oldObj, err := decode(req.OldObject.Raw)
	if err != nil {
		return nil, err
	}
	newObj, err := decode(req.Object.Raw)
	if err != nil {
		return nil, err
	}
	p.Exact = "exact:" + hashOf(withoutMeta(newObj))
	p.Generation = generation(oldObj)
	if req.SubResource == "scale" {
		// Scale's replicas is a plain int with omitempty: on the wire, 0 is an absent field.
		from, to := num(nested(oldObj, "spec", "replicas"), 0), num(nested(newObj, "spec", "replicas"), 0)
		if from == to {
			return nil, nil
		}
		p.Action = "scale"
		p.Change = map[string]string{"replicas": fmt.Sprintf("%v → %v", from, to)}
		p.Fingerprint = fmt.Sprintf("replicas=%v", to)
		return p, nil
	}
	if req.SubResource != "" {
		return nil, nil // status and other subresources are not agent actions
	}
	switch req.Resource.Resource {
	case "deployments", "statefulsets", "daemonsets", "replicasets":
		return classifyWorkload(p, oldObj, newObj), nil
	}
	// Anything else the webhook was configured to watch: any non-metadata change is an update.
	if reflect.DeepEqual(withoutMeta(oldObj), withoutMeta(newObj)) {
		return nil, nil
	}
	p.Action, p.Fingerprint = "update", "update:"+hashOf(withoutMeta(newObj))
	return p, nil
}

type part struct{ action, fingerprint, key, change string }

func classifyWorkload(p *Proposal, oldObj, newObj map[string]any) *Proposal {
	oldSpec, _ := nested(oldObj, "spec").(map[string]any)
	newSpec, _ := nested(newObj, "spec").(map[string]any)
	var parts []part

	// Workload replicas is a pointer: absent means unset, which the API server defaults to 1.
	if from, to := num(oldSpec["replicas"], 1), num(newSpec["replicas"], 1); from != to {
		parts = append(parts, part{"scale", fmt.Sprintf("replicas=%v", to), "replicas", fmt.Sprintf("%v → %v", from, to)})
	}
	oldT, _ := oldSpec["template"].(map[string]any)
	newT, _ := newSpec["template"].(map[string]any)
	oldImages, newImages := images(oldT), images(newT)
	if !reflect.DeepEqual(oldImages, newImages) {
		parts = append(parts, part{"set-image", "images=" + joinMap(newImages), "images", joinMap(oldImages) + " → " + joinMap(newImages)})
	}
	if !reflect.DeepEqual(normalizeTemplate(oldT), normalizeTemplate(newT)) {
		parts = append(parts, part{"rollout", "template=" + hashOf(normalizeTemplate(newT)), "template", "pod template changed"})
	} else if nested(oldT, "metadata", "annotations", restartedAt) != nested(newT, "metadata", "annotations", restartedAt) {
		// Only the restart stamp moved. Its value is a timestamp kubectl sets on every attempt,
		// so it is deliberately not part of the fingerprint: a retried restart matches its approval.
		parts = append(parts, part{"restart", "restart", "restart", "rolling restart"})
	}
	if !reflect.DeepEqual(otherSpec(oldSpec), otherSpec(newSpec)) {
		parts = append(parts, part{"update", "spec=" + hashOf(otherSpec(newSpec)), "spec", "workload spec changed"})
	}

	switch len(parts) {
	case 0:
		return nil
	case 1:
		p.Action, p.Fingerprint = parts[0].action, parts[0].fingerprint
		p.Change = map[string]string{parts[0].key: parts[0].change}
	default: // several kinds of change at once: one request covering all of them
		p.Action, p.Change = "update", map[string]string{}
		var fps []string
		for _, x := range parts {
			fps = append(fps, x.fingerprint)
			p.Change[x.key] = x.change
		}
		p.Fingerprint = strings.Join(fps, ";")
	}
	return p
}

func decode(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("decoding object: %w", err)
	}
	return m, nil
}

func nested(m map[string]any, path ...string) any {
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

func generation(obj map[string]any) int64 {
	if g, ok := nested(obj, "metadata", "generation").(float64); ok {
		return int64(g)
	}
	return 0
}

// num reads a replica count; absent means def (its meaning differs by type, see callers).
func num(v any, def int64) any {
	if v == nil {
		return def
	}
	if f, ok := v.(float64); ok && f == float64(int64(f)) {
		return int64(f)
	}
	return v
}

func images(template map[string]any) map[string]string {
	out := map[string]string{}
	for _, field := range []string{"initContainers", "containers"} {
		list, _ := nested(template, "spec", field).([]any)
		for _, c := range list {
			cm, _ := c.(map[string]any)
			name, _ := cm["name"].(string)
			image, _ := cm["image"].(string)
			out[name] = image
		}
	}
	return out
}

func joinMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

// normalizeTemplate drops the restart stamp and the images, which are classified separately.
func normalizeTemplate(t map[string]any) any {
	c := deepCopy(t)
	if ann, ok := nested(c, "metadata", "annotations").(map[string]any); ok {
		delete(ann, restartedAt)
		if len(ann) == 0 {
			delete(c["metadata"].(map[string]any), "annotations")
		}
	}
	for _, field := range []string{"initContainers", "containers"} {
		list, _ := nested(c, "spec", field).([]any)
		for _, x := range list {
			if cm, ok := x.(map[string]any); ok {
				delete(cm, "image")
			}
		}
	}
	return c
}

func otherSpec(spec map[string]any) map[string]any {
	c := deepCopy(spec)
	delete(c, "replicas")
	delete(c, "template")
	return c
}

func withoutMeta(obj map[string]any) map[string]any {
	c := deepCopy(obj)
	delete(c, "metadata")
	delete(c, "status")
	return c
}

func deepCopy(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	b, _ := json.Marshal(m)
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	return out
}

func hashOf(v any) string {
	b, _ := json.Marshal(v) // map keys are sorted, so this is canonical
	return sha(string(b))[:16]
}
