import { NextResponse } from "next/server";
import { COOKIE, cookieOptions, devLoginEnabled, mintDevToken } from "@/lib/session";

const USER = /^[a-z0-9._-]+@[a-z0-9-]+$/;
const DEV_ADMINS = new Set(["alice@acme", "carol@globex"]); // mirrors the dev Keycloak realm

/** Dev login only (SENTINEL_DEV_LOGIN=true). Production signs in through /api/auth/login (OIDC). */
export async function POST(req: Request) {
  if (!devLoginEnabled()) return NextResponse.json({ error: "dev login is disabled" }, { status: 404 });
  const { user } = (await req.json()) as { user?: string };
  if (!user || !USER.test(user)) {
    return NextResponse.json({ error: "user must look like name@tenant" }, { status: 400 });
  }
  const tenant = user.split("@")[1];
  const res = NextResponse.json({ user, tenant });
  res.cookies.set(COOKIE, mintDevToken(user, tenant, DEV_ADMINS.has(user) ? ["tenant-admin"] : []), cookieOptions);
  return res;
}

export async function DELETE() {
  const res = NextResponse.json({ ok: true });
  res.cookies.set(COOKIE, "", { ...cookieOptions, maxAge: 0 });
  return res;
}
