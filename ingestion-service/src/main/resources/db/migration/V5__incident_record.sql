-- The gateway's system of record for incidents (list views, audit). Workflow state stays in the
-- agent's LangGraph checkpoints; this table holds what humans search, filter and audit.
ALTER TABLE incidents
    ADD COLUMN proposed_action TEXT,
    ADD COLUMN reviewer        TEXT,
    ADD COLUMN opened_by       TEXT,
    ADD COLUMN updated_at      TIMESTAMPTZ NOT NULL DEFAULT now();
ALTER TABLE incidents DROP CONSTRAINT incidents_tenant_id_fkey;  -- tenants are provisioned by the IdP, not this table
CREATE INDEX incidents_tenant_created_idx ON incidents (tenant_id, created_at DESC);
