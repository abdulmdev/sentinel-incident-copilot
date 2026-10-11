"use client";
import { useState } from "react";
import { api } from "@/lib/api";

type Answer = { answer: string; cached: boolean;
                citations: { n: number; docId: string; title: string; heading: string; source: string }[] };

export default function Ask() {
  const [q, setQ] = useState("DB connections are timing out. What should I do?");
  const [a, setA] = useState<Answer | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  async function ask() {
    setBusy(true); setError(""); setA(null);
    const started = performance.now();
    try { const res = await api<Answer>("/api/v1/ask", { method: "POST", body: { question: q } });
          setA({ ...res, answer: res.answer + `\n\n(${((performance.now() - started) / 1000).toFixed(1)}s)` }); }
    catch (e) { setError((e as Error).message); }
    setBusy(false);
  }
  return (
    <>
      <h1>Ask the on-call assistant</h1>
      <div className="panel">
        <div className="row">
          <input aria-label="Question" value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === "Enter" && ask()} />
          <button onClick={ask} disabled={busy}>{busy ? "Thinking…" : "Ask"}</button>
        </div>
        <p className="muted">Answers are grounded in your team&apos;s runbooks and postmortems, cite them as [n], and
          repeated questions are served from a tenant-scoped semantic cache.</p>
      </div>
      {error && <p className="error">{error}</p>}
      {a && (
        <div className="panel">
          {a.cached && <p><span className="pill RESOLVED">cached answer</span></p>}
          <pre style={{ fontFamily: "inherit", fontSize: 15 }}>{a.answer}</pre>
          <h2>Sources</h2>
          {a.citations.map((c) => <div key={c.n}><span className="cite">[{c.n}]</span>{c.heading} <span className="muted mono">{c.docId}</span></div>)}
        </div>
      )}
    </>
  );
}
