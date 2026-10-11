// BFF proxy: the browser calls /api/proxy/api/v1/..., and the server attaches the token from the
// httpOnly cookie. Only the public API is reachable (/api/v1/*); /mcp, the alerts webhook and actuator
// are not. An expired OIDC access token is refreshed here, transparently.
import { NextResponse } from "next/server";
import { cookies } from "next/headers";
import { refresh } from "@/lib/oidc";
import { COOKIE, REFRESH, cookieOptions, getSession } from "@/lib/session";

const GATEWAY = process.env.GATEWAY_URL ?? "http://localhost:8080";
const ALLOWED = /^api\/v1\/(incidents|ask|knowledge|integrations)(\/|$)/;

async function forward(req: Request, ctx: { params: Promise<{ path: string[] }> }) {
  const path = (await ctx.params).path.join("/");
  if (!ALLOWED.test(path) || path.includes("..")) {
    return NextResponse.json({ error: "not found" }, { status: 404 });
  }
  const session = await getSession();
  if (!session) return NextResponse.json({ error: "unauthenticated" }, { status: 401 });

  let token = session.token;
  let renewed: { access_token: string; refresh_token?: string } | null = null;
  if (session.expired) {
    try {
      renewed = await refresh((await cookies()).get(REFRESH)!.value);
      token = renewed.access_token;
    } catch {
      return NextResponse.json({ error: "session expired" }, { status: 401 });
    }
  }

  const url = `${GATEWAY}/${path}${new URL(req.url).search}`;
  const init: RequestInit = {
    method: req.method,
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: req.method === "GET" || req.method === "DELETE" ? undefined : await req.text(),
    cache: "no-store",
  };
  let res: NextResponse;
  try {
    const upstream = await fetch(url, init);
    res = new NextResponse(upstream.status === 204 ? null : await upstream.text(), {
      status: upstream.status,
      headers: { "Content-Type": upstream.headers.get("Content-Type") ?? "application/json" },
    });
  } catch {
    res = NextResponse.json({ error: "gateway unavailable" }, { status: 502 });
  }
  if (renewed) {
    res.cookies.set(COOKIE, renewed.access_token, cookieOptions);
    if (renewed.refresh_token) res.cookies.set(REFRESH, renewed.refresh_token, cookieOptions); // rotated
  }
  return res;
}

export { forward as GET, forward as POST, forward as DELETE };
