"""Upload a tenant's runbooks and postmortems through the gateway (→ Kafka → ingestion).

  python scripts/seed_knowledge.py acme          # uploads knowledge/acme/*.md
"""
import json
import os
import pathlib
import subprocess
import sys
import urllib.request

ROOT = pathlib.Path(__file__).resolve().parent.parent
GATEWAY = os.getenv("GATEWAY_URL", "http://localhost:8080")


def main() -> None:
    tenant = sys.argv[1] if len(sys.argv) > 1 else "acme"
    token = subprocess.check_output([sys.executable, str(ROOT / "scripts/dev_token.py"), f"seed@{tenant}", tenant],
                                    text=True).strip()
    for path in sorted((ROOT / "knowledge" / tenant).glob("*.md")):
        content = path.read_text()
        title = next((l[2:].strip() for l in content.splitlines() if l.startswith("# ")), path.stem)
        body = json.dumps({"docId": path.stem, "source": f"knowledge/{tenant}/{path.name}",
                           "title": title, "content": content}).encode()
        req = urllib.request.Request(f"{GATEWAY}/api/v1/knowledge/documents", body, {
            "Content-Type": "application/json", "Authorization": f"Bearer {token}"})
        with urllib.request.urlopen(req) as r:
            print(f"{r.status} {path.stem}")


if __name__ == "__main__":
    main()
