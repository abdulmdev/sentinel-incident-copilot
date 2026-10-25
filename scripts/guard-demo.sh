#!/usr/bin/env bash
# The Sentinel Guard demo. Runs in GitHub Codespaces (one click from the README) or locally.
#
#   scripts/guard-demo.sh              the guard walkthrough: ask, approve, execute once, export evidence,
#                                verify it offline, catch tampering (about a minute, no Docker)
#   scripts/guard-demo.sh kubernetes   the guard as an admission webhook on a kind cluster: an agent's
#                                kubectl scale waits for `kubectl sentinel approve` (needs Docker)
#   scripts/guard-demo.sh setup        build the guard and the kubectl plugin, install the Python SDK
#
# GUARD_PORT (default 8080) and METRICS_PORT (default 9090) choose the guard's ports.
set -euo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
DEMO=$ROOT/.demo
BIN=$DEMO/bin
export PATH="$BIN:$ROOT/.tools:$ROOT/.tools/go/bin:$PATH"
[ -d "$ROOT/.tools/go" ] && export GOPATH="$ROOT/.tools/gopath" GOFLAGS=-modcacherw
GUARD_PORT=${GUARD_PORT:-8080}
METRICS_PORT=${METRICS_PORT:-9090}

setup() {
  mkdir -p "$BIN"
  echo "Building sentinel-guard and kubectl-sentinel"
  (cd guard && go build -trimpath -ldflags "-X main.version=demo" -o "$BIN/sentinel-guard" ./cmd/sentinel-guard \
    && go build -trimpath -ldflags "-X main.version=demo" -o "$BIN/kubectl-sentinel" ./cmd/kubectl-sentinel)
  echo "Installing the Python SDK"
  if command -v uv >/dev/null; then
    [ -x "$DEMO/venv/bin/python" ] || uv venv -q --python 3.12 "$DEMO/venv"
    uv pip install -q --python "$DEMO/venv/bin/python" -e sdk/python
  else
    [ -x "$DEMO/venv/bin/python" ] || python3 -m venv "$DEMO/venv"
    "$DEMO/venv/bin/pip" install -q -e sdk/python
  fi
}

walkthrough() {
  [ -x "$BIN/sentinel-guard" ] && [ -x "$DEMO/venv/bin/python" ] || setup
  local agent_token approver_token log=$DEMO/guard.log
  agent_token=demo-agent-$RANDOM$RANDOM
  approver_token=demo-approver-$RANDOM$RANDOM
  if curl -sf "http://localhost:$GUARD_PORT/healthz" >/dev/null 2>&1; then
    echo "Port $GUARD_PORT is taken; set GUARD_PORT (and METRICS_PORT) to free ports." >&2
    exit 1
  fi
  SENTINEL_GUARD_MODE=memory SENTINEL_GUARD_POLICY=examples/walkthrough/policy.yaml \
    SENTINEL_GUARD_ADDR=":$GUARD_PORT" SENTINEL_GUARD_METRICS_ADDR=":$METRICS_PORT" \
    SENTINEL_GUARD_AGENT_TOKENS="refund-bot=$agent_token" SENTINEL_GUARD_ADMIN_TOKEN="$approver_token" \
    "$BIN/sentinel-guard" >"$log" 2>&1 &
  GUARD_PID=$!
  trap 'kill $GUARD_PID 2>/dev/null || true' EXIT
  for _ in $(seq 1 50); do
    curl -sf "http://localhost:$GUARD_PORT/readyz" >/dev/null 2>&1 && break
    sleep 0.1
  done
  curl -sf "http://localhost:$GUARD_PORT/readyz" >/dev/null || { cat "$log" >&2; exit 1; }
  SENTINEL_GUARD_URL="http://localhost:$GUARD_PORT" SENTINEL_GUARD_METRICS_URL="http://localhost:$METRICS_PORT/metrics" \
    AGENT_TOKEN=$agent_token APPROVER_TOKEN=$approver_token KUBECTL_SENTINEL="$BIN/kubectl-sentinel" \
    "$DEMO/venv/bin/python" examples/walkthrough/walkthrough.py --out .demo
  echo "The guard's structured audit log is in .demo/guard.log; the evidence files are in .demo/."
}

kubernetes() {
  [ -x "$BIN/kubectl-sentinel" ] || setup
  for tool in docker kind kubectl helm; do
    command -v $tool >/dev/null || { echo "The Kubernetes demo needs $tool." >&2; exit 1; }
  done
  echo "A kind cluster with Sentinel Guard installed by its Helm chart, then an agent's changes checked live."
  echo "The cluster is kept afterwards: try \`kubectl sentinel requests --all\`; delete it with \`kind delete cluster --name sentinel-guard-e2e\`."
  KEEP=1 scripts/guard-e2e.sh
}

welcome() {
  cat <<'MSG'

  Sentinel Guard: no AI agent changes production unless policy or a person approved that exact
  change, once, with a signed record anyone can verify.

    scripts/guard-demo.sh              the walkthrough: about a minute, in this terminal
    scripts/guard-demo.sh kubernetes   an agent's kubectl changes held for approval on a kind cluster (~4 min)

  Read along: examples/walkthrough/README.md

MSG
}

case "${1:-walkthrough}" in
  setup) setup ;;
  walkthrough) walkthrough ;;
  kubernetes|k8s) kubernetes ;;
  welcome) welcome ;;
  *) echo "usage: scripts/guard-demo.sh [walkthrough|kubernetes|setup|welcome]" >&2; exit 2 ;;
esac
