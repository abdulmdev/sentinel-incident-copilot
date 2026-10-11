#!/usr/bin/env bash
# End-to-end demo: break checkout with a bad deploy, open an incident, approve the fix.
set -euo pipefail
cd "$(dirname "$0")/.."
GW=${GW:-http://localhost:8080}
TOKEN=$(python3 scripts/dev_token.py alice@acme acme)

python3 chaos-lab/inject.py checkout "${1:-bad_deploy}"

echo "== Opening incident"
OPENED=$(curl -sf -X POST "$GW/api/v1/incidents" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"service":"checkout","summary":"checkout 5xx rate above 20%","severity":"SEV1"}')
echo "$OPENED" | python3 -m json.tool
ID=$(echo "$OPENED" | python3 -c 'import json,sys;print(json.load(sys.stdin)["incident"]["id"])')

echo "== Approving proposed remediation"
curl -sf -X POST "$GW/api/v1/incidents/$ID/decision" -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' -d '{"approved":true}' \
  | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d["status"]);print(d["postmortem"])'

python3 chaos-lab/inject.py checkout none >/dev/null
