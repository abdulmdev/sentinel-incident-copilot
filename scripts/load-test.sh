#!/usr/bin/env bash
# Runs the k6 load test against the local stack (scripts/dev-local.sh up). Summary -> docs/load-test-results.json
set -euo pipefail
cd "$(dirname "$0")/.."
docker run --rm -v "$PWD/deploy/k6:/scripts" -v "$PWD/docs:/out" --add-host=host.docker.internal:host-gateway \
  grafana/k6 run --summary-export /out/load-test-results.json /scripts/load.js
