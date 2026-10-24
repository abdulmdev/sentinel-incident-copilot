/**
 * Sentinel Guard SDK for TypeScript / Node.
 *
 *   const guard = new Guard({ url: "http://localhost:8080", token: "agent-secret" });
 *   const cap = await guard.require("refund", "order-1042", { change: { amount: "42.00 EUR" }, wait: 300 });
 *   await guard.spend(cap.token, "payments");   // right before acting: once, ever
 *   await issueRefund("order-1042", "42.00 EUR");
 *
 * Any thrown error means: don't act. ApprovalPending carries the request id to show a human.
 */

export class GuardError extends Error {}

/** The guard could not be reached or answered unexpectedly. Fail closed. */
export class GuardUnavailable extends GuardError {}

/** A human must approve first. Show `message` (it names the request) and try again later. */
export class ApprovalPending extends GuardError {
  readonly requestId: string;
  constructor(requestId: string, message: string) {
    super(message);
    this.requestId = requestId;
  }
}

/** Policy forbids the action, or a human denied it. */
export class ActionBlocked extends GuardError {
  readonly requestId: string | undefined;
  readonly reasons: string[];
  constructor(requestId: string | undefined, message: string, reasons: string[] = []) {
    super(message);
    this.requestId = requestId;
    this.reasons = reasons;
  }
}

/** The guard refused to redeem a capability (already used, expired, forged). */
export class CapabilityRejected extends GuardError {
  readonly status: number;
  constructor(status: number, message: string) {
    super(message);
    this.status = status;
  }
}

export type Verdict = "allow" | "pending" | "block";

export interface Decision {
  verdict: Verdict;
  message: string;
  request: { id?: string; phase?: string; decidedBy?: string; [k: string]: unknown };
  reasons: string[];
  capability?: string;
}

export interface Capability {
  token: string;
  requestId: string;
  approvedBy?: string;
}

export interface ProposeOptions {
  change?: Record<string, unknown>;
  intent?: string;
  namespace?: string;
  dryRun?: boolean;
}

export interface RequireOptions extends ProposeOptions {
  /** Seconds to wait for a human before throwing ApprovalPending (default 0). */
  wait?: number;
}

export interface GuardOptions {
  /** Default: SENTINEL_GUARD_URL, else http://localhost:8080 */
  url?: string;
  /** Default: SENTINEL_GUARD_TOKEN */
  token?: string;
  /** Request timeout in milliseconds (default 10 000). */
  timeoutMs?: number;
  /** Polling interval while waiting for approval, in milliseconds (default 2 000). */
  pollMs?: number;
}

const env = (name: string): string | undefined =>
  (globalThis as { process?: { env: Record<string, string | undefined> } }).process?.env[name];

function sortedJSON(v: unknown): string {
  if (Array.isArray(v)) return `[${v.map(sortedJSON).join(",")}]`;
  if (v && typeof v === "object") {
    return `{${Object.keys(v).sort().map((k) => `${JSON.stringify(k)}:${sortedJSON((v as Record<string, unknown>)[k])}`).join(",")}}`;
  }
  return JSON.stringify(v ?? null);
}

/**
 * The approved parameters as the guard binds them: strings as they are, anything else as compact JSON
 * with sorted keys. The Python SDK encodes identically, so an approval matches whichever SDK asks.
 */
export function canonicalChange(change: Record<string, unknown> = {}): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [k, v] of Object.entries(change)) out[k] = typeof v === "string" ? v : sortedJSON(v);
  return out;
}

export class Guard {
  readonly url: string;
  private readonly token: string | undefined;
  private readonly timeoutMs: number;
  private readonly pollMs: number;

  constructor(opts: GuardOptions = {}) {
    this.url = (opts.url ?? env("SENTINEL_GUARD_URL") ?? "http://localhost:8080").replace(/\/+$/, "");
    this.token = opts.token ?? env("SENTINEL_GUARD_TOKEN");
    this.timeoutMs = opts.timeoutMs ?? 10_000;
    this.pollMs = opts.pollMs ?? 2_000;
  }

  private async call(method: string, path: string, body?: unknown, auth = true): Promise<Response> {
    const headers: Record<string, string> = { "content-type": "application/json" };
    if (auth && this.token) headers.authorization = `Bearer ${this.token}`;
    try {
      return await fetch(`${this.url}${path}`, {
        method, headers, body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(this.timeoutMs),
      });
    } catch (e) {
      throw new GuardUnavailable(`Sentinel Guard unreachable: ${(e as Error).message}`);
    }
  }

  private static async decision(r: Response): Promise<Decision> {
    const body = (await r.json().catch(() => ({}))) as Record<string, unknown>;
    // 200 allow, 202 pending, 403 block are answers; anything else is the guard failing.
    if (![200, 202, 403].includes(r.status) || typeof body.verdict !== "string") {
      throw new GuardUnavailable(`Sentinel Guard: HTTP ${r.status} ${String(body.error ?? "")}`.trim());
    }
    return {
      verdict: body.verdict as Verdict, message: String(body.message ?? ""),
      request: (body.request as Decision["request"]) ?? {}, reasons: (body.reasons as string[]) ?? [],
      capability: body.capability as string | undefined,
    };
  }

  /** Proposes an action; the guard answers allow (with a capability), pending or block. */
  async propose(action: string, target: string, opts: ProposeOptions = {}): Promise<Decision> {
    const t: Record<string, string> = { name: target };
    if (opts.namespace) t.namespace = opts.namespace;
    return Guard.decision(await this.call("POST", "/v1/actions", {
      action, target: t, change: canonicalChange(opts.change), intent: (opts.intent ?? "").slice(0, 500),
      ...(opts.dryRun ? { dryRun: true } : {}),
    }));
  }

  /** The request's state; once approved, the capability (issued once, to the requesting agent). */
  async status(requestId: string): Promise<Decision> {
    return Guard.decision(await this.call("GET", `/v1/actions/${encodeURIComponent(requestId)}`));
  }

  /** Asks the guard and resolves to a capability, waiting up to `wait` seconds for a human. */
  async require(action: string, target: string, opts: RequireOptions = {}): Promise<Capability> {
    let d = await this.propose(action, target, opts);
    const deadline = Date.now() + (opts.wait ?? 0) * 1000;
    while (d.verdict === "pending" && Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, Math.min(this.pollMs, Math.max(0, deadline - Date.now()))));
      d = await this.status(d.request.id ?? "");
    }
    if (d.verdict === "allow" && d.capability) {
      return { token: d.capability, requestId: d.request.id ?? "", approvedBy: d.request.decidedBy };
    }
    if (d.verdict === "pending") throw new ApprovalPending(d.request.id ?? "", d.message);
    throw new ActionBlocked(d.request.id, d.message, d.reasons);
  }

  /** Redeems the capability (atomically, once) and resolves to its verified claims. */
  async spend(capability: string, executor = ""): Promise<Record<string, unknown>> {
    const r = await this.call("POST", "/v1/capabilities/spend", { capability, executor }, false);
    const body = (await r.json().catch(() => ({}))) as Record<string, unknown>;
    if (r.status === 200) return (body.claims as Record<string, unknown>) ?? {};
    if ([400, 403, 409].includes(r.status)) throw new CapabilityRejected(r.status, String(body.error ?? "capability rejected"));
    throw new GuardUnavailable(`Sentinel Guard: HTTP ${r.status} ${String(body.error ?? "")}`.trim());
  }

  /**
   * Wraps a function so it runs only once the guard allows exactly this call. `target` and `change`
   * are computed from the arguments; by default the approval is bound to all of them.
   */
  guarded<A extends unknown[], R>(
    action: string,
    spec: { target: (...args: A) => string; change?: (...args: A) => Record<string, unknown>; wait?: number; executor?: string },
    fn: (...args: A) => Promise<R> | R,
  ): (...args: A) => Promise<R> {
    return async (...args: A) => {
      const change = spec.change ? spec.change(...args) : Object.fromEntries(args.map((a, i) => [`arg${i}`, a]));
      const cap = await this.require(action, spec.target(...args), { change, wait: spec.wait });
      await this.spend(cap.token, spec.executor ?? (fn.name || action));
      return fn(...args);
    };
  }
}
