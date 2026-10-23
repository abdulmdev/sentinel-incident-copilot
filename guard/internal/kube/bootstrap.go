// Package kube is the guard's Kubernetes glue: it creates its own TLS certificate and signing key
// on first start (so installing needs no cert-manager and no secrets in manifests), keeps the
// webhook CA bundles current, publishes the public keys, and authenticates API agents by their
// ServiceAccount tokens.
package kube

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	admregv1client "k8s.io/client-go/kubernetes/typed/admissionregistration/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/util/retry"
)

const (
	TLSSecret     = "sentinel-guard-tls"
	SigningSecret = "sentinel-guard-signing"
	JWKSConfigMap = "sentinel-guard-jwks"
)

var labels = map[string]string{"app.kubernetes.io/name": "sentinel-guard"}

// EnsureSecret returns the secret's data, creating it with gen() if it doesn't exist. Replicas
// starting together race on create; the losers read the winner's copy, so all share one.
func EnsureSecret(ctx context.Context, c corev1client.CoreV1Interface, ns, name string, gen func() (map[string][]byte, error)) (map[string][]byte, error) {
	secrets := c.Secrets(ns)
	s, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		return s.Data, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	data, err := gen()
	if err != nil {
		return nil, err
	}
	_, err = secrets.Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}, Data: data}, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		s, err := secrets.Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		return s.Data, nil
	}
	return data, err
}

// GenerateServingCert makes a CA and a serving certificate for the guard's Service DNS names.
func GenerateServingCert(service, ns string, validity time.Duration) (map[string][]byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	caTpl := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: "sentinel-guard-ca"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(validity),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTpl, caTpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	dns := []string{service, service + "." + ns, service + "." + ns + ".svc", service + "." + ns + ".svc.cluster.local"}
	tpl := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: dns[2]}, DNSNames: dns,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(validity),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"ca.crt":  pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		"tls.key": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
	}, nil
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	return n
}

func TLSConfig(data map[string][]byte) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(data["tls.crt"], data["tls.key"])
	if err != nil {
		return nil, fmt.Errorf("serving certificate: %w", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

// EnsureCABundles points the guard's webhook configurations at its CA. It is called at start and
// periodically, so a re-applied manifest that drops the bundle heals itself within a minute.
func EnsureCABundles(ctx context.Context, c admregv1client.AdmissionregistrationV1Interface, validating, mutating string, ca []byte) error {
	if validating != "" {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cfg, err := c.ValidatingWebhookConfigurations().Get(ctx, validating, metav1.GetOptions{})
			if err != nil {
				return err
			}
			changed := false
			for i := range cfg.Webhooks {
				if !bytes.Equal(cfg.Webhooks[i].ClientConfig.CABundle, ca) {
					cfg.Webhooks[i].ClientConfig.CABundle, changed = ca, true
				}
			}
			if !changed {
				return nil
			}
			_, err = c.ValidatingWebhookConfigurations().Update(ctx, cfg, metav1.UpdateOptions{})
			return err
		})
		if err != nil {
			return fmt.Errorf("validating webhook %s: %w", validating, err)
		}
	}
	if mutating != "" {
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			cfg, err := c.MutatingWebhookConfigurations().Get(ctx, mutating, metav1.GetOptions{})
			if err != nil {
				return err
			}
			changed := false
			for i := range cfg.Webhooks {
				if !bytes.Equal(cfg.Webhooks[i].ClientConfig.CABundle, ca) {
					cfg.Webhooks[i].ClientConfig.CABundle, changed = ca, true
				}
			}
			if !changed {
				return nil
			}
			_, err = c.MutatingWebhookConfigurations().Update(ctx, cfg, metav1.UpdateOptions{})
			return err
		})
		if err != nil {
			return fmt.Errorf("mutating webhook %s: %w", mutating, err)
		}
	}
	return nil
}

// PublishJWKS writes the public keys to a ConfigMap, so anyone with read access (the kubectl
// plugin, an auditor) can verify the audit chain without trusting the guard's word for it.
func PublishJWKS(ctx context.Context, c corev1client.CoreV1Interface, ns string, jwks map[string]any) error {
	b, err := json.Marshal(jwks)
	if err != nil {
		return err
	}
	cms := c.ConfigMaps(ns)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, err := cms.Get(ctx, JWKSConfigMap, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = cms.Create(ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: JWKSConfigMap, Labels: labels},
				Data: map[string]string{"jwks.json": string(b)}}, metav1.CreateOptions{})
			if apierrors.IsAlreadyExists(err) {
				return apierrors.NewConflict(corev1.Resource("configmaps"), JWKSConfigMap, err)
			}
			return err
		}
		if err != nil {
			return err
		}
		if cm.Data["jwks.json"] == string(b) {
			return nil
		}
		cm.Data = map[string]string{"jwks.json": string(b)}
		_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
}
