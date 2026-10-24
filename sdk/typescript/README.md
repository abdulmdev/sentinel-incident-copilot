# @sentinel-guard/sdk (TypeScript / Node)

Ask [Sentinel Guard](../../docs/guard/README.md) before your agent acts. Policy may allow the action at once; otherwise a human must approve it (or it is blocked). Once approved you get a **single-use capability** for exactly that action and its parameters; redeem it right before the side effect.

Zero dependencies (uses `fetch`); Node 20+.

Not on npm yet. Build it from this folder and install it from there:

```bash
npm ci && npm run build
```

```bash
npm install /path/to/sentinel-incident-copilot/sdk/typescript
```

It is one file (`src/index.ts`, no dependencies), so you can also vendor it.

```ts
import { Guard, ApprovalPending } from "@sentinel-guard/sdk";

const guard = new Guard({ url: "http://localhost:8080", token: "agent-secret" });   // or SENTINEL_GUARD_URL / _TOKEN

try {
  const cap = await guard.require("refund", "order-1042", { change: { amount: "42.00 EUR" }, intent: "charged twice", wait: 300 });
  await guard.spend(cap.token, "payments");   // redeem once, immediately before acting
  await issueRefund("order-1042", "42.00 EUR");
} catch (e) {
  if (e instanceof ApprovalPending) console.log(e.message);   // "... needs human approval (request ar-…)"
  throw e;
}
```

Or wrap a function so it runs only once its exact call is allowed:

```ts
const refund = guard.guarded("refund",
  { target: (id: string) => id, change: (_id: string, amount: string) => ({ amount }) },
  async (id: string, amount: string) => issueRefund(id, amount));
```

Errors: `ApprovalPending` (a human must decide), `ActionBlocked` (policy or a denial), `CapabilityRejected` (already used, expired, forged), `GuardUnavailable` (fail closed). **Any error means: don't act.**

The change encoding (`canonicalChange`: strings as they are, everything else as compact JSON with sorted keys) is identical to the Python SDK's. An approval matches a call from either SDK, and a shared golden value is tested in both.
