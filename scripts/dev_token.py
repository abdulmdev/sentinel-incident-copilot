"""Mint an HS256 dev *user* JWT for the gateway. Local development only.

Accepted only while the gateway's dev issuer is enabled (SENTINEL_DEV_TOKENS=true); production
users come from OIDC. Service tokens (tool, capability, integration, agent) can't be made here:
only the gateway's private key signs those.

  python scripts/dev_token.py alice@acme acme                       # user, 1 hour
  python scripts/dev_token.py alice@acme acme --roles tenant-admin  # tenant admin
"""
from __future__ import annotations

import base64
import hashlib
import hmac
import json
import os
import sys
import time


def b64(data: bytes) -> bytes:
    return base64.urlsafe_b64encode(data).rstrip(b"=")


def sign(claims: dict) -> str:
    secret = os.getenv("SENTINEL_JWT_SECRET", "dev-only-secret-change-me-0123456789abcdef").encode()
    header = b64(json.dumps({"alg": "HS256", "typ": "JWT"}).encode())
    body = b64(json.dumps(claims).encode())
    sig = b64(hmac.new(secret, header + b"." + body, hashlib.sha256).digest())
    return (header + b"." + body + b"." + sig).decode()


def user_token(sub: str, tenant: str, roles: list[str] | None = None, ttl: int = 3600) -> str:
    now = int(time.time())
    return sign({"sub": sub, "tenant_id": tenant, "roles": roles or [], "iat": now, "exp": now + ttl})


def main() -> None:
    args = sys.argv[1:]
    roles: list[str] = []
    if "--roles" in args:
        i = args.index("--roles")
        roles = args[i + 1].split(",")
        del args[i:i + 2]
    sub, tenant = (args[:2] + ["alice@acme", "acme"][len(args[:2]):])
    print(user_token(sub, tenant, roles))


if __name__ == "__main__":
    main()
