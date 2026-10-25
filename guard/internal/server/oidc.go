package server

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// OIDC verifies people's OIDC tokens (an ID token, or an access token issued for the guard's
// audience) at the guard itself, so a decision over HTTP is recorded as the person the identity
// provider authenticated, not as a name someone typed. Keys come from the issuer's discovery
// document and are refreshed when a token names a key the guard hasn't seen (at most once a minute).
type OIDC struct {
	Issuer        string
	Audience      string
	UsernameClaim string // default "email"
	GroupsClaim   string // default "groups"
	Client        *http.Client

	mu      sync.Mutex
	keys    map[string]any
	fetched time.Time
}

// oidcAlgorithms are the signature algorithms accepted from an identity provider ("none" and
// HMAC never are: a public key must verify the token).
var oidcAlgorithms = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384"}

func (o *OIDC) Authenticate(ctx context.Context, token string) (string, []string, error) {
	// Kubernetes ServiceAccount tokens are JWTs too: don't look up keys for another issuer's token.
	unverified := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, unverified); err != nil {
		return "", nil, fmt.Errorf("invalid OIDC token: %w", err)
	}
	if iss, _ := unverified["iss"].(string); iss != o.Issuer {
		return "", nil, fmt.Errorf("invalid OIDC token: issued by %q, not %q", iss, o.Issuer)
	}
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return o.key(ctx, kid)
	}, jwt.WithValidMethods(oidcAlgorithms), jwt.WithIssuer(o.Issuer), jwt.WithAudience(o.Audience),
		jwt.WithExpirationRequired(), jwt.WithLeeway(30*time.Second))
	if err != nil {
		return "", nil, fmt.Errorf("invalid OIDC token: %w", err)
	}
	userClaim := o.UsernameClaim
	if userClaim == "" {
		userClaim = "email"
	}
	user, _ := claims[userClaim].(string)
	if user == "" {
		return "", nil, fmt.Errorf("invalid OIDC token: no %q claim", userClaim)
	}
	if userClaim == "email" {
		if verified, ok := claims["email_verified"].(bool); ok && !verified {
			return "", nil, errors.New("invalid OIDC token: the email address is not verified")
		}
	}
	groupsClaim := o.GroupsClaim
	if groupsClaim == "" {
		groupsClaim = "groups"
	}
	var groups []string
	if list, ok := claims[groupsClaim].([]any); ok {
		for _, g := range list {
			if s, ok := g.(string); ok {
				groups = append(groups, s)
			}
		}
	}
	return user, groups, nil
}

func (o *OIDC) key(ctx context.Context, kid string) (any, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if k, ok := o.keys[kid]; ok {
		return k, nil
	}
	if time.Since(o.fetched) < time.Minute {
		return nil, fmt.Errorf("unknown signing key %q", kid)
	}
	o.fetched = time.Now()
	keys, err := o.fetchKeys(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching the issuer's keys: %w", err)
	}
	o.keys = keys
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	if kid == "" && len(keys) == 1 { // a provider with a single key may omit kid
		for _, k := range keys {
			return k, nil
		}
	}
	return nil, fmt.Errorf("unknown signing key %q", kid)
}

func (o *OIDC) fetchKeys(ctx context.Context) (map[string]any, error) {
	var discovery struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := o.getJSON(ctx, strings.TrimSuffix(o.Issuer, "/")+"/.well-known/openid-configuration", &discovery); err != nil {
		return nil, err
	}
	if discovery.Issuer != o.Issuer {
		return nil, fmt.Errorf("discovery names issuer %q, expected %q", discovery.Issuer, o.Issuer)
	}
	var set struct {
		Keys []struct {
			Kty, Kid, Use, Crv, N, E, X, Y string
		} `json:"keys"`
	}
	if err := o.getJSON(ctx, discovery.JWKSURI, &set); err != nil {
		return nil, err
	}
	keys := map[string]any{}
	for _, k := range set.Keys {
		if k.Use == "enc" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil || len(e) > 4 {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		case "EC":
			curve := map[string]elliptic.Curve{"P-256": elliptic.P256(), "P-384": elliptic.P384()}[k.Crv]
			x, err1 := base64.RawURLEncoding.DecodeString(k.X)
			y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
			if curve == nil || err1 != nil || err2 != nil {
				continue
			}
			keys[k.Kid] = &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("the issuer publishes no usable signing keys")
	}
	return keys, nil
}

func (o *OIDC) getJSON(ctx context.Context, url string, v any) error {
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, res.StatusCode)
	}
	return json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(v)
}
