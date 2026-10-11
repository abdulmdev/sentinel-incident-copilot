CREATE EXTENSION IF NOT EXISTS vector;

-- Services connect as a non-superuser role: superusers bypass row-level security entirely.
DO $$
BEGIN
    CREATE ROLE sentinel_app LOGIN PASSWORD '${app_password}';
EXCEPTION WHEN duplicate_object THEN NULL;
END
$$;
GRANT USAGE ON SCHEMA public TO sentinel_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO sentinel_app;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT USAGE, SELECT ON SEQUENCES TO sentinel_app;

CREATE TABLE tenants (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE incidents (
    id          UUID PRIMARY KEY,
    tenant_id   TEXT NOT NULL REFERENCES tenants(id),
    service     TEXT NOT NULL,
    summary     TEXT NOT NULL,
    severity    TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'OPEN',
    rca         JSONB,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Tenant isolation is enforced by the database as well as by application code.
-- Services set `app.tenant_id` per transaction. FORCE also covers the owner (migrations role)
-- as defense in depth.
ALTER TABLE incidents ENABLE ROW LEVEL SECURITY;
ALTER TABLE incidents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON incidents
    USING (tenant_id = current_setting('app.tenant_id', true));

INSERT INTO tenants (id, name) VALUES ('acme', 'Acme Corp'), ('globex', 'Globex');
