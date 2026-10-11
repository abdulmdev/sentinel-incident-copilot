-- Semantic cache for /ask: a paraphrased question reuses an earlier answer instead of calling the LLM.
-- An entry is only valid for the knowledge it was answered from: knowledge_version changes whenever
-- the tenant's runbooks change, so stale answers are never served (no TTL guessing needed).
CREATE TABLE ask_cache (
    id                 BIGSERIAL PRIMARY KEY,
    tenant_id          TEXT NOT NULL REFERENCES tenants(id),
    question           TEXT NOT NULL,
    embedding          vector(384) NOT NULL,
    answer             TEXT NOT NULL,
    citations          JSONB NOT NULL,
    model              TEXT NOT NULL,
    knowledge_version  TEXT NOT NULL,
    hits               INT NOT NULL DEFAULT 0,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX ask_cache_embedding_idx ON ask_cache USING hnsw (embedding vector_cosine_ops);
CREATE INDEX ask_cache_tenant_version_idx ON ask_cache (tenant_id, knowledge_version);

ALTER TABLE ask_cache ENABLE ROW LEVEL SECURITY;
ALTER TABLE ask_cache FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ask_cache
    USING (tenant_id = current_setting('app.tenant_id', true));
