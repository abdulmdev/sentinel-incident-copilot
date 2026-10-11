package dev.sentinel.ingestion;

import java.util.List;

import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Repository;
import org.springframework.transaction.annotation.Transactional;

@Repository
public class KnowledgeRepository {

    public record EmbeddedChunk(MarkdownChunker.Chunk chunk, float[] embedding) {
    }

    private final JdbcClient jdbc;

    public KnowledgeRepository(JdbcClient jdbc) {
        this.jdbc = jdbc;
    }

    /** True if this exact content is already indexed, so replays and duplicates cost no embedding work. */
    @Transactional(readOnly = true)
    public boolean isCurrent(String tenantId, String docId, String contentHash) {
        bindTenant(tenantId);
        return jdbc.sql("SELECT count(*) FROM knowledge_documents WHERE tenant_id = ? AND doc_id = ? AND content_hash = ?")
                .params(tenantId, docId, contentHash).query(Long.class).single() > 0;
    }

    /** Atomically replaces every chunk of a document: readers see the old version or the new one, never a mix. */
    @Transactional
    public void replace(KnowledgeDocument doc, String contentHash, List<EmbeddedChunk> chunks) {
        bindTenant(doc.tenantId());
        jdbc.sql("""
                INSERT INTO knowledge_documents (tenant_id, doc_id, source, title, content_hash)
                VALUES (?, ?, ?, ?, ?)
                ON CONFLICT (tenant_id, doc_id) DO UPDATE
                SET source = EXCLUDED.source, title = EXCLUDED.title,
                    content_hash = EXCLUDED.content_hash, updated_at = now()""")
                .params(doc.tenantId(), doc.docId(), doc.source(), doc.title(), contentHash).update();
        jdbc.sql("DELETE FROM knowledge_chunks WHERE tenant_id = ? AND doc_id = ?")
                .params(doc.tenantId(), doc.docId()).update();
        for (EmbeddedChunk ec : chunks) {
            jdbc.sql("""
                    INSERT INTO knowledge_chunks (tenant_id, doc_id, chunk_index, heading, content, embedding)
                    VALUES (?, ?, ?, ?, ?, ?::vector)""")
                    .params(doc.tenantId(), doc.docId(), ec.chunk().index(), ec.chunk().heading(),
                            ec.chunk().text(), toVectorLiteral(ec.embedding()))
                    .update();
        }
    }

    /** Scopes row-level security to this tenant for the rest of the current transaction. */
    private void bindTenant(String tenantId) {
        jdbc.sql("SELECT set_config('app.tenant_id', ?, true)").param(tenantId).query(String.class).single();
    }

    static String toVectorLiteral(float[] v) {
        StringBuilder sb = new StringBuilder(v.length * 10).append('[');
        for (int i = 0; i < v.length; i++) {
            if (i > 0) {
                sb.append(',');
            }
            sb.append(v[i]);
        }
        return sb.append(']').toString();
    }
}
