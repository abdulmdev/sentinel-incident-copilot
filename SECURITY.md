# Security policy

Sentinel can execute remediations against production, so security reports are welcome and taken seriously.

## Reporting a vulnerability
Please **don't open a public issue**. Report it privately through the repository's **Security tab → "Report a vulnerability"**. If that isn't available, open an issue asking for a private contact, without any details.

Include what an attacker could do, the affected component, and steps to reproduce. You'll get an acknowledgement, and a fix or mitigation plan for confirmed issues.

## Scope
Especially interesting:
- forging or replaying **capabilities**, or executing a remediation without approval;
- forging **service tokens** or confusing token issuers or algorithms;
- **cross-tenant** access: API, row-level security, agent checkpoints, caches;
- prompt injection that leads to a harmful action **after** guardrails;
- secrets reaching pods that shouldn't hold them.

The design and threat model are in [chapter 10 of the handbook](docs/handbook/10-security.md) and [ADR-0009](docs/adr/0009-zero-trust-identity.md).

## Not a vulnerability
The dev-only credentials in `deploy/keycloak/sentinel-realm.json`, `scripts/dev_token.py` and the kind values are intentionally public, and are disabled in production values. That's checked in CI by `scripts/check_chart_secrets.py --prod`.
