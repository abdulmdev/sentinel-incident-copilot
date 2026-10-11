"use client";
import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { api, duration, when } from "@/lib/api";

type Row = { id: string; service: string; summary: string; severity: string; status: string;
             proposedAction: string | null; openedBy: string; reviewer: string | null; createdAt: string;
             source: string | null; timeToMitigateS: number | null };

export default function Incidents() {
  const [rows, setRows] = useState<Row[] | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [form, setForm] = useState({ service: "checkout", summary: "checkout 5xx rate above 20%", severity: "SEV1" });

  const load = useCallback(() => api<Row[]>("/api/v1/incidents").then(setRows).catch((e) => setError(e.message)), []);
  useEffect(() => { load(); const t = setInterval(load, 5000); return () => clearInterval(t); }, [load]);

  async function open() {
    setBusy(true); setError("");
    try {
      const view = await api<{ incident: { id: string } }>("/api/v1/incidents", { method: "POST", body: form });
      window.location.href = `/incidents/${view.incident.id}`;
    } catch (e) { setError((e as Error).message); setBusy(false); }
  }

  return (
    <>
      <h1>Incidents</h1>
      <div className="panel">
        <div className="row">
          <select aria-label="Service" value={form.service} onChange={(e) => setForm({ ...form, service: e.target.value })}>
            <option>checkout</option><option>inventory</option>
          </select>
          <input aria-label="Alert summary" value={form.summary} onChange={(e) => setForm({ ...form, summary: e.target.value })} />
          <select aria-label="Severity" value={form.severity} onChange={(e) => setForm({ ...form, severity: e.target.value })}>
            <option>SEV1</option><option>SEV2</option><option>SEV3</option>
          </select>
          <button onClick={open} disabled={busy}>{busy ? "Investigating…" : "Open incident"}</button>
        </div>
        {error && <p className="error">{error}</p>}
      </div>
      <div className="panel" style={{ overflowX: "auto" }}>
        {rows === null ? <p className="muted">Loading…</p> : rows.length === 0 ? <p className="muted">No incidents yet.</p> : (
          <table>
            <thead><tr><th>Opened</th><th>Service</th><th>Summary</th><th>Status</th><th>Proposed</th><th>Mitigated in</th><th>Source</th><th>By</th></tr></thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.id}>
                  <td className="muted">{when(r.createdAt)}</td>
                  <td>{r.service}</td>
                  <td><Link href={`/incidents/${r.id}`}>{r.summary}</Link> <span className="muted">{r.severity}</span></td>
                  <td><span className={`pill ${r.status}`}>{r.status.replaceAll("_", " ").toLowerCase()}</span></td>
                  <td className="mono">{r.proposedAction ?? "—"}</td>
                  <td className="mono">{duration(r.timeToMitigateS)}</td>
                  <td className="muted">{r.source === "alertmanager" ? "alert" : "manual"}</td>
                  <td className="muted">{r.reviewer ?? r.openedBy}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
