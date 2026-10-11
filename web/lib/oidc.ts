// OIDC authorization code flow with PKCE, run by the BFF (server side). The browser only ever sees
// redirects; the code is exchanged for tokens server-to-server with the client secret.
import "server-only";
import { createHash, randomBytes } from "node:crypto";

type Discovery = { authorization_endpoint: string; token_endpoint: string; end_session_endpoint?: string };

const issuer = () => process.env.OIDC_ISSUER ?? "";
// When the console runs in a container, the IdP's public URL (in redirects and the token's iss) may
// not be reachable from the server: OIDC_INTERNAL_ISSUER is used for server-to-server calls.
const internal = () => process.env.OIDC_INTERNAL_ISSUER || issuer();
export const oidcEnabled = () => Boolean(issuer() && process.env.OIDC_CLIENT_ID);
export const publicUrl = () => (process.env.PUBLIC_URL ?? "http://localhost:3000").replace(/\/$/, "");
const redirectUri = () => `${publicUrl()}/api/auth/callback`;

let cached: { at: number; doc: Discovery } | null = null;
async function discovery(): Promise<Discovery> {
  if (cached && Date.now() - cached.at < 300_000) return cached.doc;
  const res = await fetch(`${internal()}/.well-known/openid-configuration`, { cache: "no-store" });
  if (!res.ok) throw new Error(`OIDC discovery failed: ${res.status}`);
  cached = { at: Date.now(), doc: await res.json() };
  return cached.doc;
}

/** Server-to-server URL for an endpoint the IdP advertises under its public origin. */
const backchannel = (url: string) => url.replace(issuer(), internal());

export async function authorizeUrl() {
  const state = randomBytes(16).toString("base64url");
  const verifier = randomBytes(32).toString("base64url");
  const challenge = createHash("sha256").update(verifier).digest("base64url");
  const url = new URL((await discovery()).authorization_endpoint);
  url.search = new URLSearchParams({
    response_type: "code", client_id: process.env.OIDC_CLIENT_ID!, redirect_uri: redirectUri(),
    scope: "openid profile email", state, code_challenge: challenge, code_challenge_method: "S256",
  }).toString();
  return { url: url.toString(), state, verifier };
}

type Tokens = { access_token: string; refresh_token?: string; id_token?: string; expires_in: number };

async function tokenRequest(params: Record<string, string>): Promise<Tokens> {
  const res = await fetch(backchannel((await discovery()).token_endpoint), {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams({ client_id: process.env.OIDC_CLIENT_ID!,
                                client_secret: process.env.OIDC_CLIENT_SECRET ?? "", ...params }),
    cache: "no-store",
  });
  if (!res.ok) throw new Error(`token endpoint: ${res.status} ${await res.text()}`);
  return res.json();
}

export const exchangeCode = (code: string, verifier: string) =>
  tokenRequest({ grant_type: "authorization_code", code, redirect_uri: redirectUri(), code_verifier: verifier });

export const refresh = (refreshToken: string) =>
  tokenRequest({ grant_type: "refresh_token", refresh_token: refreshToken });

export async function logoutUrl(idTokenHint?: string) {
  const end = (await discovery()).end_session_endpoint;
  if (!end) return `${publicUrl()}/login`;
  const url = new URL(end);
  url.search = new URLSearchParams({ client_id: process.env.OIDC_CLIENT_ID!,
    post_logout_redirect_uri: `${publicUrl()}/login`, ...(idTokenHint ? { id_token_hint: idTokenHint } : {}) }).toString();
  return url.toString();
}
