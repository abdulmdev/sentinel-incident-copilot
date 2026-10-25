// Command sentinel-guard runs Sentinel Guard: the safe-action layer between AI agents and
// production. Two modes:
//
//	kubernetes  in a cluster: admission webhooks + action API, state in ActionRequest objects
//	memory      one process (docker run): action API with HTTP approvals, state in memory
package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	admregv1client "k8s.io/client-go/kubernetes/typed/admissionregistration/v1"
	authnv1client "k8s.io/client-go/kubernetes/typed/authentication/v1"
	coordv1client "k8s.io/client-go/kubernetes/typed/coordination/v1"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/guard"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/kube"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/server"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/internal/store"
	"github.com/abdulmdev/sentinel-incident-copilot/guard/policies"
)

var version = "dev"

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	defaultMode := "memory"
	if os.Getenv("KUBERNETES_SERVICE_HOST") != "" {
		defaultMode = "kubernetes"
	}
	mode := flag.String("mode", env("SENTINEL_GUARD_MODE", defaultMode), "kubernetes | memory")
	policyPath := flag.String("policy", env("SENTINEL_GUARD_POLICY", ""), "policy file (default: built-in policy)")
	addr := flag.String("addr", env("SENTINEL_GUARD_ADDR", ""), "listen address (default :8443 in kubernetes, :8080 in memory)")
	metricsAddr := flag.String("metrics-addr", env("SENTINEL_GUARD_METRICS_ADDR", ":9090"), "metrics listen address")
	namespace := flag.String("namespace", env("POD_NAMESPACE", "sentinel-system"), "namespace for the guard's own objects")
	service := flag.String("service", env("SENTINEL_GUARD_SERVICE", "sentinel-guard"), "the guard's Service name (TLS names)")
	validating := flag.String("validating-webhook", env("SENTINEL_GUARD_VALIDATING_WEBHOOK", "sentinel-guard"), "ValidatingWebhookConfiguration to keep the CA bundle on")
	mutating := flag.String("mutating-webhook", env("SENTINEL_GUARD_MUTATING_WEBHOOK", "sentinel-guard"), "MutatingWebhookConfiguration to keep the CA bundle on")
	issuer := flag.String("issuer", env("SENTINEL_GUARD_ISSUER", "sentinel-guard"), "issuer of capabilities")
	apiAddr := flag.String("api-addr", env("SENTINEL_GUARD_API_ADDR", ""), "kubernetes mode: also serve the action API (no webhooks) over plain HTTP here, for in-cluster clients")
	verifyEvery := flag.Duration("verify-interval", envDuration("SENTINEL_GUARD_VERIFY_INTERVAL", 5*time.Minute), "how often each replica verifies the whole audit chain (sentinel_guard_audit_chain_ok)")
	showVersion := flag.Bool("version", false, "print the version")
	healthcheck := flag.Bool("healthcheck", false, "probe this guard's /readyz (memory mode, plain HTTP) and exit 0 if ready: for container health checks, as the image has no shell")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	if *healthcheck {
		os.Exit(probe(*addr))
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log, *mode, *policyPath, *addr, *apiAddr, *metricsAddr, *namespace, *service, *validating, *mutating, *issuer, *verifyEvery); err != nil {
		log.Error("sentinel-guard stopped", "error", err)
		os.Exit(1)
	}
}

// probe asks a guard listening on addr (default :8080) whether it is ready.
func probe(addr string) int {
	if addr == "" {
		addr = ":8080"
	}
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 2 * time.Second}
	res, err := client.Get("http://" + addr + "/readyz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "not ready:", err)
		return 1
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "not ready: HTTP", res.StatusCode)
		return 1
	}
	return 0
}

func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil && d > 0 {
		return d
	}
	return def
}

func run(log *slog.Logger, mode, policyPath, addr, apiAddr, metricsAddr, ns, service, validating, mutating, issuer string, verifyEvery time.Duration) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	raw := policies.Default
	if policyPath != "" {
		b, err := os.ReadFile(policyPath)
		if err != nil {
			return err
		}
		raw = b
	}
	policy, err := guard.ParsePolicy(raw)
	if err != nil {
		return err
	}
	if mode == "kubernetes" { // agents may never touch the guard itself
		policy.ProtectedNamespaces = append(policy.ProtectedNamespaces, ns)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	srv := &server.Server{Metrics: server.NewMetrics(reg), Log: log, Version: version}
	var ready atomic.Bool
	srv.Ready = ready.Load

	var (
		st        guard.Store
		keyPEM    []byte
		tlsConfig *tls.Config
		refresh   func(context.Context) error
		core      corev1client.CoreV1Interface
		// elect runs work only while this replica holds the housekeeping lease (one replica at a
		// time applies decisions and expiries, so replicas don't race each other for them).
		elect = func(work func(context.Context)) { work(ctx) }
	)
	switch mode {
	case "kubernetes":
		cfg, err := rest.InClusterConfig()
		if err != nil {
			return err
		}
		cfg.QPS, cfg.Burst = 200, 400 // the webhook is on the request path: don't queue behind client-side throttling
		if core, err = corev1client.NewForConfig(cfg); err != nil {
			return err
		}
		dyn, err := dynamic.NewForConfig(cfg)
		if err != nil {
			return err
		}
		kst := &store.Kube{Dynamic: dyn, Core: core, Namespace: ns}
		if err := kst.Start(ctx); err != nil {
			return err
		}
		st = kst
		leases, err := coordv1client.NewForConfig(cfg)
		if err != nil {
			return err
		}
		elect = func(work func(context.Context)) {
			lock := &resourcelock.LeaseLock{
				LeaseMeta:  metav1.ObjectMeta{Name: "sentinel-guard-housekeeping", Namespace: ns},
				Client:     leases,
				LockConfig: resourcelock.ResourceLockConfig{Identity: env("POD_NAME", randomToken()[:12])},
			}
			for ctx.Err() == nil {
				leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
					Lock: lock, ReleaseOnCancel: true, Name: "sentinel-guard",
					LeaseDuration: 15 * time.Second, RenewDeadline: 10 * time.Second, RetryPeriod: 2 * time.Second,
					Callbacks: leaderelection.LeaderCallbacks{
						OnStartedLeading: work,
						OnStoppedLeading: func() {},
					},
				})
			}
		}
		signing, err := kube.EnsureSecret(ctx, core, ns, kube.SigningSecret, func() (map[string][]byte, error) {
			k, err := guard.GenerateKeyPEM()
			return map[string][]byte{"key.pem": k}, err
		})
		if err != nil {
			return fmt.Errorf("signing key: %w", err)
		}
		keyPEM = signing["key.pem"]
		certs, err := kube.EnsureSecret(ctx, core, ns, kube.TLSSecret, func() (map[string][]byte, error) {
			return kube.GenerateServingCert(service, ns, 10*365*24*time.Hour)
		})
		if err != nil {
			return fmt.Errorf("serving certificate: %w", err)
		}
		if tlsConfig, err = kube.TLSConfig(certs); err != nil {
			return err
		}
		authn, err := authnv1client.NewForConfig(cfg)
		if err != nil {
			return err
		}
		admreg, err := admregv1client.NewForConfig(cfg)
		if err != nil {
			return err
		}
		srv.Agents = &kube.TokenReviewer{Client: authn}
		srv.Generation = func(ctx context.Context, gvr schema.GroupVersionResource, ns, name string) (int64, error) {
			obj, err := dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
			if err != nil {
				return 0, err
			}
			return obj.GetGeneration(), nil
		}
		srv.GuardUser = "system:serviceaccount:" + ns + ":" + env("SERVICE_ACCOUNT", "sentinel-guard")
		refresh = func(ctx context.Context) error {
			return kube.EnsureCABundles(ctx, admreg, validating, mutating, certs["ca.crt"])
		}
		if addr == "" {
			addr = ":8443"
		}
	case "memory":
		st = store.NewMemory()
		if keyPEM, err = guard.GenerateKeyPEM(); err != nil {
			return err
		}
		agents, admin, delegates, people := memoryTokens(log)
		srv.Agents, srv.AdminToken, srv.ApproverTokens = agents, admin, people
		policy.Delegates.Users = append(policy.Delegates.Users, delegates...)
		if addr == "" {
			addr = ":8080"
		}
	default:
		return fmt.Errorf("unknown mode %q (kubernetes | memory)", mode)
	}

	signer, err := guard.NewSigner(keyPEM, issuer)
	if err != nil {
		return err
	}
	var prober guard.Prober
	if policy.Prometheus.URL != "" {
		prober = &guard.PrometheusProber{URL: policy.Prometheus.URL, Client: &http.Client{Timeout: 3 * time.Second}}
	}
	srv.Engine = guard.NewEngine(st, signer, prober, policy)
	if issuer := os.Getenv("SENTINEL_GUARD_OIDC_ISSUER"); issuer != "" {
		audience := os.Getenv("SENTINEL_GUARD_OIDC_AUDIENCE")
		if audience == "" {
			return errors.New("SENTINEL_GUARD_OIDC_AUDIENCE is required with SENTINEL_GUARD_OIDC_ISSUER")
		}
		srv.OIDC = &server.OIDC{Issuer: issuer, Audience: audience,
			UsernameClaim: os.Getenv("SENTINEL_GUARD_OIDC_USERNAME_CLAIM"), GroupsClaim: os.Getenv("SENTINEL_GUARD_OIDC_GROUPS_CLAIM")}
		log.Info("people's OIDC tokens are verified", "issuer", issuer, "audience", audience,
			"approverUsers", policy.Approvers.Users, "approverGroups", policy.Approvers.Groups)
	}
	if url := os.Getenv("SENTINEL_GUARD_NOTIFY_URL"); url != "" {
		srv.Notify = server.NewNotifier(url, os.Getenv("SENTINEL_GUARD_PUBLIC_URL"), log, reg)
		go srv.Notify.Run(ctx)
	}
	srv.Hook()
	// Keep the policy version in force, signed, so every decision can be traced to its rules.
	if err := srv.Engine.RecordPolicy(ctx); err != nil {
		return err
	}

	if mode == "kubernetes" {
		if err := kube.PublishJWKS(ctx, core, ns, signer.JWKS()); err != nil {
			return fmt.Errorf("publishing public keys: %w", err)
		}
		if err := refresh(ctx); err != nil {
			log.Warn("could not set webhook CA bundles yet", "error", err)
		}
	}

	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler(), TLSConfig: tlsConfig,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second}
	metricsSrv := &http.Server{Addr: metricsAddr, Handler: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}), ReadHeaderTimeout: 5 * time.Second}
	errs := make(chan error, 3)
	var apiSrv *http.Server
	if apiAddr != "" && tlsConfig != nil {
		apiSrv = &http.Server{Addr: apiAddr, Handler: srv.APIHandler(), ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second}
		go func() { errs <- apiSrv.ListenAndServe() }()
	}
	go func() {
		var err error
		if tlsConfig != nil {
			err = httpSrv.ListenAndServeTLS("", "")
		} else {
			err = httpSrv.ListenAndServe()
		}
		errs <- err
	}()
	go func() { errs <- metricsSrv.ListenAndServe() }()

	// Housekeeping, on the lease holder only: apply decisions, expire stale requests, heal the
	// CA bundles. Requests are read from the watch cache, so a tick costs no API call.
	go elect(func(ctx context.Context) {
		log.Info("housekeeping lease acquired")
		t := time.NewTicker(time.Second)
		defer t.Stop()
		var n int
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := srv.Engine.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
					log.Warn("reconcile", "error", err)
				}
				if n++; refresh != nil && n%60 == 0 {
					if err := refresh(ctx); err != nil {
						log.Warn("webhook CA bundles", "error", err)
					}
				}
			}
		}
	})
	go srv.Monitor(ctx, 15*time.Second, verifyEvery)
	ready.Store(true)
	log.Info("sentinel-guard started", "version", version, "mode", mode, "addr", addr, "kid", signer.KeyID(),
		"policy", srv.Engine.PolicyVersion()[:12],
		"agentGroups", policy.Agents.Groups, "agentUsers", policy.Agents.Users)

	select {
	case <-ctx.Done():
	case err := <-errs:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	ready.Store(false)
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = metricsSrv.Shutdown(shutdown)
	if apiSrv != nil {
		_ = apiSrv.Shutdown(shutdown)
	}
	return httpSrv.Shutdown(shutdown)
}

// memoryTokens reads agent, delegate, approver and admin tokens from the environment, or generates
// an agent and an admin token and prints them once (single-process mode is for trying the guard out).
// With personal approver tokens there is no shared admin token unless one is set explicitly.
func memoryTokens(log *slog.Logger) (server.StaticTokens, string, []string, server.StaticTokens) {
	agents := server.StaticTokens{}
	for _, pair := range strings.Split(os.Getenv("SENTINEL_GUARD_AGENT_TOKENS"), ",") {
		if name, token, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && name != "" && token != "" {
			agents[token] = name
		}
	}
	// Delegates (e.g. Sentinel's gateway) authenticate with their own tokens and may record
	// decisions on behalf of the people they signed in.
	var delegates []string
	for _, pair := range strings.Split(os.Getenv("SENTINEL_GUARD_DELEGATE_TOKENS"), ",") {
		if name, token, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && name != "" && token != "" {
			agents[token] = name
			delegates = append(delegates, name)
		}
	}
	// People who decide with their own token: each decision is recorded as that person.
	people := server.StaticTokens{}
	for _, pair := range strings.Split(os.Getenv("SENTINEL_GUARD_APPROVER_TOKENS"), ",") {
		if name, token, ok := strings.Cut(strings.TrimSpace(pair), "="); ok && name != "" && token != "" {
			people[token] = name
		}
	}
	admin := os.Getenv("SENTINEL_GUARD_ADMIN_TOKEN")
	if len(agents) == 0 {
		token := randomToken()
		agents[token] = "demo-agent"
		fmt.Fprintf(os.Stderr, "\n  Sentinel Guard (memory mode) generated tokens for trying it out:\n    agent \"demo-agent\": %s\n", token)
	}
	if admin == "" && len(people) == 0 {
		admin = randomToken()
		fmt.Fprintf(os.Stderr, "    approver (admin):   %s\n  Set SENTINEL_GUARD_AGENT_TOKENS=name=token,... and SENTINEL_GUARD_ADMIN_TOKEN to choose your own.\n\n", admin)
	}
	approvers := make([]string, 0, len(people))
	for _, name := range people {
		approvers = append(approvers, name)
	}
	slices.Sort(approvers)
	log.Info("memory mode: state is kept in this process only", "tokens", len(agents), "delegates", delegates,
		"approvers", approvers, "adminToken", admin != "")
	return agents, admin, delegates, people
}

func randomToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
