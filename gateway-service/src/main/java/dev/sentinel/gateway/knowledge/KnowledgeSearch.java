package dev.sentinel.gateway.knowledge;

import java.util.List;
import java.util.Locale;

import io.micrometer.core.instrument.MeterRegistry;
import io.micrometer.core.instrument.Timer;
import org.springframework.ai.embedding.EmbeddingModel;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Service;
import org.springframework.transaction.support.TransactionTemplate;

/**
 * Hybrid retrieval over the tenant's runbooks and postmortems.
 *
 * Dense search catches paraphrases ("service keeps OOMing" ↔ "memory leak"). Lexical search
 * catches exact tokens embeddings blur: error strings, metric names, service names.
 * Both run in one transaction scoped to the tenant by row-level security, then get fused with RRF.
 */
@Service
public class KnowledgeSearch {

    public enum Mode {
        VECTOR, KEYWORD, HYBRID;

        public static Mode parse(String s) {
            return s == null ? HYBRID : valueOf(s.toUpperCase(Locale.ROOT));
        }
    }

    /** Candidates per retriever before fusion. Recall comes from here; precision from fusion. */
    static final int CANDIDATES = 20;

    private static final String SELECT = """
            SELECT c.id, c.doc_id, d.title, d.source, c.heading, c.content, %s AS score
            FROM knowledge_chunks c
            JOIN knowledge_documents d ON d.tenant_id = c.tenant_id AND d.doc_id = c.doc_id
            """;

    private static final String VECTOR_SQL = SELECT.formatted("1 - (c.embedding <=> ?::vector)") + """
            WHERE c.tenant_id = ?
            ORDER BY c.embedding <=> ?::vector
            LIMIT ?""";

    // OR over the query's stemmed, stop-word-free lexemes. websearch_to_tsquery ANDs every
    // term, which almost never matches a natural-language question.
    private static final String KEYWORD_SQL = """
            WITH q AS (
                SELECT to_tsquery('simple', coalesce(string_agg(quote_literal(lexeme), ' | '), '')) AS q
                FROM unnest(tsvector_to_array(to_tsvector('english', ?))) AS lexeme
            )
            """ + SELECT.formatted("ts_rank_cd(c.tsv, q.q)") + """
            CROSS JOIN q
            WHERE c.tenant_id = ? AND c.tsv @@ q.q
            ORDER BY score DESC
            LIMIT ?""";

    private final JdbcClient jdbc;
    private final TransactionTemplate tx;
    private final EmbeddingModel embeddings;
    private final MeterRegistry meters;

    public KnowledgeSearch(JdbcClient jdbc, TransactionTemplate tx, EmbeddingModel embeddings, MeterRegistry meters) {
        this.jdbc = jdbc;
        this.tx = tx;
        this.embeddings = embeddings;
        this.meters = meters;
    }

    public List<SearchHit> search(String tenantId, String query, Mode mode, int k) {
        Timer.Sample sample = Timer.start(meters);
        float[] queryVector = mode == Mode.KEYWORD ? null : embeddings.embed(query);
        List<SearchHit> hits = tx.execute(status -> {
            jdbc.sql("SELECT set_config('app.tenant_id', ?, true)").param(tenantId).query(String.class).single();
            return switch (mode) {
                case VECTOR -> vector(tenantId, queryVector, k);
                case KEYWORD -> keyword(tenantId, query, k);
                case HYBRID -> ReciprocalRankFusion.fuse(
                        List.of(vector(tenantId, queryVector, CANDIDATES), keyword(tenantId, query, CANDIDATES)), k);
            };
        });
        sample.stop(meters.timer("sentinel.retrieval.latency", "mode", mode.name().toLowerCase(Locale.ROOT)));
        return hits;
    }

    private List<SearchHit> vector(String tenantId, float[] v, int limit) {
        String literal = toVectorLiteral(v);
        return jdbc.sql(VECTOR_SQL).params(literal, tenantId, literal, limit).query(this::row).list();
    }

    private List<SearchHit> keyword(String tenantId, String query, int limit) {
        return jdbc.sql(KEYWORD_SQL).params(query, tenantId, limit).query(this::row).list();
    }

    private SearchHit row(java.sql.ResultSet rs, int n) throws java.sql.SQLException {
        return new SearchHit(rs.getLong("id"), rs.getString("doc_id"), rs.getString("title"), rs.getString("source"),
                rs.getString("heading"), rs.getString("content"), rs.getDouble("score"));
    }

    public static String toVectorLiteral(float[] v) {
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
