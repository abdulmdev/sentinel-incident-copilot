-- Phase 7: alert-driven incidents, remediation execution, verification and time-to-mitigate.

ALTER TABLE incidents
    ADD COLUMN source            TEXT NOT NULL DEFAULT 'console',   -- console | alertmanager
    ADD COLUMN alert_fingerprint TEXT,
    ADD COLUMN opened_at         TIMESTAMPTZ,                         -- alert start (or the console request)
    ADD COLUMN proposed_at       TIMESTAMPTZ,
    ADD COLUMN decided_at        TIMESTAMPTZ,
    ADD COLUMN executed_at       TIMESTAMPTZ,
    ADD COLUMN verified_at       TIMESTAMPTZ,
    ADD COLUMN alert_resolved_at TIMESTAMPTZ,
    ADD COLUMN execution         JSONB,
    ADD COLUMN verification      JSONB,
    ADD COLUMN time_to_mitigate_s NUMERIC;

-- One alert (fingerprint) maps to at most one *active* incident: Alertmanager re-sends firing
-- alerts on every group interval, and two deliveries may race. The partial unique index makes the
-- link insert itself the lock (INSERT ... ON CONFLICT DO NOTHING). A link goes inactive when its
-- incident reaches a final state, so the same alert firing again later opens a new incident.
CREATE TABLE alert_links (
    tenant_id    TEXT NOT NULL,
    fingerprint  TEXT NOT NULL,
    incident_id  UUID NOT NULL REFERENCES incidents(id),
    alertname    TEXT,
    active       BOOLEAN NOT NULL DEFAULT true,
    fire_count   INT NOT NULL DEFAULT 1,
    first_seen   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen    TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at  TIMESTAMPTZ,
    PRIMARY KEY (tenant_id, fingerprint, incident_id)
);
CREATE UNIQUE INDEX alert_links_one_active ON alert_links (tenant_id, fingerprint) WHERE active;
ALTER TABLE alert_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE alert_links FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON alert_links USING (tenant_id = current_setting('app.tenant_id', true));

-- Append-only record of every remediation decision that reached execution.
CREATE TABLE remediation_audit (
    id              BIGSERIAL PRIMARY KEY,
    tenant_id       TEXT NOT NULL,
    incident_id     UUID NOT NULL,
    action          TEXT NOT NULL,
    target          TEXT NOT NULL,
    approved_by     TEXT NOT NULL,
    capability_jti  TEXT,
    executed        BOOLEAN NOT NULL,
    verified        BOOLEAN,
    outcome         TEXT NOT NULL,
    error           TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX remediation_audit_incident_idx ON remediation_audit (tenant_id, incident_id);
ALTER TABLE remediation_audit ENABLE ROW LEVEL SECURITY;
ALTER TABLE remediation_audit FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON remediation_audit USING (tenant_id = current_setting('app.tenant_id', true));
