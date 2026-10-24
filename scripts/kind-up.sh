#!/usr/bin/env bash
# Sentinel on a local Kubernetes cluster (kind), installed with the real Helm chart.
#   scripts/kind-up.sh          # cluster + images + deps + sentinel
#   scripts/kind-up.sh --down   # delete the cluster
set -euo pipefail
cd "$(dirname "$0")/.."
export PATH="$PWD/.tools:$PATH"
CLUSTER=sentinel; NS=sentinel

if [ "${1:-}" = "--down" ]; then kind delete cluster --name $CLUSTER; exit 0; fi

kind get clusters | grep -qx $CLUSTER || kind create cluster --config deploy/kind/cluster.yaml --wait 120s
docker image inspect sentinel/guard:dev >/dev/null 2>&1 || scripts/build-images.sh

echo "Loading our images into the node (no registry needed)"
# Third-party images (postgres, kafka, redis) are pulled by the node itself: kind can't import
# multi-platform images from Docker Desktop's store ("content digest ... not found").
for img in sentinel/gateway:dev sentinel/ingestion:dev sentinel/agent:dev sentinel/mcp-observability:dev \
           sentinel/mcp-remediation:dev sentinel/chaos-lab:dev sentinel/web:dev sentinel/guard:dev; do
  kind load docker-image --name $CLUSTER "$img" >/dev/null
done

kubectl create namespace $NS --dry-run=client -o yaml | kubectl apply -f - >/dev/null
echo "Installing dependencies (dev only: Postgres+pgvector, Kafka, Redis)"
helm upgrade --install sentinel-deps deploy/helm/sentinel-deps -n $NS --wait --timeout 5m >/dev/null
helm dependency build deploy/helm/sentinel >/dev/null   # Sentinel Guard (subchart)
helm status sentinel -n $NS >/dev/null 2>&1 && UPGRADE=1 || UPGRADE=0
echo "Installing Sentinel (migration hook Job runs first)"
helm upgrade --install sentinel deploy/helm/sentinel -n $NS -f deploy/helm/sentinel/values-kind.yaml \
  --wait --timeout 10m >/dev/null
if [ "$UPGRADE" = 1 ]; then
  # Same image tag (dev) on every rebuild: restart so pods pick up freshly loaded images.
  # (A fresh install, as in CI, has nothing stale to restart.)
  kubectl -n $NS rollout restart deploy -l app.kubernetes.io/instance=sentinel >/dev/null
  kubectl -n $NS rollout status deploy -l app.kubernetes.io/instance=sentinel --timeout 5m >/dev/null
fi
# Ready through the NodePort, 3 times in a row (terminating pods can still receive traffic briefly).
ok=0; for _ in $(seq 1 120); do
  if curl -sf -m 2 http://localhost:18080/actuator/health >/dev/null; then ok=$((ok+1)); [ $ok -ge 3 ] && break; else ok=0; fi
  sleep 1
done
kubectl -n $NS get pods
echo "Console: http://localhost:13000   API: http://localhost:18080"
