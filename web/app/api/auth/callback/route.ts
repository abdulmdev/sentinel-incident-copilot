import { NextResponse } from "next/server";
import { cookies } from "next/headers";
import { exchangeCode, publicUrl } from "@/lib/oidc";
import { COOKIE, ID_HINT, REFRESH, TX, cookieOptions } from "@/lib/session";

export async function GET(req: Request) {
  const params = new URL(req.url).searchParams;
  const jar = await cookies();
  const tx = jar.get(TX)?.value;
  let expected: { state: string; verifier: string } | null = null;
  try { expected = tx ? JSON.parse(tx) : null; } catch { expected = null; }
  // State binds the response to the request this browser started (CSRF); PKCE binds the code to it.
  if (!expected || params.get("state") !== expected.state || !params.get("code")) {
    return NextResponse.json({ error: params.get("error_description") ?? "invalid login response" }, { status: 400 });
  }
  let tokens;
  try {
    tokens = await exchangeCode(params.get("code")!, expected.verifier);
  } catch (e) {
    return NextResponse.json({ error: (e as Error).message }, { status: 502 });
  }
  // Tokens came straight from the token endpoint (back channel), not via the browser; the gateway
  // verifies the access token's signature, issuer, audience and expiry on every call.
  // HTML redirect instead of a 302: the session cookie is SameSite=Strict, and a redirect chain that
  // began on the IdP's site wouldn't send it on the next request.
  const res = new NextResponse(`<!doctype html><meta http-equiv="refresh" content="0;url=${publicUrl()}/">`,
    { headers: { "Content-Type": "text/html" } });
  res.cookies.set(COOKIE, tokens.access_token, cookieOptions);
  if (tokens.refresh_token) res.cookies.set(REFRESH, tokens.refresh_token, cookieOptions);
  if (tokens.id_token) res.cookies.set(ID_HINT, tokens.id_token, cookieOptions);
  res.cookies.set(TX, "", { ...cookieOptions, maxAge: 0 });
  return res;
}
