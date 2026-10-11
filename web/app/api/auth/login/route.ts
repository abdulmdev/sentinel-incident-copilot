import { NextResponse } from "next/server";
import { authorizeUrl, oidcEnabled } from "@/lib/oidc";
import { TX, cookieOptions } from "@/lib/session";

export async function GET() {
  if (!oidcEnabled()) return NextResponse.json({ error: "OIDC is not configured" }, { status: 404 });
  const { url, state, verifier } = await authorizeUrl();
  const res = NextResponse.redirect(url);
  // Lax, not Strict: it must survive the IdP's cross-site redirect back to /api/auth/callback.
  res.cookies.set(TX, JSON.stringify({ state, verifier }), { ...cookieOptions, sameSite: "lax", maxAge: 600 });
  return res;
}
