package kube

import (
	"context"
	"errors"
	"sync"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	authnv1client "k8s.io/client-go/kubernetes/typed/authentication/v1"
)

// TokenReviewer authenticates API callers by their Kubernetes ServiceAccount token (the same
// identity the admission webhook sees), with a short cache.
type TokenReviewer struct {
	Client authnv1client.AuthenticationV1Interface
	mu     sync.Mutex
	cache  map[string]cached
}

type cached struct {
	user   string
	groups []string
	until  time.Time
}

func (t *TokenReviewer) Authenticate(ctx context.Context, token string) (string, []string, error) {
	t.mu.Lock()
	if c, ok := t.cache[token]; ok && time.Now().Before(c.until) {
		t.mu.Unlock()
		return c.user, c.groups, nil
	}
	t.mu.Unlock()
	rev, err := t.Client.TokenReviews().Create(ctx,
		&authnv1.TokenReview{Spec: authnv1.TokenReviewSpec{Token: token}}, metav1.CreateOptions{})
	if err != nil {
		return "", nil, err
	}
	if !rev.Status.Authenticated {
		return "", nil, errors.New("token not authenticated: " + rev.Status.Error)
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cache == nil || len(t.cache) > 10000 {
		t.cache = map[string]cached{}
	}
	t.cache[token] = cached{rev.Status.User.Username, rev.Status.User.Groups, time.Now().Add(30 * time.Second)}
	return rev.Status.User.Username, rev.Status.User.Groups, nil
}
