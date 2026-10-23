#!/usr/bin/env bash
# Sentinel Guard end to end on a real Kubernetes API server (kind), installed with the Helm chart.
#
#   scripts/guard-e2e.sh             # cluster, build, install, run every check, delete the cluster
#   KEEP=1 scripts/guard-e2e.sh      # keep the cluster afterwards
#   LOAD=1 scripts/guard-e2e.sh      # also measure latency (writes evals/guard/latency.json)
#
# Proves, against the real API server: agents wait for approval while humans don't; an approval
# admits exactly that change, once; agents can't approve; records can't be edited or deleted;
# server-side dry runs preview; with the guard down only agents are stopped; the audit verifies.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
export PATH="$ROOT/.tools:$ROOT/.tools/go/bin:$PATH" GOPATH="$ROOT/.tools/gopath" GOFLAGS=-modcacherw
CLUSTER=${CLUSTER:-sentinel-guard-e2e}
NS=sentinel-system
WORK=$(mktemp -d)
BIN=$WORK/bin; mkdir -p "$BIN"; export PATH="$BIN:$PATH"
PASS=0
cleanup() {
  rm -rf "$WORK"
  if [ "${KEEP:-0}" != 1 ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
}
trap cleanup EXIT
ok() { PASS=$((PASS+1)); echo "  ✓ $*"; }
die() { echo "  ✗ $*" >&2; exit 1; }

echo "Building the guard image and the kubectl plugin"
(cd guard && go build -trimpath -o "$BIN/kubectl-sentinel" ./cmd/kubectl-sentinel && go build -o "$BIN/loadgen" ./test/loadgen)
docker build -q -t sentinel/guard:e2e --build-arg VERSION=e2e guard >/dev/null

kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --wait 120s >/dev/null
kubectl config use-context "kind-$CLUSTER" >/dev/null
kind load docker-image --name "$CLUSTER" sentinel/guard:e2e >/dev/null

echo "Installing Sentinel Guard with the Helm chart"
helm upgrade --install sentinel-guard deploy/helm/sentinel-guard -n $NS --create-namespace \
  --set image.repository=sentinel/guard --set image.tag=e2e --set image.pullPolicy=Never --wait --timeout 3m >/dev/null
for _ in $(seq 1 60); do
  [ -n "$(kubectl get validatingwebhookconfiguration sentinel-guard -o jsonpath='{.webhooks[0].clientConfig.caBundle}')" ] && break
  sleep 1
done
[ -n "$(kubectl get validatingwebhookconfiguration sentinel-guard -o jsonpath='{.webhooks[0].clientConfig.caBundle}')" ] \
  || die "the guard never set its webhook CA bundle"
ok "installed: $(kubectl -n $NS get deploy sentinel-guard -o jsonpath='{.status.readyReplicas}') replicas ready, CA bundle set by the guard"

echo "A workload, and an agent ServiceAccount allowed to edit it"
kubectl create namespace agents --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl create namespace shop --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n agents create serviceaccount fixer --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n shop create rolebinding fixer-edit --clusterrole=edit --serviceaccount=agents:fixer --dry-run=client -o yaml | kubectl apply -f - >/dev/null
for d in web load; do
  kubectl -n shop create deployment $d --image=registry.k8s.io/pause:3.10 --replicas=2 --dry-run=client -o yaml | kubectl apply -f - >/dev/null
done
kubectl -n shop rollout status deploy/web --timeout 2m >/dev/null
kubectl config view --raw --minify > "$WORK/agent.kubeconfig"
TOKEN=$(kubectl -n agents create token fixer --duration=2h)
KUBECONFIG="$WORK/agent.kubeconfig" kubectl config set-credentials fixer --token="$TOKEN" >/dev/null
KUBECONFIG="$WORK/agent.kubeconfig" kubectl config set-context --current --user=fixer >/dev/null
agent() { KUBECONFIG="$WORK/agent.kubeconfig" kubectl "$@"; }
replicas() { kubectl -n shop get deploy/web -o jsonpath='{.spec.replicas}'; }
[ "$(agent auth whoami -o jsonpath='{.status.userInfo.username}')" = system:serviceaccount:agents:fixer ] || die "agent identity"

echo "Checks"
if kubectl -n shop scale deploy/web --replicas=2 >/dev/null; then ok "a human's change is never sent to the guard"; else die "a human's change is never sent to the guard"; fi

out=$(agent -n shop scale deploy/web --replicas=3 2>&1) && die "an agent's change was admitted without approval: $out"
ID=$(grep -o 'ar-[0-9a-f]\{8\}-[0-9]\{1,\}' <<<"$out" | head -1)
[ -n "$ID" ] && grep -q "kubectl sentinel approve $ID" <<<"$out" || die "the denial should name the request: $out"
if [ "$(replicas)" = 2 ]; then ok "agent scale denied, pending as $ID, with the approve command in the message"; else die "agent scale denied, pending as $ID, with the approve command in the message"; fi

out=$(agent -n shop scale deploy/web --replicas=3 2>&1) && die "admitted on retry"
if grep -q "$ID" <<<"$out"; then ok "a retry reports the same request (no duplicates)"; else die "a retry reports the same request (no duplicates)"; fi

out=$(agent -n shop scale deploy/web --replicas=3 --dry-run=server 2>&1) || die "server-side dry run refused: $out"
if grep -qi "would need human approval" <<<"$out"; then ok "server-side dry run is a preview: admitted, with a warning"; else die "server-side dry run is a preview: admitted, with a warning"; fi

kubectl -n $NS create rolebinding fixer-approver --clusterrole=sentinel-guard-approver --serviceaccount=agents:fixer >/dev/null
out=$(agent sentinel approve "$ID" 2>&1) && die "the agent approved its own request: $out"
if grep -q "agents cannot approve" <<<"$out"; then ok "an agent can't approve, even with RBAC to patch requests"; else die "an agent can't approve, even with RBAC to patch requests"; fi
kubectl -n $NS delete rolebinding fixer-approver >/dev/null

if kubectl sentinel requests | grep -q "$ID"; then ok "kubectl sentinel requests lists it"; else die "kubectl sentinel requests lists it"; fi
if kubectl sentinel approve "$ID" -m "e2e: scale for load" | grep -q "Approved $ID"; then ok "kubectl sentinel approve"; else die "kubectl sentinel approve"; fi
DECIDER=$(kubectl -n $NS get actionrequest "$ID" -o jsonpath='{.spec.decidedBy}')
if [ "$DECIDER" = kubernetes-admin ]; then ok "the approver recorded is the identity the API server authenticated"; else die "approver recorded as $DECIDER"; fi

agent -n shop scale deploy/web --replicas=3 >/dev/null 2>&1 || die "the approved change was not admitted"
if [ "$(replicas)" = 3 ]; then ok "the approved change is admitted"; else die "the approved change is admitted"; fi
kubectl -n shop scale deploy/web --replicas=2 >/dev/null   # a human reverts it
out=$(agent -n shop scale deploy/web --replicas=3 2>&1) && die "after a revert, the spent approval admitted the same change again"
ok "after a revert, repeating the approved change needs a new approval"
kubectl -n shop scale deploy/web --replicas=3 >/dev/null
out=$(agent -n shop scale deploy/web --replicas=4 2>&1) && die "a different change rode on the approval"
ok "a different change (replicas 4) needs its own approval"

out=$(agent -n shop rollout restart deploy/web 2>&1) && die "restart admitted without approval"
RID=$(grep -o 'ar-[0-9a-f]\{8\}-[0-9]\{1,\}' <<<"$out" | head -1)
kubectl sentinel approve "$RID" >/dev/null
agent -n shop rollout restart deploy/web >/dev/null 2>&1 || die "approved restart not admitted (kubectl stamps a new time on each attempt)"
ok "approved restart admitted, although kubectl stamped a new restartedAt"
out=$(agent -n shop rollout restart deploy/web 2>&1) && die "a second restart was admitted on one approval"
ok "the same restart again is denied: one approval, one execution"

out=$(agent -n shop scale deploy/web --replicas=0 2>&1) && die "scale to zero admitted"
if grep -q "replicas 0 is below the minimum 1" <<<"$out"; then ok "scale to zero is blocked by policy (cannot be approved)"; else die "scale to zero is blocked by policy (cannot be approved)"; fi

out=$(agent -n shop delete pod -l app=web --wait=false 2>&1) && die "agent deleted pods without approval"
if grep -q "needs human approval" <<<"$out"; then ok "agent pod deletion waits for approval"; else die "agent pod deletion waits for approval"; fi

out=$(kubectl -n $NS delete actionrequest "$ID" 2>&1) && die "the audit record was deleted"
if grep -q "cannot be deleted" <<<"$out"; then ok "even cluster-admin can't delete an ActionRequest"; else die "even cluster-admin can't delete an ActionRequest"; fi
out=$(kubectl -n $NS patch actionrequest "$ID" --type merge -p '{"spec":{"target":{"name":"payments"}}}' 2>&1) && die "a request was edited"
if grep -q "cannot be edited" <<<"$out"; then ok "what was requested can't be edited"; else die "what was requested can't be edited"; fi

if kubectl sentinel audit verify | tee "$WORK/verify.txt" | grep -q "Audit chain OK"; then ok "$(cat "$WORK/verify.txt")"; else die "$(cat "$WORK/verify.txt")"; fi

if [ "${LOAD:-0}" = 1 ]; then
  echo "Latency (through the API server; see evals/guard/latency.json)"
  mkdir -p evals/guard
  loadgen -admin-kubeconfig "$HOME/.kube/config" -agent-kubeconfig "$WORK/agent.kubeconfig" -n "${LOAD_N:-400}" > "$WORK/latency.json"
  python3 - "$WORK/latency.json" <<'EOF'
import json, sys, subprocess, datetime
d = json.load(open(sys.argv[1]))
d["measured_at"] = datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="seconds")
d["environment"] = "kind (1 node, Docker Desktop, Apple Silicon); 3 guard replicas"
open("evals/guard/latency.json", "w").write(json.dumps(d, indent=2) + "\n")
for k, v in d.items():
    if isinstance(v, dict): print(f"    {k:22s} n={v['n']:4d}  p50 {v['p50_ms']:6.1f} ms  p95 {v['p95_ms']:6.1f} ms  p99 {v['p99_ms']:6.1f} ms")
EOF
  ok "latency measured"
  if kubectl sentinel audit verify | grep -q "Audit chain OK"; then ok "audit still verifies after the load run: $(kubectl sentinel audit verify)"; else die "audit still verifies after the load run: $(kubectl sentinel audit verify)"; fi
fi

echo "Chaos: the guard is down"
kubectl -n $NS scale deploy/sentinel-guard --replicas=0 >/dev/null
kubectl -n $NS wait --for=delete pod -l app.kubernetes.io/name=sentinel-guard --timeout 90s >/dev/null 2>&1 || true
if kubectl -n shop scale deploy/web --replicas=2 >/dev/null; then ok "guard down: a human's change still goes through"; else die "guard down: a human's change still goes through"; fi
out=$(agent -n shop scale deploy/web --replicas=5 2>&1) && die "guard down: an agent's change was admitted (must fail closed)"
if grep -qi "failed calling webhook" <<<"$out"; then ok "guard down: the agent's change is refused (fail closed)"; else die "guard down: the agent's change is refused (fail closed)"; fi
T0=$(date +%s)
kubectl -n $NS scale deploy/sentinel-guard --replicas=3 >/dev/null
for _ in $(seq 1 120); do   # until the API server reaches a guard again (pods ready + endpoints published)
  out=$(agent -n shop scale deploy/web --replicas=5 2>&1) && die "admitted after recovery without approval"
  grep -q "needs human approval" <<<"$out" && break
  sleep 0.5
done
grep -q "needs human approval" <<<"$out" || die "the guard did not come back: $out"
ok "guard back: answering again $(( $(date +%s) - T0 ))s after scaling up from zero"
if kubectl sentinel audit verify | grep -q "Audit chain OK"; then ok "audit chain intact after the outage"; else die "audit chain intact after the outage"; fi

echo "All $PASS checks passed."
