-- One row per source document; content_hash makes re-ingestion of unchanged docs a no-op.
CREATE TABLE knowledge_documents (
    tenant_id     TEXT NOT NULL REFERENCES tenants(id),
    doc_id        TEXT NOT NULL,
    source        TEXT NOT NULL,
    title         TEXT NOT NULL,
    content_hash  TEXT NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, doc_id)
);

-- Dense (pgvector) and lexical (tsvector) indexes live on the same row, so hybrid search
-- is one round trip and both sides are always consistent with each other.
CREATE TABLE knowledge_chunks (
    id           BIGSERIAL PRIMARY KEY,
    tenant_id    TEXT NOT NULL,
    doc_id       TEXT NOT NULL,
    chunk_index  INT  NOT NULL,
    heading      TEXT NOT NULL,
    content      TEXT NOT NULL,
    embedding    vector(384) NOT NULL,
    tsv          tsvector GENERATED ALWAYS AS (
                     setweight(to_tsvector('english', heading), 'A') ||
                     setweight(to_tsvector('english', content), 'B')) STORED,
    UNIQUE (tenant_id, doc_id, chunk_index),
    FOREIGN KEY (tenant_id, doc_id) REFERENCES knowledge_documents (tenant_id, doc_id) ON DELETE CASCADE
);

CREATE INDEX knowledge_chunks_embedding_idx ON knowledge_chunks USING hnsw (embedding vector_cosine_ops);
CREATE INDEX knowledge_chunks_tsv_idx ON knowledge_chunks USING gin (tsv);
CREATE INDEX knowledge_chunks_tenant_idx ON knowledge_chunks (tenant_id);

ALTER TABLE knowledge_documents ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge_documents FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON knowledge_documents
    USING (tenant_id = current_setting('app.tenant_id', true));

ALTER TABLE knowledge_chunks ENABLE ROW LEVEL SECURITY;
ALTER TABLE knowledge_chunks FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON knowledge_chunks
    USING (tenant_id = current_setting('app.tenant_id', true));
