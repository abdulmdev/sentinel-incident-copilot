# ADR-0016: Two-person approvals, verified approvers, and notifications

**Status:** accepted, 2026-10-25. Builds on [ADR-0010](0010-sentinel-guard.md) (approvals), [ADR-0011](0011-copilot-approvals-through-the-guard.md) (delegates) and [ADR-0015](0015-quickstart-inbox-and-opt-in-publishing.md) (the inbox).

## Context
Three gaps were left in human oversight:
- **One person could approve anything.** For the riskiest actions (closing a customer's account, deleting a namespace, moving money), teams want two people: separation of duties. The EU AI Act names it for one high-risk category (Art. 14(5)). Many internal controls ask for it more widely.
- **Outside Kubernetes, the guard didn't know who decided.** The inbox and the HTTP API used one shared admin token plus a typed name. A delegate's word was taken for who its user was. Two-person rules mean nothing if one person can type two names.
- **Nobody was told when a request was waiting.** Someone had to look, and unseen requests expired undecided.

## Decisions
1. **`approvals: N` per action** (default 1, at most 5).
   - **Nobody counts twice:** not the agent, not its delegate, not the same person. A denial by anyone is final.
   - **Each approval before the last is its own signed event** (`approval-added`). The last approval is the decision, as before, so the capability is issued only when N different people have approved.
   - **The capability carries all of them** (`approvers`), so an executor can check too.
   - **The required number is inside the request's digest** (only when above 1, so earlier records keep verifying). Lowering it after the fact breaks verification.
   - **The verifier checks the people:** every signed approval is listed; an approved request shows N distinct people in its signed record; nothing unsigned is listed once the request has left Pending.
2. **In Kubernetes, the approval webhook turns an early "approve" into an approval.**
   - People keep using `kubectl sentinel approve`, which sets `spec.decision`. While more approvals are needed, the mutating webhook moves it into `spec.approvals` as the authenticated user, and keeps the request pending.
   - It answers with a warning ("approval 1 of 2 …") and refuses the same person twice. Nobody can write `spec.approvals` directly.
   - The guard signs each approval within a second.
   - Defence in depth: a decision that arrives with too few approvals (written by an older guard, or straight into storage) is demoted to one approval, never applied.
3. **People decide as themselves, authenticated by the guard.**
   - **Personal approver tokens** (`SENTINEL_GUARD_APPROVER_TOKENS`, single-process mode). With them, no shared admin token is generated.
   - **OIDC tokens verified at the guard:**
     - from the issuer's discovery and JWKS;
     - RS/PS/ES algorithms only;
     - issuer, audience and expiry required; an unverified email refused;
     - the person must be in the policy's `approvers`, which the chart fills from the same list as its RBAC approvers.
   - When the guard authenticated the person, that's who decided, and a different name in the body is refused.
   - A **delegate can forward the person's OIDC token** (`X-Sentinel-Person-Token`). The guard then records the verified person *via* the delegate instead of trusting the delegate's word.
   - `GET /v1/whoami` tells the inbox who is signed in, so it never asks for a name except with the legacy admin token.
4. **Notifications by webhook** (`SENTINEL_GUARD_NOTIFY_URL`).
   - **When:** a request needs a person, an approval leaves it needing more, or a request expires with nobody deciding.
   - **Payload:** JSON with `text` (Slack), `content` (Discord) and structured fields, with a link to the inbox or the `kubectl sentinel approve` command.
   - **Delivery:** off the request path, from a bounded queue; two retries on errors and 5xx. Results are counted (`sentinel_guard_notifications_total`).
   - **In Kubernetes:** sent by the replica that recorded the event, so once per event.

## Consequences
- Two-person rules work the same way in Kubernetes (`kubectl`), over HTTP, in the inbox, and through a delegate, and they are visible in the evidence (`multiPersonApproved`, every approver among the deciders).
- The quickstart now runs with Alice and Bob as approvers and an account closure that needs both. A notification sink prints what Slack would receive.
- A shared admin token still works in single-process mode, for compatibility. The inbox labels it as one that can't tell who you are.
- **Not done:**
  - an OIDC sign-in flow inside the inbox (people paste a token);
  - requiring approvers from different groups;
  - delivery guarantees for notifications beyond retries, since the queue lives in memory.
