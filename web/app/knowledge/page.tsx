"use client";
import { useState } from "react";
import { api } from "@/lib/api";

type Hit = { chunkId: number; docId: string; title: string; heading: string; content: string; score: number };
const MODES = ["hybrid", "vector", "keyword"] as const;

export default function Knowledge() {
  const [q, setQ] = useState("pods keep getting OOMKilled");
  const [hits, setHits] = useState<Record<string, Hit[]>>({});
  const [error, setError] = useState("");
  async function search() {
    setError("");
    try {
      const results = await Promise.all(MODES.map((m) => api<Hit[]>(`/api/v1/knowledge/search?q=${encodeURIComponent(q)}&mode=${m}&k=4`)));
      setHits(Object.fromEntries(MODES.map((m, i) => [m, results[i]])));
    } catch (e) { setError((e as Error).message); }
  }
  return (
    <>
      <h1>Knowledge search</h1>
      <div className="panel">
        <div className="row">
          <input aria-label="Search" value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={(e) => e.key === "Enter" && search()} />
          <button onClick={search}>Search</button>
        </div>
        <p className="muted">Side by side: dense vectors, keyword full-text, and their reciprocal-rank fusion.</p>
      </div>
      {error && <p className="error">{error}</p>}
      <div className="grid2" style={{ gridTemplateColumns: "repeat(auto-fit, minmax(280px, 1fr))" }}>
        {MODES.filter((m) => hits[m]).map((m) => (
          <div key={m} className="panel">
            <h2 style={{ marginTop: 0 }}>{m}</h2>
            {hits[m].length === 0 && <p className="muted">No matches.</p>}
            {hits[m].map((h, i) => (
              <div key={h.chunkId} style={{ marginBottom: 10 }}>
                <b>{i + 1}. {h.heading}</b><div className="mono muted">{h.docId} · {h.score.toFixed(4)}</div>
              </div>
            ))}
          </div>
        ))}
      </div>
    </>
  );
}
