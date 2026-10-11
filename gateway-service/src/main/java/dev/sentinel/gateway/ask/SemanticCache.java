package dev.sentinel.gateway.ask;

import java.util.List;
import java.util.Optional;

import dev.sentinel.gateway.knowledge.KnowledgeSearch;
import io.micrometer.core.instrument.MeterRegistry;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Service;
import org.springframework.transaction.support.TransactionTemplate;
import tools.jackson.core.type.TypeReference;
import tools.jackson.databind.json.JsonMapper;

/**
 * Tenant-scoped semantic cache for /ask.
 *
 * A hit needs the same tenant (RLS), the same model, the same knowledge version (unchanged
 * runbooks), an entry younger than the TTL, and cosine similarity ≥ threshold. The threshold is
 * deliberately high: a wrong cached answer to an on-call question costs far more than an LLM call.
 */
@Service
public class SemanticCache {

    public record Entry(String answer, List<AskController.Citation> citations, double similarity) {
    }

    private static final TypeReference<List<AskController.Citation>> CITATIONS = new TypeReference<>() {
    };

    private final JdbcClient jdbc;
    private final TransactionTemplate tx;
    private final JsonMapper json;
    private final MeterRegistry meters;
    private final double threshold;
    private final int ttlHours;

    public SemanticCache(JdbcClient jdbc, TransactionTemplate tx, JsonMapper json, MeterRegistry meters,
                         @Value("${sentinel.ask-cache.similarity-threshold:0.95}") double threshold,
                         @Value("${sentinel.ask-cache.ttl-hours:24}") int ttlHours) {
        this.jdbc = jdbc;
        this.tx = tx;
        this.json = json;
        this.meters = meters;
        this.threshold = threshold;
        this.ttlHours = ttlHours;
    }

    /** Changes whenever any of the tenant's knowledge documents is added, changed or removed. */
    public String knowledgeVersion(String tenantId) {
        return tx.execute(s -> {
            bind(tenantId);
            return jdbc.sql("""
                    SELECT count(*) || ':' || coalesce(max(updated_at)::text, '-')
                         || ':' || coalesce(md5(string_agg(content_hash, ',' ORDER BY doc_id)), '-')
                    FROM knowledge_documents WHERE tenant_id = ?""")
                    .param(tenantId).query(String.class).single();
        });
    }

    public Optional<Entry> lookup(String tenantId, float[] question, String model, String version) {
        String vec = KnowledgeSearch.toVectorLiteral(question);
        Optional<Entry> hit = tx.execute(s -> {
            bind(tenantId);
            return jdbc.sql("""
                    SELECT id, answer, citations::text AS citations, 1 - (embedding <=> ?::vector) AS similarity
                    FROM ask_cache
                    WHERE tenant_id = ? AND model = ? AND knowledge_version = ?
                      AND created_at > now() - make_interval(hours => ?)
                    ORDER BY embedding <=> ?::vector
                    LIMIT 1""")
                    .params(vec, tenantId, model, version, ttlHours, vec)
                    .query((rs, n) -> new Object[] {rs.getLong("id"), new Entry(rs.getString("answer"),
                            json.readValue(rs.getString("citations"), CITATIONS), rs.getDouble("similarity"))})
                    .optional()
                    .filter(row -> ((Entry) row[1]).similarity() >= threshold)
                    .map(row -> {
                        jdbc.sql("UPDATE ask_cache SET hits = hits + 1 WHERE id = ?").param(row[0]).update();
                        return (Entry) row[1];
                    });
        });
        meters.counter("sentinel.ask.cache", "result", hit.isPresent() ? "hit" : "miss").increment();
        return hit;
    }

    public void store(String tenantId, String question, float[] embedding, String answer,
                      List<AskController.Citation> citations, String model, String version) {
        tx.executeWithoutResult(s -> {
            bind(tenantId);
            jdbc.sql("""
                    INSERT INTO ask_cache (tenant_id, question, embedding, answer, citations, model, knowledge_version)
                    VALUES (?, ?, ?::vector, ?, ?::jsonb, ?, ?)""")
                    .params(tenantId, question, KnowledgeSearch.toVectorLiteral(embedding), answer,
                            json.writeValueAsString(citations), model, version)
                    .update();
        });
    }

    private void bind(String tenantId) {
        jdbc.sql("SELECT set_config('app.tenant_id', ?, true)").param(tenantId).query(String.class).single();
    }
}
