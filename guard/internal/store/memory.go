// Package store holds the guard's storage backends: Memory for a single process (docker run,
// tests) and Kube, which keeps everything in the Kubernetes API so replicas share state without
// a database.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
)

// Memory is a process-local store. State is lost on restart: use it for one replica only.
type Memory struct {
	mu       sync.Mutex
	reqs     map[string]*guard.Request
	head     guard.Head
	policies []guard.PolicyRecord
}

func NewMemory() *Memory { return &Memory{reqs: map[string]*guard.Request{}} }

func clone(r *guard.Request) *guard.Request {
	b, _ := json.Marshal(r)
	var c guard.Request
	_ = json.Unmarshal(b, &c)
	return &c
}

func (m *Memory) Create(_ context.Context, r *guard.Request) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.reqs[r.ID]; ok {
		return fmt.Errorf("%w: %s exists", guard.ErrConflict, r.ID)
	}
	m.reqs[r.ID] = clone(r)
	return nil
}

func (m *Memory) Get(_ context.Context, id string) (*guard.Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.reqs[id]
	if !ok {
		return nil, fmt.Errorf("%w: request %s", guard.ErrNotFound, id)
	}
	return clone(r), nil
}

func (m *Memory) List(_ context.Context) ([]*guard.Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*guard.Request, 0, len(m.reqs))
	for _, r := range m.reqs {
		out = append(out, clone(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (m *Memory) FindByKey(_ context.Context, key string) ([]*guard.Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*guard.Request
	for _, r := range m.reqs {
		if r.Key == key {
			out = append(out, clone(r))
		}
	}
	return out, nil
}

func (m *Memory) Update(_ context.Context, id string, fn func(*guard.Request) error) (*guard.Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.reqs[id]
	if !ok {
		return nil, fmt.Errorf("%w: request %s", guard.ErrNotFound, id)
	}
	c := clone(r)
	if err := fn(c); err != nil {
		return nil, err
	}
	m.reqs[id] = clone(c)
	return c, nil
}

func (m *Memory) Head(_ context.Context) (guard.Head, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.head, nil
}

func (m *Memory) AdvanceHead(_ context.Context, fn func(guard.Head) (guard.Head, error)) (guard.Head, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, err := fn(m.head)
	if err != nil {
		return m.head, err
	}
	m.head = h
	return h, nil
}

func (m *Memory) RecordPolicy(_ context.Context, p guard.PolicyRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, have := range m.policies {
		if have.Version == p.Version {
			return nil
		}
	}
	m.policies = append(m.policies, p)
	return nil
}

func (m *Memory) Policies(_ context.Context) ([]guard.PolicyRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]guard.PolicyRecord(nil), m.policies...), nil
}

// Delete exists for tests that prove deletions are detected.
func (m *Memory) Delete(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.reqs, id)
}
