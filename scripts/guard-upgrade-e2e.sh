#!/usr/bin/env bash
# Upgrade test on a real API server: install the last published release from GHCR, record decisions,
# then upgrade to this checkout's chart and image, as a user would. Checks that:
#   - a guard on the old CRD stops with the exact fix (helm upgrade never updates CRDs);
#   - after applying the CRD, the old records are read on the v1 API and still verify;
#   - a request left pending across the upgrade can be approved and is admitted once.
#
#   scripts/guard-upgrade-e2e.sh            # FROM=2.0.0-alpha.3 by default; KEEP=1 keeps the cluster
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export PATH="$ROOT/.tools:$ROOT/.tools/go/bin:$PATH" GOPATH="$ROOT/.tools/gopath" GOFLAGS=-modcacherw
CLUSTER=sentinel-guard-upgrade
NS=sentinel-system
FROM=${FROM:-2.0.0-alpha.3}
WORK="$(mktemp -d)"
BIN="$WORK/bin"; mkdir -p "$BIN"; export PATH="$BIN:$PATH"
PASSED=0
ok() { PASSED=$((PASSED + 1)); echo "  ✓ $*"; }
die() { echo "  ✗ $*" >&2; exit 1; }
cleanup() {
  if [ "${KEEP:-0}" != 1 ]; then kind delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; fi
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "Building this checkout's guard image and kubectl plugin"
(cd guard && go build -trimpath -o "$BIN/kubectl-sentinel" ./cmd/kubectl-sentinel)
docker build -q -t sentinel/guard:upgrade --build-arg VERSION=upgrade guard >/dev/null
kind get clusters 2>/dev/null | grep -qx "$CLUSTER" || kind create cluster --name "$CLUSTER" --wait 120s >/dev/null
kubectl config use-context "kind-$CLUSTER" >/dev/null
kind load docker-image --name "$CLUSTER" sentinel/guard:upgrade >/dev/null

echo "Installing the published $FROM (chart and image from GHCR)"
helm install sentinel-guard oci://ghcr.io/abdulmdev/charts/sentinel-guard --version "$FROM" \
  -n $NS --create-namespace --wait --timeout 4m >/dev/null
ok "$FROM installed: $(kubectl -n $NS get deploy sentinel-guard -o jsonpath='{.status.readyReplicas}') replicas ready"

kubectl create namespace agents --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl create namespace shop --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n agents create serviceaccount fixer --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n shop create rolebinding fixer-edit --clusterrole=edit --serviceaccount=agents:fixer --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n shop create deployment web --image=registry.k8s.io/pause:3.10 --replicas=2 --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl -n shop rollout status deploy/web --timeout 2m >/dev/null
kubectl config view --raw --minify > "$WORK/agent.kubeconfig"
KUBECONFIG="$WORK/agent.kubeconfig" kubectl config set-credentials fixer --token="$(kubectl -n agents create token fixer --duration=1h)" >/dev/null
KUBECONFIG="$WORK/agent.kubeconfig" kubectl config set-context --current --user=fixer >/dev/null
agent() { KUBECONFIG="$WORK/agent.kubeconfig" kubectl "$@"; }
request_id() { grep -o 'ar-[0-9a-f]\{8\}-[0-9]\{1,\}' <<<"$1" | head -1; }
phase() { kubectl -n $NS get actionrequest "$1" -o jsonpath='{.status.phase}'; }
approve_old() { # with kubectl itself: the plugin from this checkout speaks v1, which the old CRD doesn't serve
  kubectl -n $NS patch actionrequest "$1" --type merge -p '{"spec":{"decision":"approve"}}' >/dev/null
  for _ in $(seq 1 30); do [ "$(phase "$1")" = Approved ] && return; sleep 0.5; done
  die "$1 was not approved by the old guard"
}

echo "Decisions recorded by $FROM"
out=$(agent -n shop scale deploy/web --replicas=3 2>&1) && die "admitted without approval"
DONE=$(request_id "$out"); approve_old "$DONE"
agent -n shop scale deploy/web --replicas=3 >/dev/null 2>&1 || die "the approved change was not admitted by $FROM"
out=$(agent -n shop scale deploy/web --replicas=4 2>&1) && die "admitted without approval"
WAITING=$(request_id "$out")
[ "$(phase "$DONE")" = Executed ] && [ "$(phase "$WAITING")" = Pending ] || die "unexpected phases before the upgrade"
ok "$FROM executed $DONE and holds $WAITING pending"

echo "Upgrading the chart without the new CRD (what helm upgrade alone does)"
helm upgrade sentinel-guard deploy/helm/sentinel-guard -n $NS \
  --set image.repository=sentinel/guard --set image.tag=upgrade --set image.pullPolicy=Never >/dev/null
for _ in $(seq 1 60); do
  logs=$(kubectl -n $NS logs -l app.kubernetes.io/name=sentinel-guard --tail=20 --prefix 2>/dev/null | grep -h "Helm doesn't upgrade CRDs" | head -1 || true)
  [ -n "$logs" ] && break; sleep 2
done
[ -n "$logs" ] || die "a guard on the old CRD should stop with the fix in its log"
ok "the new guard on the old CRD stops and says what to apply"

echo "Applying the CRD, as the upgrade notes say"
kubectl apply -f deploy/helm/sentinel-guard/crds/actionrequests.sentinel.dev.yaml >/dev/null
kubectl -n $NS rollout restart deploy/sentinel-guard >/dev/null
kubectl -n $NS rollout status deploy/sentinel-guard --timeout 3m >/dev/null || die "the guard did not become ready after the CRD"
served=$(kubectl get crd actionrequests.sentinel.dev -o jsonpath='{range .spec.versions[*]}{.name}={.storage} {end}')
[ "$served" = "v1=true v1alpha1=false " ] || die "CRD versions: $served"
ok "ready on the v1 API (v1 stored; v1alpha1 still served, deprecated)"

kubectl sentinel requests --all | grep -q "$DONE" && kubectl sentinel requests | grep -q "$WAITING" \
  || die "the records from $FROM should be listed through v1"
if kubectl sentinel audit verify | tee "$WORK/verify.txt" | grep -q "Audit chain OK"; then
  ok "the records from $FROM are read through v1 and verify: $(head -1 "$WORK/verify.txt")"
else die "$(cat "$WORK/verify.txt")"; fi
kubectl get actionrequests.v1alpha1.sentinel.dev -n $NS "$DONE" >/dev/null 2>&1 || die "v1alpha1 should still be served"
ok "old clients can still read the v1alpha1 API"

kubectl sentinel approve "$WAITING" | grep -q "Approved $WAITING" || die "approving the request left pending across the upgrade"
agent -n shop scale deploy/web --replicas=4 >/dev/null 2>&1 || die "the request approved after the upgrade was not admitted"
out=$(agent -n shop scale deploy/web --replicas=3 2>&1) && die "a different change rode on the approval"
ok "a request left pending across the upgrade is approved and admitted once"
kubectl sentinel audit verify | grep -q "Audit chain OK" || die "the chain should still verify after new events"
ok "the chain verifies with events from both versions"

echo "All $PASSED checks passed."
