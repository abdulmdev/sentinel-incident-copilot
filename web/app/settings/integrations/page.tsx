"use client";
import { useCallback, useEffect, useState } from "react";
import { api, when } from "@/lib/api";

type Row = { jti: string; kind: string; name: string; createdBy: string; createdAt: string; expiresAt: string;
             revokedAt: string | null; revokedBy: string | null; lastUsedAt: string | null };
type Issued = { jti: string; token: string; expiresAt: string };

export default function Integrations() {
  const [rows, setRows] = useState<Row[] | null>(null);
  const [error, setError] = useState("");
  const [name, setName] = useState("prod alertmanager");
  const [issued, setIssued] = useState<Issued | null>(null);

  const load = useCallback(() => api<Row[]>("/api/v1/integrations").then(setRows).catch((e) => setError(e.message)), []);
  useEffect(() => { load(); }, [load]);

  async function issue() {
    setError("");
    try {
      setIssued(await api<Issued>("/api/v1/integrations/alertmanager", { method: "POST", body: { name, ttlDays: 30 } }));
      load();
    } catch (e) { setError((e as Error).message); }
  }

  async function revoke(jti: string) {
    try { await api(`/api/v1/integrations/${jti}`, { method: "DELETE" }); load(); }
    catch (e) { setError((e as Error).message); }
  }

  const state = (r: Row) => r.revokedAt ? "revoked" : new Date(r.expiresAt) < new Date() ? "expired" : "active";

  return (
    <>
      <h1>Integrations</h1>
      <div className="panel">
        <h2 style={{ marginTop: 0 }}>Alertmanager token</h2>
        <p className="muted">Signed by the gateway (ES256), valid only on the alerts webhook, for this tenant. Only its
          id is stored; revoking takes effect on the next delivery.</p>
        <div className="row">
          <input aria-label="Token name" value={name} onChange={(e) => setName(e.target.value)} />
          <button onClick={issue}>Issue token</button>
        </div>
        {issued && (
          <div className="event mono" style={{ marginTop: 10, wordBreak: "break-all" }}>
            Copy it now; it won&apos;t be shown again (expires {when(issued.expiresAt)}):<br />{issued.token}
          </div>
        )}
        {error && <p className="error">{error}</p>}
      </div>
      <div className="panel" style={{ overflowX: "auto" }}>
        {rows === null ? <p className="muted">Loading…</p> : rows.length === 0 ? <p className="muted">No tokens issued.</p> : (
          <table>
            <thead><tr><th>Name</th><th>State</th><th>Issued by</th><th>Created</th><th>Expires</th><th>Last used</th><th /></tr></thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.jti}>
                  <td>{r.name} <span className="muted mono">{r.jti.slice(0, 8)}</span></td>
                  <td><span className={`pill ${state(r) === "active" ? "RESOLVED" : "FAILED"}`}>{state(r)}</span></td>
                  <td className="muted">{r.createdBy}</td>
                  <td className="muted">{when(r.createdAt)}</td>
                  <td className="muted">{when(r.expiresAt)}</td>
                  <td className="muted">{r.lastUsedAt ? when(r.lastUsedAt) : "never"}</td>
                  <td>{state(r) === "active" && <button className="danger" onClick={() => revoke(r.jti)}>Revoke</button>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    </>
  );
}
