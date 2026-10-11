import { NextResponse } from "next/server";
import { cookies } from "next/headers";
import { logoutUrl, oidcEnabled, publicUrl } from "@/lib/oidc";
import { COOKIE, ID_HINT, REFRESH, cookieOptions } from "@/lib/session";

export async function GET() {
  const jar = await cookies();
  const hint = jar.get(ID_HINT)?.value;
  const wasOidc = Boolean(jar.get(REFRESH)?.value);
  // Ends the IdP session too, so "sign out" really signs out (not just this app).
  const target = wasOidc && oidcEnabled() ? await logoutUrl(hint) : `${publicUrl()}/login`;
  const res = NextResponse.redirect(target);
  for (const c of [COOKIE, REFRESH, ID_HINT]) res.cookies.set(c, "", { ...cookieOptions, maxAge: 0 });
  return res;
}
