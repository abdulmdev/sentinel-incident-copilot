"""Get an Alertmanager integration token from the gateway (as a tenant admin) and write it to a file.

Integration tokens are ES256, signed by the gateway, recorded (by jti) and revocable, so they can't be
minted offline. This asks the API, like an admin in the console would. Dev: the admin is a dev user
token (needs the gateway's dev issuer). Used by the alertmanager-token container in compose.

  python scripts/integration_token.py http://localhost:8080 acme /tmp/integration.jwt
"""
import json
import sys
import time
import urllib.error
import urllib.request

from dev_token import user_token


def main() -> None:
    gateway, tenant, out = (sys.argv[1:] + ["http://localhost:8080", "acme", "integration.jwt"][len(sys.argv[1:]):])
    for _ in range(150):  # the gateway may still be starting (compose starts everything at once)
        try:
            urllib.request.urlopen(f"{gateway}/actuator/health", timeout=2).read()
            break
        except (urllib.error.URLError, OSError):
            time.sleep(2)
    admin = user_token(f"provisioner@{tenant}", tenant, ["tenant-admin"], ttl=120)
    req = urllib.request.Request(f"{gateway}/api/v1/integrations/alertmanager",
                                 json.dumps({"name": "alertmanager-dev", "ttlDays": 30}).encode(),
                                 {"Authorization": f"Bearer {admin}", "Content-Type": "application/json"})
    try:
        issued = json.load(urllib.request.urlopen(req, timeout=10))
    except urllib.error.HTTPError as e:
        sys.exit(f"gateway refused to issue the token: {e.code} {e.read().decode()[:300]}")
    with open(out, "w") as f:
        f.write(issued["token"])
    print(f"integration token {issued['jti']} for {tenant} → {out} (expires {issued['expiresAt']})")


if __name__ == "__main__":
    main()
