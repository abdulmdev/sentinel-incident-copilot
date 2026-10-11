import type { Metadata } from "next";
import Link from "next/link";
import { getSession } from "@/lib/session";
import { SignOut } from "./signout";
import "./globals.css";

export const metadata: Metadata = { title: "Sentinel", description: "Agentic incident-response copilot" };

export default async function RootLayout({ children }: { children: React.ReactNode }) {
  const session = await getSession();
  return (
    <html lang="en">
      <body>
        <header className="top">
          <span className="brand">Sentinel</span>
          <nav>
            <Link href="/">Incidents</Link>
            <Link href="/ask">Ask</Link>
            <Link href="/knowledge">Knowledge</Link>
            {session?.roles.includes("tenant-admin") && <Link href="/settings/integrations">Integrations</Link>}
          </nav>
          <span className="who">
            {session ? <>{session.user} · tenant <b>{session.tenant}</b>{session.kind === "dev" && <span className="muted"> (dev)</span>} · <SignOut /></> : <Link href="/login">Sign in</Link>}
          </span>
        </header>
        <main>{children}</main>
      </body>
    </html>
  );
}
