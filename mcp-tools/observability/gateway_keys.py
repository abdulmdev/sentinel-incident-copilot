"""Verifies ES256 tokens minted by the Sentinel gateway, against its published JWKS.

No shared secret: this service can check the gateway's tokens but can't create one. Only ES256 is
accepted (no HS/none confusion); issuer, audience, expiry and (optionally) a scope are required.
Keys are cached and refetched on an unknown kid, so gateway key rotation needs no coordination.
"""
from __future__ import annotations

import os

import jwt


class GatewayKeys:
    def __init__(self, issuer: str | None = None, jwks_url: str | None = None, jwks: dict | None = None):
        self.issuer = issuer or os.getenv("SENTINEL_ISSUER", "http://localhost:8080")
        self.jwks_url = jwks_url or os.getenv("SENTINEL_JWKS_URL") or f"{self.issuer}/.well-known/jwks.json"
        self._static = {k.key_id: k for k in jwt.PyJWKSet.from_dict(jwks).keys} if jwks else None
        self._client = None if jwks else jwt.PyJWKClient(self.jwks_url, cache_keys=True, lifespan=300, timeout=3)

    def _key(self, kid: str | None):
        if self._static is not None:
            if kid not in self._static:
                raise jwt.InvalidTokenError(f"unknown signing key {kid!r}")
            return self._static[kid].key
        try:
            return self._client.get_signing_key(kid).key  # refetches the set once on an unknown kid
        except jwt.PyJWKClientError as e:
            raise jwt.InvalidTokenError(f"cannot resolve signing key {kid!r}: {e}") from e

    def verify(self, token: str, audience: str, scope: str | None = None) -> dict:
        header = jwt.get_unverified_header(token)
        if header.get("alg") != "ES256":
            raise jwt.InvalidAlgorithmError(f"only ES256 gateway tokens are accepted, got {header.get('alg')!r}")
        claims = jwt.decode(token, self._key(header.get("kid")), algorithms=["ES256"], audience=audience,
                            issuer=self.issuer, options={"require": ["exp", "iat", "iss", "aud"]})
        if scope and scope not in str(claims.get("scope", "")).split():
            raise jwt.InvalidTokenError(f"token lacks {scope}")
        return claims
