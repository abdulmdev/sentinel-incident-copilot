package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/cache"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
)

// ActionRequests is the custom resource that holds every request.
var ActionRequests = schema.GroupVersionResource{Group: "sentinel.dev", Version: "v1alpha1", Resource: "actionrequests"}

const (
	KeyLabel      = "sentinel.dev/key"
	HeadConfigMap = "sentinel-guard-audit-head"
	maxAttempts   = 20
	keyIndex      = "key"
)

// Spec holds what was asked and the human decision (written by approvers through the API server,
// stamped by the approval webhook); status holds what the guard did. Approvers can only patch spec.
var (
	specFields   = []string{"key", "source", "agent", "action", "target", "change", "fingerprint", "intent", "risk", "autonomy", "createdAt", "decision", "decidedBy", "decisionNote", "decidedAt"}
	statusFields = []string{"phase", "reasons", "preconditions", "expiresAt", "executedAt", "exact", "execGeneration", "capabilityJti", "audit"}
)

// Kube keeps requests as ActionRequest objects and the audit chain head in a ConfigMap.
//
// Every write carries the object's resourceVersion, so the API server is the compare-and-swap:
// when replicas race to consume one approval, exactly one write succeeds. Reads come from a
// watch-fed cache (Start), which every replica keeps current within milliseconds; a stale read
// only costs a conflict and a fresh read, never correctness. Without Start (the kubectl plugin),
// reads go straight to the API.
type Kube struct {
	Dynamic   dynamic.Interface
	Core      corev1client.CoreV1Interface
	Namespace string

	reqs  cache.SharedIndexInformer
	heads cache.SharedIndexInformer

	mu       sync.Mutex
	written  map[string]*unstructured.Unstructured // our own latest writes, until the watch catches up
	headSeen *corev1.ConfigMap
}

// Start runs the watch caches and waits until they are in sync.
func (k *Kube) Start(ctx context.Context) error {
	f := dynamicinformer.NewFilteredDynamicSharedInformerFactory(k.Dynamic, 10*time.Minute, k.Namespace, nil)
	k.reqs = f.ForResource(ActionRequests).Informer()
	if err := k.reqs.AddIndexers(cache.Indexers{keyIndex: func(obj any) ([]string, error) {
		if u, ok := obj.(*unstructured.Unstructured); ok {
			return []string{u.GetLabels()[KeyLabel]}, nil
		}
		return nil, nil
	}}); err != nil {
		return err
	}
	byName := "metadata.name=" + HeadConfigMap
	cms := k.Core.ConfigMaps(k.Namespace)
	k.heads = cache.NewSharedIndexInformer(&cache.ListWatch{
		ListWithContextFunc: func(ctx context.Context, o metav1.ListOptions) (runtime.Object, error) {
			o.FieldSelector = byName
			return cms.List(ctx, o)
		},
		WatchFuncWithContext: func(ctx context.Context, o metav1.ListOptions) (watch.Interface, error) {
			o.FieldSelector = byName
			return cms.Watch(ctx, o)
		},
	}, &corev1.ConfigMap{}, 10*time.Minute, cache.Indexers{})
	go k.reqs.RunWithContext(ctx)
	go k.heads.RunWithContext(ctx)
	if !cache.WaitForCacheSync(ctx.Done(), k.reqs.HasSynced, k.heads.HasSynced) {
		return errors.New("action request cache did not sync")
	}
	return nil
}

func (k *Kube) res() dynamic.ResourceInterface {
	return k.Dynamic.Resource(ActionRequests).Namespace(k.Namespace)
}

// ToObject converts a request to its ActionRequest form.
func ToObject(r *guard.Request) (*unstructured.Unstructured, error) {
	fields, err := toMap(r)
	if err != nil {
		return nil, err
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "sentinel.dev/v1alpha1", "kind": "ActionRequest",
		"metadata": map[string]any{"name": r.ID, "labels": map[string]any{KeyLabel: r.Key}},
		"spec":     pick(fields, specFields), "status": pick(fields, statusFields),
	}}
	return obj, nil
}

// FromObject reads a request back from an ActionRequest.
func FromObject(obj *unstructured.Unstructured) (*guard.Request, error) {
	merged := map[string]any{"id": obj.GetName()}
	for _, part := range []string{"spec", "status"} {
		if m, ok := obj.Object[part].(map[string]any); ok {
			for k, v := range m {
				merged[k] = v
			}
		}
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	var r guard.Request
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("actionrequest %s: %w", obj.GetName(), err)
	}
	return &r, nil
}

func toMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

func pick(m map[string]any, keys []string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}

func wrap(err error, what string) error {
	switch {
	case err == nil:
		return nil
	case apierrors.IsNotFound(err):
		return fmt.Errorf("%w: %s", guard.ErrNotFound, what)
	case apierrors.IsConflict(err), apierrors.IsAlreadyExists(err):
		return fmt.Errorf("%w: %s: %v", guard.ErrConflict, what, err)
	}
	return err
}

func auditLen(u *unstructured.Unstructured) int {
	a, _, _ := unstructured.NestedSlice(u.Object, "status", "audit")
	return len(a)
}

// cached returns the freshest copy this replica knows: the watch cache, or our own write if the
// watch hasn't delivered it yet (the guard appends an audit event on every write it makes, so a
// longer trail is newer).
func (k *Kube) cached(name string) *unstructured.Unstructured {
	var fromWatch *unstructured.Unstructured
	if k.reqs != nil {
		if item, ok, _ := k.reqs.GetIndexer().GetByKey(k.Namespace + "/" + name); ok {
			fromWatch = item.(*unstructured.Unstructured)
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	own := k.written[name]
	switch {
	case own == nil && fromWatch == nil:
		return nil
	case own == nil:
		return fromWatch.DeepCopy()
	case fromWatch == nil || auditLen(own) > auditLen(fromWatch):
		return own.DeepCopy()
	}
	delete(k.written, name) // the watch caught up
	return fromWatch.DeepCopy()
}

func (k *Kube) remember(obj *unstructured.Unstructured) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.written == nil || len(k.written) > 1000 {
		k.written = map[string]*unstructured.Unstructured{}
	}
	k.written[obj.GetName()] = obj.DeepCopy()
}

func (k *Kube) forget(name string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.written, name)
}

func (k *Kube) Create(ctx context.Context, r *guard.Request) error {
	obj, err := ToObject(r)
	if err != nil {
		return err
	}
	status := obj.Object["status"]
	created, err := k.res().Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return wrap(err, "creating "+r.ID)
	}
	// The status subresource ignores status on create: write it separately.
	created.Object["status"] = status
	updated, err := k.res().UpdateStatus(ctx, created, metav1.UpdateOptions{})
	if err == nil {
		k.remember(updated)
	}
	return wrap(err, "status of "+r.ID)
}

func (k *Kube) Get(ctx context.Context, id string) (*guard.Request, error) {
	if obj := k.cached(id); obj != nil {
		return FromObject(obj)
	}
	obj, err := k.res().Get(ctx, id, metav1.GetOptions{})
	if err != nil {
		return nil, wrap(err, "request "+id)
	}
	return FromObject(obj)
}

func (k *Kube) fromObjects(items []*unstructured.Unstructured) ([]*guard.Request, error) {
	out := make([]*guard.Request, 0, len(items))
	for _, item := range items {
		if own := k.cached(item.GetName()); own != nil {
			item = own
		}
		r, err := FromObject(item)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (k *Kube) list(ctx context.Context, selector string) ([]*guard.Request, error) {
	l, err := k.res().List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, wrap(err, "listing requests")
	}
	items := make([]*unstructured.Unstructured, len(l.Items))
	for i := range l.Items {
		items[i] = &l.Items[i]
	}
	return k.fromObjects(items)
}

func indexed(objs []any) []*unstructured.Unstructured {
	out := make([]*unstructured.Unstructured, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.(*unstructured.Unstructured))
	}
	return out
}

func (k *Kube) List(ctx context.Context) ([]*guard.Request, error) {
	if k.reqs != nil {
		return k.fromObjects(indexed(k.reqs.GetIndexer().List()))
	}
	return k.list(ctx, "")
}

func (k *Kube) FindByKey(ctx context.Context, key string) ([]*guard.Request, error) {
	if k.reqs != nil {
		objs, err := k.reqs.GetIndexer().ByIndex(keyIndex, key)
		if err != nil {
			return nil, err
		}
		return k.fromObjects(indexed(objs))
	}
	return k.list(ctx, KeyLabel+"="+key)
}

func (k *Kube) Update(ctx context.Context, id string, fn func(*guard.Request) error) (*guard.Request, error) {
	obj := k.cached(id)
	for range maxAttempts {
		if obj == nil {
			var err error
			if obj, err = k.res().Get(ctx, id, metav1.GetOptions{}); err != nil {
				return nil, wrap(err, "request "+id)
			}
		}
		r, err := FromObject(obj)
		if err != nil {
			return nil, err
		}
		if err := fn(r); err != nil {
			return nil, err
		}
		next, err := ToObject(r)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(normalize(obj.Object["spec"]), normalize(next.Object["spec"])) {
			obj.Object["spec"] = next.Object["spec"]
			if obj, err = k.res().Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
				obj = nil
				if apierrors.IsConflict(err) {
					k.forget(id)
					continue
				}
				return nil, wrap(err, "updating "+id)
			}
		}
		obj.Object["status"] = next.Object["status"]
		updated, err := k.res().UpdateStatus(ctx, obj, metav1.UpdateOptions{})
		if err != nil {
			obj = nil // someone else changed it: re-read and re-apply fn to the new state
			k.forget(id)
			if apierrors.IsConflict(err) {
				continue
			}
			return nil, wrap(err, "updating status of "+id)
		}
		k.remember(updated)
		return r, nil
	}
	return nil, fmt.Errorf("%w: request %s kept changing", guard.ErrConflict, id)
}

func normalize(v any) any {
	b, _ := json.Marshal(v)
	var out any
	_ = json.Unmarshal(b, &out)
	return out
}

// cachedHead returns the freshest chain head this replica knows (the higher seq: it only grows).
func (k *Kube) cachedHead() *corev1.ConfigMap {
	var fromWatch *corev1.ConfigMap
	if k.heads != nil {
		if item, ok, _ := k.heads.GetStore().GetByKey(k.Namespace + "/" + HeadConfigMap); ok {
			fromWatch = item.(*corev1.ConfigMap)
		}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	own := k.headSeen
	switch {
	case own == nil && fromWatch == nil:
		return nil
	case own == nil:
		return fromWatch.DeepCopy()
	case fromWatch == nil:
		return own.DeepCopy()
	}
	a, _ := parseHead(own)
	b, _ := parseHead(fromWatch)
	if a.Seq > b.Seq {
		return own.DeepCopy()
	}
	return fromWatch.DeepCopy()
}

func (k *Kube) setHead(cm *corev1.ConfigMap) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.headSeen = cm
}

func (k *Kube) Head(ctx context.Context) (guard.Head, error) {
	if k.heads != nil {
		if cm := k.cachedHead(); cm != nil {
			return parseHead(cm)
		}
	}
	cm, err := k.Core.ConfigMaps(k.Namespace).Get(ctx, HeadConfigMap, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return guard.Head{}, nil
	}
	if err != nil {
		return guard.Head{}, err
	}
	return parseHead(cm)
}

func parseHead(cm *corev1.ConfigMap) (guard.Head, error) {
	seq, err := strconv.ParseInt(cm.Data["seq"], 10, 64)
	if err != nil {
		return guard.Head{}, fmt.Errorf("audit head: bad seq %q", cm.Data["seq"])
	}
	h := guard.Head{Seq: seq, Hash: cm.Data["hash"]}
	if v := cm.Data["voids"]; v != "" {
		if err := json.Unmarshal([]byte(v), &h.Voids); err != nil {
			return guard.Head{}, fmt.Errorf("audit head: bad voids: %w", err)
		}
	}
	return h, nil
}

func (k *Kube) AdvanceHead(ctx context.Context, fn func(guard.Head) (guard.Head, error)) (guard.Head, error) {
	cms := k.Core.ConfigMaps(k.Namespace)
	cm := k.cachedHead()
	for range maxAttempts {
		if cm == nil {
			var err error
			cm, err = cms.Get(ctx, HeadConfigMap, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				next, err := fn(guard.Head{})
				if err != nil {
					return guard.Head{}, err
				}
				created, err := cms.Create(ctx, &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: HeadConfigMap, Labels: map[string]string{"app.kubernetes.io/name": "sentinel-guard"}},
					Data:       headData(next)}, metav1.CreateOptions{})
				if apierrors.IsAlreadyExists(err) {
					cm = nil
					continue
				}
				k.setHead(created)
				return next, err
			}
			if err != nil {
				return guard.Head{}, err
			}
		}
		cur, err := parseHead(cm)
		if err != nil {
			return guard.Head{}, err
		}
		next, err := fn(cur)
		if err != nil {
			return guard.Head{}, err
		}
		cm.Data = headData(next)
		updated, err := cms.Update(ctx, cm, metav1.UpdateOptions{})
		if err != nil {
			cm = nil // stale: read it fresh
			if apierrors.IsConflict(err) {
				continue
			}
			return guard.Head{}, err
		}
		k.setHead(updated)
		return next, nil
	}
	return guard.Head{}, errors.New("audit head: too much contention")
}

func headData(h guard.Head) map[string]string {
	d := map[string]string{"seq": strconv.FormatInt(h.Seq, 10), "hash": h.Hash}
	if len(h.Voids) > 0 {
		b, _ := json.Marshal(h.Voids)
		d["voids"] = string(b)
	}
	return d
}
