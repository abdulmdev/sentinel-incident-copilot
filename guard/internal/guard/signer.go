package guard

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// CapabilityAudience is the aud of every capability; executors accept nothing else.
const CapabilityAudience = "sentinel-action"

// Signer holds the guard's ES256 key. Only the guard can mint capabilities or sign audit events;
// everyone else verifies with the public JWKS.
type Signer struct {
	key    *ecdsa.PrivateKey
	kid    string
	issuer string
}

func GenerateKeyPEM() ([]byte, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(k)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), nil
}

func NewSigner(keyPEM []byte, issuer string) (*Signer, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("signing key: not PEM")
	}
	k, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("signing key: %w", err)
	}
	if k.Curve != elliptic.P256() {
		return nil, errors.New("signing key: must be EC P-256 (ES256)")
	}
	return &Signer{key: k, kid: keyID(&k.PublicKey), issuer: issuer}, nil
}

func keyID(pub *ecdsa.PublicKey) string {
	h := sha256.Sum256(append(pad32(pub.X), pad32(pub.Y)...))
	return base64.RawURLEncoding.EncodeToString(h[:])[:16]
}

func pad32(n *big.Int) []byte {
	b := make([]byte, 32)
	return n.FillBytes(b)
}

func (s *Signer) KeyID() string  { return s.kid }
func (s *Signer) Issuer() string { return s.issuer }

// JWKS is the public key set, safe to publish.
func (s *Signer) JWKS() map[string]any {
	return map[string]any{"keys": []map[string]any{{
		"kty": "EC", "crv": "P-256", "use": "sig", "alg": "ES256", "kid": s.kid,
		"x": base64.RawURLEncoding.EncodeToString(pad32(s.key.X)),
		"y": base64.RawURLEncoding.EncodeToString(pad32(s.key.Y)),
	}}}
}

// ParseJWKS reads public keys (as served by the guard) for verifiers such as the kubectl plugin.
func ParseJWKS(b []byte) (map[string]*ecdsa.PublicKey, error) {
	var set struct {
		Keys []struct{ Kty, Crv, Kid, X, Y string } `json:"keys"`
	}
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, fmt.Errorf("jwks: %w", err)
	}
	out := map[string]*ecdsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "EC" || k.Crv != "P-256" {
			continue
		}
		x, err1 := base64.RawURLEncoding.DecodeString(k.X)
		y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("jwks: bad coordinates for %s", k.Kid)
		}
		out[k.Kid] = &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
	}
	return out, nil
}

// CapabilityClaims authorize exactly one approved action, once.
type CapabilityClaims struct {
	RequestID   string            `json:"request_id"`
	Action      string            `json:"action"`
	Target      string            `json:"target"`
	Namespace   string            `json:"namespace,omitempty"` // the target's scope (a namespace, or a tenant)
	Change      map[string]string `json:"change,omitempty"`    // the approved parameters, for the executor to check
	Fingerprint string            `json:"fingerprint"`
	ApprovedBy  string            `json:"approved_by"`
	jwt.RegisteredClaims
}

func (s *Signer) MintCapability(r *Request, expires time.Time) (token, jti string, err error) {
	jti = randomID("cap-")
	claims := CapabilityClaims{
		RequestID: r.ID, Action: r.Action, Target: r.Target.String(), Namespace: r.Target.Namespace, Change: r.Change,
		Fingerprint: r.Fingerprint, ApprovedBy: r.DecidedBy,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: s.issuer, Subject: r.Agent, Audience: jwt.ClaimStrings{CapabilityAudience}, ID: jti,
			IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(expires),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	t.Header["kid"] = s.kid
	token, err = t.SignedString(s.key)
	return token, jti, err
}

// VerifyCapability checks signature, algorithm, issuer, audience and expiry. Single use is
// enforced separately, by the store (Engine.Spend).
func (s *Signer) VerifyCapability(token string) (*CapabilityClaims, error) {
	var c CapabilityClaims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		if t.Header["kid"] != s.kid {
			return nil, fmt.Errorf("unknown signing key %v", t.Header["kid"])
		}
		return &s.key.PublicKey, nil
	}, jwt.WithValidMethods([]string{"ES256"}), jwt.WithIssuer(s.issuer), jwt.WithAudience(CapabilityAudience),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt())
	if err != nil {
		return nil, err
	}
	if c.ID == "" || c.RequestID == "" {
		return nil, errors.New("capability lacks jti or request_id")
	}
	return &c, nil
}

// SignHash signs an audit event hash (hex) as a raw r||s ES256 signature.
func (s *Signer) SignHash(hexHash string) (string, error) {
	digest, err := hex.DecodeString(hexHash)
	if err != nil {
		return "", err
	}
	r, sig, err := ecdsa.Sign(rand.Reader, s.key, digest)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(append(pad32(r), pad32(sig)...)), nil
}

func VerifyHashSignature(pub *ecdsa.PublicKey, hexHash, signature string) bool {
	digest, err := hex.DecodeString(hexHash)
	if err != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || len(raw) != 64 {
		return false
	}
	return ecdsa.Verify(pub, digest, new(big.Int).SetBytes(raw[:32]), new(big.Int).SetBytes(raw[32:]))
}

func (s *Signer) PublicKeys() map[string]*ecdsa.PublicKey {
	return map[string]*ecdsa.PublicKey{s.kid: &s.key.PublicKey}
}

func randomID(prefix string) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}
