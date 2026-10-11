// Server-only session handling (backend-for-frontend).
// Tokens live in httpOnly cookies: browser JS never sees them, so XSS can't exfiltrate them.
// Two ways in: OIDC (authorization code + PKCE against the company IdP, the production path) and an
// optional dev login (SENTINEL_DEV_LOGIN=true) that mints HS256 tokens the gateway accepts only when
// its dev issuer is enabled.
import "server-only";
import { createHmac } from "node:crypto";
import { cookies } from "next/headers";

export const COOKIE = "sentinel_session";   // the access token
export const REFRESH = "sentinel_refresh";  // OIDC refresh token (OIDC sessions only)
export const ID_HINT = "sentinel_id_hint";  // OIDC id token, only as the logout hint
export const TX = "sentinel_oidc_tx";       // state + PKCE verifier of a login in progress
const DEV_TTL_SECONDS = 8 * 60 * 60;

export type Session = { user: string; tenant: string; roles: string[]; token: string; kind: "oidc" | "dev";
                        expired: boolean };

const b64url = (s: string | Buffer) => Buffer.from(s).toString("base64url");

export const devLoginEnabled = () =>
  process.env.SENTINEL_DEV_LOGIN === "true" || process.env.NODE_ENV !== "production";

/** Dev login: the same HS256 JWT as scripts/dev_token.py. Off in production unless explicitly enabled. */
export function mintDevToken(user: string, tenant: string, roles: string[] = []): string {
  if (!devLoginEnabled()) throw new Error("dev login is disabled");
  const secret = process.env.SENTINEL_JWT_SECRET ?? "dev-only-secret-change-me-0123456789abcdef";
  const now = Math.floor(Date.now() / 1000);
  const unsigned = `${b64url(JSON.stringify({ alg: "HS256", typ: "JWT" }))}.${b64url(
    JSON.stringify({ sub: user, tenant_id: tenant, roles, iat: now, exp: now + DEV_TTL_SECONDS }))}`;
  return `${unsigned}.${createHmac("sha256", secret).update(unsigned).digest("base64url")}`;
}

/** Display/session info from the cookie. Not a security check: the gateway verifies every token. */
export function describe(token: string): Omit<Session, "token"> | null {
  try {
    const c = JSON.parse(Buffer.from(token.split(".")[1], "base64url").toString());
    const roles: string[] = [...(c.roles ?? []), ...(c.realm_access?.roles ?? [])];
    return { user: c.preferred_username ?? c.email ?? c.sub, tenant: c.tenant_id, roles,
             kind: c.iss ? "oidc" : "dev", expired: c.exp * 1000 < Date.now() };
  } catch {
    return null;
  }
}

/** The current session; an expired OIDC access token still counts if a refresh token exists. */
export async function getSession(): Promise<Session | null> {
  const jar = await cookies();
  const token = jar.get(COOKIE)?.value;
  if (!token) return null;
  const info = describe(token);
  if (!info) return null;
  if (info.expired && !(info.kind === "oidc" && jar.get(REFRESH))) return null;
  return { ...info, token };
}

export const cookieOptions = {
  httpOnly: true,
  sameSite: "strict" as const,
  secure: process.env.NODE_ENV === "production" && process.env.SENTINEL_INSECURE_COOKIES !== "true",
  path: "/",
  maxAge: DEV_TTL_SECONDS,
};
