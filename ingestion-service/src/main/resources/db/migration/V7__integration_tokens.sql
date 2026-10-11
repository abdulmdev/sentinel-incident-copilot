-- Phase 8: integration tokens are issued by the gateway (ES256), listed and revocable.
-- Only the jti is stored, never the token: a leaked database can't be replayed against the webhook.
CREATE TABLE integration_tokens (
    jti          TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    kind         TEXT NOT NULL,              -- alertmanager
    name         TEXT NOT NULL,
    created_by   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ,
    revoked_by   TEXT,
    last_used_at TIMESTAMPTZ
);
CREATE INDEX integration_tokens_tenant_idx ON integration_tokens (tenant_id, created_at DESC);
ALTER TABLE integration_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE integration_tokens FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON integration_tokens USING (tenant_id = current_setting('app.tenant_id', true));
