"use client";
import { useState } from "react";

const PRESETS = ["alice@acme", "bob@acme", "carol@globex"];

export function DevLogin({ primary }: { primary: boolean }) {
  const [user, setUser] = useState(PRESETS[0]);
  const [error, setError] = useState("");
  async function signIn(u: string) {
    const res = await fetch("/api/session", { method: "POST", body: JSON.stringify({ user: u }) });
    if (res.ok) window.location.href = "/";
    else setError((await res.json()).error);
  }
  return (
    <div style={{ marginTop: primary ? 0 : 18 }}>
      {!primary && <h2>Dev sign-in</h2>}
      <p className="muted">Development only: the console mints an HS256 token, accepted only while the gateway&apos;s
        dev issuer is enabled.</p>
      <div className="row">
        <input aria-label="User" value={user} onChange={(e) => setUser(e.target.value)} />
        <button onClick={() => signIn(user)}>Sign in (dev)</button>
      </div>
      <p className="muted">Try: {PRESETS.map((p) => <a key={p} href="#" onClick={(e) => { e.preventDefault(); signIn(p); }} style={{ marginRight: 10 }}>{p}</a>)}</p>
      {error && <p className="error">{error}</p>}
    </div>
  );
}
