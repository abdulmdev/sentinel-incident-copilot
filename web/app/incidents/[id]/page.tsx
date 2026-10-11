"use client";
import { use, useEffect, useState } from "react";
import { api, duration } from "@/lib/api";

type Cause = { cause: string; hypothesis: string; confidence: number; citations: string[] };
type View = {
  status: string;
  incident: { id: string; service: string; summary: string; severity: string; opened_at: string };
  triage?: { category: string; rationale: string };
  rca?: { root_causes: Cause[]; remediation: { action: string; target: string; reason: string; risk: string } };
  evidence?: { source: string; detail: string }[];
  budget?: { tool_calls: number; cost_usd: number; stop_reason: string | null };
  guardrail_events?: Record<string, unknown>[];
  postmortem?: string;
  execution?: { executed: boolean; action?: string; service?: string; error?: string; result?: Record<string, unknown> };
  verification?: { healthy: boolean; attempts: { success_rate?: number; p95_ms?: number; error?: string }[] };
  timeline?: { proposed_at?: string; decided_at?: string; executed_at?: string; verified_at?: string;
               time_to_mitigate_s?: number | null };
};

const STEPS = [["proposed_at", "Proposal ready"], ["decided_at", "Human decision"],
               ["executed_at", "Remediation executed"], ["verified_at", "Recovery verified"]] as const;

export default function Incident({ params }: { params: Promise<{ id: string }> }) {
  const { id } = use(params);
  const [view, setView] = useState<View | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => { api<View>(`/api/v1/incidents/${id}`).then(setView).catch((e) => setError(e.message)); }, [id]);

  async function decide(approved: boolean) {
    setBusy(true);
    try { setView(await api<View>(`/api/v1/incidents/${id}/decision`, { method: "POST", body: { approved } })); }
    catch (e) { setError((e as Error).message); }
    setBusy(false);
  }

  if (error) return <p className="error">{error}</p>;
  if (!view) return <p className="muted">Loading…</p>;
  const { incident, rca, budget, execution, verification, timeline } = view;
  const opened = new Date(incident.opened_at).getTime();
  const since = (iso?: string) => (iso ? duration((new Date(iso).getTime() - opened) / 1000) : "");
  const cited = new Set(rca?.root_causes.flatMap((c) => c.citations));

  return (
    <>
      <h1>{incident.summary} <span className={`pill ${view.status}`}>{view.status.replaceAll("_", " ").toLowerCase()}</span></h1>
      <p className="muted">{incident.service} · {incident.severity} · opened {new Date(incident.opened_at).toLocaleString()}
        {view.triage && <> · triage: <b>{view.triage.category}</b></>}
        {budget && <> · {budget.tool_calls} tool calls · ${budget.cost_usd.toFixed(4)} · stopped: {budget.stop_reason}</>}</p>

      {rca && (
        <div className="panel">
          <h2 style={{ marginTop: 0 }}>Proposed remediation</h2>
          <p><span className="mono" style={{ fontSize: 16 }}><b>{rca.remediation.action}</b> → {rca.remediation.target}</span>
            {" "}<span className="muted">risk {rca.remediation.risk}</span></p>
          <p className="muted">{rca.remediation.reason}</p>
          {view.status === "AWAITING_APPROVAL" && (
            <div className="row">
              <button onClick={() => decide(true)} disabled={busy}>{busy ? "Executing and verifying…" : "Approve"}</button>
              <button className="danger" onClick={() => decide(false)} disabled={busy}>Reject</button>
              <span className="muted">Nothing runs until a human approves. Approval authorizes exactly this action, once.</span>
            </div>
          )}
          <h2>Root-cause hypotheses</h2>
          {rca.root_causes.map((c, i) => (
            <div key={i} style={{ marginBottom: 8 }}>
              <b>{c.hypothesis}</b> <span className="muted">({Math.round(c.confidence * 100)}% · {c.cause})</span><br />
              {c.citations.map((s) => <span key={s} className="cite">{s}</span>)}
            </div>
          ))}
        </div>
      )}

      {timeline?.decided_at && (
        <div className="panel">
          <h2 style={{ marginTop: 0 }}>Closed loop {timeline.time_to_mitigate_s != null &&
            <span className="pill RESOLVED">mitigated in {duration(timeline.time_to_mitigate_s)}</span>}</h2>
          <ol className="timeline">
            <li><b>Alert opened</b> <span className="muted">{new Date(incident.opened_at).toLocaleTimeString()}</span></li>
            {STEPS.filter(([k]) => timeline[k]).map(([k, label]) => (
              <li key={k}><b>{label}</b> <span className="muted">+{since(timeline[k])}</span></li>
            ))}
          </ol>
          {execution && (
            <p className="mono">
              {execution.executed
                ? <>executed <b>{execution.action}</b> on <b>{execution.service}</b>{execution.result &&
                    <span className="muted"> · {Object.entries(execution.result).filter(([k]) => k !== "service")
                      .map(([k, v]) => `${k}=${typeof v === "object" ? JSON.stringify(v) : v}`).join(" · ")}</span>}</>
                : <span className="error">not executed: {execution.error}</span>}
            </p>
          )}
          {verification && (
            <div className="mono">
              {verification.attempts.map((a, i) => (
                <div key={i} className="event">probe {i + 1}: {a.error ?? `success ${Math.round((a.success_rate ?? 0) * 100)}% · p95 ${a.p95_ms} ms`}</div>
              ))}
              <p className={verification.healthy ? "" : "error"}>
                {verification.healthy ? "Healthy: service recovered." : "Still unhealthy after the remediation: needs a human."}</p>
            </div>
          )}
        </div>
      )}

      {!!view.guardrail_events?.length && (
        <div className="panel">
          <h2 style={{ marginTop: 0 }}>Guardrails</h2>
          {view.guardrail_events.map((e, i) => (
            <div key={i} className="event mono">{String(e.type)}: {Object.entries(e).filter(([k]) => k !== "type")
              .map(([k, v]) => `${k}=${typeof v === "object" ? JSON.stringify(v) : v}`).join(" · ")}</div>
          ))}
        </div>
      )}

      <div className="grid2">
        <div className="panel">
          <h2 style={{ marginTop: 0 }}>Evidence ({view.evidence?.length ?? 0})</h2>
          {view.evidence?.map((e, i) => (
            <div key={i} style={{ marginBottom: 8, opacity: cited.has(e.source) ? 1 : 0.65 }}>
              <span className="cite">{e.source}</span>{cited.has(e.source) && <b> cited</b>}
              <div className="mono muted">{e.detail.slice(0, 260)}</div>
            </div>
          ))}
        </div>
        <div className="panel">
          <h2 style={{ marginTop: 0 }}>Postmortem</h2>
          {view.postmortem ? <pre>{view.postmortem}</pre> : <p className="muted">Written after the decision.</p>}
        </div>
      </div>
    </>
  );
}
