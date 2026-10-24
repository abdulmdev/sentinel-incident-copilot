#!/usr/bin/env bash
# Builds every image as sentinel/<name>:<tag>. Jars are compiled once on the host (or CI runner),
# then packaged; the embedding model is copied in so images are self-contained.
set -euo pipefail
cd "$(dirname "$0")/.."
TAG=${TAG:-dev}
MODEL=.models/all-MiniLM-L6-v2/model.onnx
[ -f "$MODEL" ] || { mkdir -p "$(dirname "$MODEL")"; curl -fL -o "$MODEL" \
  https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/onnx/model.onnx; }
for svc in gateway-service ingestion-service; do
  (cd $svc && mvn -q package -DskipTests) && cp "$MODEL" $svc/target/model.onnx
done
docker build -q -t sentinel/gateway:$TAG gateway-service
docker build -q -t sentinel/ingestion:$TAG ingestion-service
docker build -q -t sentinel/agent:$TAG agent-service
docker build -q -t sentinel/mcp-observability:$TAG mcp-tools/observability
docker build -q -t sentinel/mcp-remediation:$TAG mcp-tools/remediation
docker build -q -t sentinel/chaos-lab:$TAG chaos-lab/service
docker build -q -t sentinel/web:$TAG web
docker build -q -t sentinel/guard:$TAG guard
docker images --format '{{.Repository}}:{{.Tag}}\t{{.Size}}' | grep "^sentinel/.*:$TAG"
