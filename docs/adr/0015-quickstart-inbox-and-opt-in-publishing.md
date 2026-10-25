# ADR-0015: A one-command quickstart, a built-in approvals inbox, and opt-in publishing

**Status:** accepted, 2026-10-25. Builds on [ADR-0010](0010-sentinel-guard.md) (the guard), [ADR-0012](0012-sdks-and-the-mcp-proxy.md) (the SDKs) and [ADR-0014](0014-governance-evidence-and-guardian-monitoring.md) (guardian monitoring).

## Context
The guard could be tried in three ways, and each asked something of a newcomer:
- `docker run` plus curl commands to approve;
- the Codespaces walkthrough, which is scripted and over in a minute;
- the Kubernetes e2e, which needs kind.

None of them showed what running it feels like: an agent working continuously while a person approves its requests as they arrive, with the guard's metrics and alerts beside it.

Two more gaps:
- **Approving in single-process mode meant crafting HTTP requests.** In Kubernetes people use `kubectl sentinel approve`. Outside it, there was no tool for a person at all.
- **The SDKs and chart were reachable only from GitHub:** `pip install` from a git URL, a tarball for npm. Publishing to PyPI, npm and Artifact Hub needs accounts the project's maintainer owns, so it can't be done from CI alone.

## Decisions
1. **`docker compose up` at the repository root is the quickstart.**
   - It builds the guard (memory mode, the walkthrough's policy) and starts a support-desk agent that works through tickets indefinitely:
     - receipts run on policy;
     - refunds wait for a person;
     - erasures are blocked;
     - now and then a spent capability is replayed and refused.
   - `--profile monitoring` adds Prometheus and Grafana. Prometheus loads the chart's own alert and recording rules (the same file, not a copy); Grafana shows a provisioned dashboard.
   - Tokens are fixed demo values, and every port binds to 127.0.0.1.
   - The copilot's full stack keeps its own file (`deploy/docker-compose.yml`). The guard is the product most people try first, so it gets the root.
2. **The guard serves a small approvals inbox, only in single-process mode.**
   - `GET /` redirects to `/inbox/`: a static page embedded in the binary, with no build step and no dependencies.
   - It calls the same HTTP API with the admin token, which stays in the tab's `sessionStorage`. It shows each request's change, intent, risk, the guard's own precondition observations and the policy version.
   - It approves or denies with a note, lists recent outcomes, verifies the chain, and downloads the evidence bundle.
   - Everything it renders comes from agents, so all of it is inserted as text. The page runs under `default-src 'none'; script-src 'self'; connect-src 'self'` (no inline script, nothing loaded from elsewhere) with `frame-ancestors 'none'`.
   - It is not served when there is no admin token, which is always the case in Kubernetes: there, people approve through the API server, so their identity is the one Kubernetes authenticated.
   - The approver's name is self-asserted, as for any admin-token decision. The inbox is for the single-process deployments where the admin token *is* the authority.
3. **Publishing is built, tested and switched off per channel.**
   - Every release attaches the Python wheel and sdist and the npm tarball, and signs the chart with cosign as it already signed the image.
   - PyPI and npm jobs run only when the repository variables `PUBLISH_PYPI` / `PUBLISH_NPM` are `true`, using trusted publishing (OIDC). No registry token is ever stored.
   - The chart's Artifact Hub metadata is pushed only when `ARTIFACTHUB_REPOSITORY_ID` is set.
   - The one-time steps on each registry are written down ([publishing](../guard/publishing.md)), so turning a channel on is a settings change, not a code change.
4. **A distroless image still gets a health check.** `sentinel-guard -healthcheck` probes its own `/readyz` and exits 0 or 1, so compose (and anything else without a shell or curl in the image) can wait for the guard.

## Consequences
- One command shows the whole story live. CI runs it (`examples/quickstart/check.py`, against `docker compose --profile monitoring up`), so the quickstart can't silently rot.
- The page is 18 KB (HTML, CSS and script); the guard binary grows by 136 KB with it (measured: stripped build, darwin/arm64). In Kubernetes mode no route is added.
- The inbox is deliberately minimal: no multi-user identity, no notifications. Teams that need those approve in Kubernetes, or through a delegate such as Sentinel's console, which authenticates people with OIDC.
- Until the maintainer opts in, `pip install sentinel-guard` and `npm install @sentinel-guard/sdk` don't resolve. The release assets and the git-URL install remain the way in.
