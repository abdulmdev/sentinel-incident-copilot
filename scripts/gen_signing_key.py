"""Generate the gateway's ES256 signing key set (JWK set JSON with private keys) for SENTINEL_SIGNING_KEYS.

  uv run --with cryptography scripts/gen_signing_key.py 2026-10                      # new set, one key
  uv run --with cryptography scripts/gen_signing_key.py 2027-01 --add current.json   # rotation: prepend a key

Rotation: publish the new key first (add it; the gateway signs with the first key unless
SENTINEL_SIGNING_ACTIVE_KID pins the old one), roll out, then drop the old key once every token it
signed has expired (integration tokens: up to 90 days). Store the output in your secret manager,
never in Git.
"""
import base64
import json
import sys

from cryptography.hazmat.primitives.asymmetric import ec


def b64(n: int) -> str:
    return base64.urlsafe_b64encode(n.to_bytes(32, "big")).rstrip(b"=").decode()


def new_key(kid: str) -> dict:
    private = ec.generate_private_key(ec.SECP256R1())
    numbers = private.private_numbers()
    return {"kty": "EC", "crv": "P-256", "kid": kid, "use": "sig", "alg": "ES256",
            "x": b64(numbers.public_numbers.x), "y": b64(numbers.public_numbers.y), "d": b64(numbers.private_value)}


def main() -> None:
    args = sys.argv[1:]
    if not args:
        sys.exit(__doc__)
    keys = [new_key(args[0])]
    if "--add" in args:
        with open(args[args.index("--add") + 1]) as f:
            keys += json.load(f)["keys"]
    print(json.dumps({"keys": keys}))


if __name__ == "__main__":
    main()
