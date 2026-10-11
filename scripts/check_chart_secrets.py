"""Least-privilege check on the rendered Helm chart: which pod may hold which secret.

  helm template t deploy/helm/sentinel [-f overlay] | python3 scripts/check_chart_secrets.py [--prod]

Always: the agent and the MCP servers hold no signing material (a compromised agent must not be able
to mint a capability). With --prod: no pod holds the dev HS256 secret, and only the gateway holds the
signing keys.
"""
import re
import sys

SIGNING = {"SENTINEL_SIGNING_KEYS", "SENTINEL_JWT_SECRET"}


def env_by_deployment(text: str) -> dict[str, set[str]]:
    out = {}
    for doc in text.split("\n---"):
        m = re.search(r"kind: Deployment\n(?:.*\n)*?  name: (\S+)\n", doc)
        if m:
            name = re.sub(r"^[a-z0-9]+-", "", m.group(1), count=1)  # drop the release prefix
            out[name] = set(re.findall(r"- name: ([A-Z_]+)\n\s+valueFrom: \{ secretKeyRef", doc))
    return out


def main() -> None:
    prod = "--prod" in sys.argv
    env = env_by_deployment(sys.stdin.read())
    problems = []
    for pod in ("agent", "mcp-observability", "mcp-remediation", "checkout", "inventory", "ingestion"):
        if env.get(pod, set()) & SIGNING:
            problems.append(f"{pod} holds signing material: {sorted(env[pod] & SIGNING)}")
    if env.get("mcp-remediation"):
        problems.append(f"mcp-remediation should hold no secrets, has {sorted(env['mcp-remediation'])}")
    if prod:
        for pod, names in env.items():
            if "SENTINEL_JWT_SECRET" in names:
                problems.append(f"{pod} holds the dev HS256 secret in production")
            if "SENTINEL_SIGNING_KEYS" in names and pod != "gateway":
                problems.append(f"{pod} holds the gateway's signing keys")
        if "SENTINEL_SIGNING_KEYS" not in env.get("gateway", set()):
            problems.append("gateway has no signing keys")
    if not env:
        problems.append("no deployments found in the input")
    for p in problems:
        print(f"LEAST-PRIVILEGE: {p}")
    if problems:
        sys.exit(1)
    print("ok: " + ", ".join(f"{k}={sorted(v) or '-'}" for k, v in sorted(env.items())))


if __name__ == "__main__":
    main()
