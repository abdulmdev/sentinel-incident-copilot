#!/usr/bin/env bash
# Fast local dev loop: infra in Docker (Postgres, Kafka), services as local processes.
# No image builds, so a restart takes seconds instead of minutes.
#
#   scripts/dev-local.sh up      # start everything (logs in .logs/)
#   scripts/dev-local.sh down    # stop the local services (leaves Docker infra running)
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
LOGS="$ROOT/.logs"
MODEL="$ROOT/.models/all-MiniLM-L6-v2/model.onnx"
MODEL_URL="https://huggingface.co/sentence-transformers/all-MiniLM-L6-v2/resolve/main/onnx/model.onnx"

wait_for() { # url, name
  for _ in $(seq 1 120); do curl -sf -m 2 "$1" >/dev/null && { echo "  ✓ $2"; return; }; sleep 1; done
  echo "  ✗ $2 did not come up; see $LOGS" >&2; exit 1
}

up() {
  mkdir -p "$LOGS" "$(dirname "$MODEL")"
  grep -qE '^\.(logs|models)/' "$ROOT/.gitignore" 2>/dev/null || printf '.logs/\n.models/\n' >> "$ROOT/.gitignore"
  if [ ! -f "$MODEL" ]; then
    echo "Downloading embedding model (~86 MB, once)..."
    curl -fL --retry 3 -o "$MODEL.part" "$MODEL_URL" && mv "$MODEL.part" "$MODEL"
  fi
  export SENTINEL_EMBEDDING_MODEL_URI="file:$MODEL"

  echo "Infra (Docker): postgres, kafka, redis, jaeger, keycloak (then prometheus, alertmanager, grafana)"
  (cd "$ROOT/deploy" && docker compose up -d --wait postgres kafka redis jaeger keycloak >/dev/null 2>&1)
  export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
  export MANAGEMENT_OPENTELEMETRY_TRACING_EXPORT_OTLP_ENDPOINT=http://localhost:4318/v1/traces

  echo "Building Java services"
  (cd "$ROOT/ingestion-service" && mvn -q package -DskipTests)
  (cd "$ROOT/gateway-service" && mvn -q package -DskipTests)

  echo "Starting services"
  (cd "$ROOT/chaos-lab/service" && [ -d .venv ] || uv venv -q -p 3.12 .venv) && \
    (cd "$ROOT/chaos-lab/service" && uv pip install -q -p .venv -r requirements.txt)
  (cd "$ROOT/chaos-lab/service" && SERVICE_NAME=inventory nohup .venv/bin/uvicorn main:app --port 8102 >"$LOGS/inventory.log" 2>&1 &)
  (cd "$ROOT/chaos-lab/service" && SERVICE_NAME=checkout DOWNSTREAM_URL=http://localhost:8102 \
    nohup .venv/bin/uvicorn main:app --port 8101 >"$LOGS/checkout.log" 2>&1 &)
  (cd "$ROOT/mcp-tools/observability" && OBSERVED_SERVICES=checkout=http://localhost:8101,inventory=http://localhost:8102 \
    nohup uv run python server.py >"$LOGS/mcp-observability.log" 2>&1 &)
  (cd "$ROOT/mcp-tools/remediation" && OPS_SERVICES=checkout=http://localhost:8101,inventory=http://localhost:8102 \
    SENTINEL_REPLAY_STORE=redis://localhost:6379/0 nohup uv run python server.py >"$LOGS/mcp-remediation.log" 2>&1 &)
  (cd "$ROOT/chaos-lab/service" && LOADGEN_TARGETS=http://localhost:8101 \
    nohup .venv/bin/python loadgen.py >"$LOGS/loadgen.log" 2>&1 &)
  (cd "$ROOT/ingestion-service" && nohup java -jar target/ingestion-service-0.1.0.jar >"$LOGS/ingestion.log" 2>&1 &)
  wait_for http://localhost:8090/actuator/health ingestion   # runs Flyway migrations first
  # Agent starts after migrations: its checkpoint schema is created by Flyway.
  (cd "$ROOT/agent-service" && \
    SENTINEL_CHECKPOINT_DSN="postgresql://sentinel_app:sentinel_app@localhost:5432/sentinel?options=-csearch_path%3Dlanggraph" \
    SENTINEL_MCP_OBSERVABILITY_URL=http://localhost:8200/mcp SENTINEL_MCP_KNOWLEDGE_URL=http://localhost:8080/mcp \
    SENTINEL_MCP_REMEDIATION_URL=http://localhost:8300/mcp nohup uv run uvicorn app.main:app --port 8000 >"$LOGS/agent.log" 2>&1 &)
  # Users: Keycloak (OIDC) or dev tokens. Service tokens: the gateway's own ES256 keys (ephemeral here).
  (cd "$ROOT/gateway-service" && SENTINEL_AGENT_URL=http://localhost:8000 SENTINEL_DEV_TOKENS=true \
    SENTINEL_OIDC_ISSUER=http://localhost:8180/realms/sentinel \
    nohup java -jar target/gateway-service-0.1.0.jar >"$LOGS/gateway.log" 2>&1 &)
  wait_for http://localhost:8101/healthz checkout
  wait_for http://localhost:8200/healthz mcp-observability
  wait_for http://localhost:8300/healthz mcp-remediation
  wait_for http://localhost:8000/healthz agent
  wait_for http://localhost:8080/actuator/health gateway
  # Alertmanager's token is issued by the gateway's API, so the alerting stack starts after it.
  (cd "$ROOT/deploy" && docker compose -p sentinel -f docker-compose.local-observability.yml up -d >/dev/null 2>&1)
  if [ -d "$ROOT/web/node_modules" ]; then   # console (BFF): build once with `cd web && npm run build`
    [ -d "$ROOT/web/.next" ] || (cd "$ROOT/web" && npm run build >/dev/null)
    (cd "$ROOT/web" && SENTINEL_DEV_LOGIN=true GATEWAY_URL=http://localhost:8080 PUBLIC_URL=http://localhost:3000 \
      OIDC_ISSUER=http://localhost:8180/realms/sentinel OIDC_CLIENT_ID=sentinel-console OIDC_CLIENT_SECRET=dev-console-secret \
      nohup npm run start >"$LOGS/web.log" 2>&1 &)
    wait_for http://localhost:3000/login console
  fi
  echo "Ready. Console: http://localhost:3000  Traces: http://localhost:16686  Dashboard: http://localhost:3001  Alerts: http://localhost:9093"
  echo "Sign in with Keycloak (http://localhost:8180, dev users in deploy/keycloak/sentinel-realm.json) or the dev login."
  echo "LLM provider: ${SENTINEL_LLM_PROVIDER:-none (rule baseline)}. Local model: SENTINEL_LLM_PROVIDER=ollama $0 up"
}

down() {
  pkill -f 'ingestion-service-0.1.0.jar' || true
  pkill -f 'gateway-service-0.1.0.jar' || true
  pkill -f 'uvicorn app.main:app' || true
  pkill -f 'uvicorn main:app' || true
  pkill -f 'python server.py' || true
  pkill -f 'python loadgen.py' || true
  pkill -f 'next start' || true
  pkill -f 'next-server' || true
  echo "Stopped local services. Infra still running: (cd deploy && docker compose stop)"
}

case "${1:-up}" in
  up) up ;;
  down) down ;;
  *) echo "usage: $0 up|down" >&2; exit 2 ;;
esac
