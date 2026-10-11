package dev.sentinel.gateway.knowledge;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.List;

import dev.sentinel.gateway.FakeEmbeddingModel;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.transaction.support.TransactionTemplate;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;
import org.testcontainers.postgresql.PostgreSQLContainer;
import org.testcontainers.utility.DockerImageName;

/** Runs the real hybrid SQL against pgvector, using the schema owned by ingestion-service. */
@Testcontainers
@SpringBootTest(properties = {
        "spring.flyway.enabled=true",
        "spring.flyway.locations=filesystem:../ingestion-service/src/main/resources/db/migration",
        "spring.flyway.placeholders.app_password=sentinel_app"})
class KnowledgeSearchIT {

    @Container
    static PostgreSQLContainer pg = new PostgreSQLContainer(
            DockerImageName.parse("pgvector/pgvector:pg16").asCompatibleSubstituteFor("postgres"));

    @DynamicPropertySource
    static void props(DynamicPropertyRegistry r) {
        r.add("spring.datasource.url", pg::getJdbcUrl);
        r.add("spring.flyway.user", pg::getUsername);
        r.add("spring.flyway.password", pg::getPassword);
    }

    @Autowired
    KnowledgeSearch search;

    @Autowired
    JdbcClient jdbc;

    @Autowired
    TransactionTemplate tx;

    @BeforeEach
    void seed() {
        insert("acme", "db-pool", "DB pool exhaustion > Mitigation",
                "Timeout acquiring DB connection means the HikariCP pool is exhausted. Raise maxPoolSize and look for leaked connections.");
        insert("acme", "rollback", "Checkout rollback > Steps",
                "After a bad deploy causes 5xx errors, run the rollback pipeline to restore the previous build.");
        insert("acme", "oom", "JVM memory leak > Diagnosis",
                "Long GC pauses and rising heap usage indicate a memory leak. Capture a heap dump, then restart.");
        insert("globex", "db-pool", "Globex DB runbook",
                "Globex-only secret procedure for DB connection pool exhaustion timeout.");
    }

    void insert(String tenant, String docId, String heading, String content) {
        tx.executeWithoutResult(s -> {
            jdbc.sql("SELECT set_config('app.tenant_id', ?, true)").param(tenant).query(String.class).single();
            jdbc.sql("""
                    INSERT INTO knowledge_documents (tenant_id, doc_id, source, title, content_hash)
                    VALUES (?, ?, ?, ?, 'h') ON CONFLICT DO NOTHING""")
                    .params(tenant, docId, "runbooks/" + docId + ".md", heading.split(" > ")[0]).update();
            jdbc.sql("DELETE FROM knowledge_chunks WHERE tenant_id = ? AND doc_id = ?").params(tenant, docId).update();
            jdbc.sql("""
                    INSERT INTO knowledge_chunks (tenant_id, doc_id, chunk_index, heading, content, embedding)
                    VALUES (?, ?, 0, ?, ?, ?::vector)""")
                    .params(tenant, docId, heading, content,
                            KnowledgeSearch.toVectorLiteral(FakeEmbeddingModel.vector(heading + "\n\n" + content)))
                    .update();
        });
    }

    static List<String> docIds(List<SearchHit> hits) {
        return hits.stream().map(SearchHit::docId).toList();
    }

    @Test
    void keywordSearchMatchesNaturalLanguageQuestionsWithOrSemantics() {
        // Not every word appears in the doc; websearch_to_tsquery (AND) would return nothing.
        var hits = search.search("acme", "what should I do when we get timeouts acquiring a DB connection?",
                KnowledgeSearch.Mode.KEYWORD, 3);
        assertThat(docIds(hits)).first().isEqualTo("db-pool");
    }

    @Test
    void stemmingMatchesWordForms() {
        var hits = search.search("acme", "rolling back deploys", KnowledgeSearch.Mode.KEYWORD, 3);
        assertThat(docIds(hits)).contains("rollback");
    }

    @Test
    void hybridRanksTheRightRunbookFirstForEachScenario() {
        assertThat(docIds(search.search("acme", "connection pool exhausted", KnowledgeSearch.Mode.HYBRID, 3)))
                .first().isEqualTo("db-pool");
        assertThat(docIds(search.search("acme", "bad deploy 5xx errors", KnowledgeSearch.Mode.HYBRID, 3)))
                .first().isEqualTo("rollback");
        assertThat(docIds(search.search("acme", "heap keeps growing, long GC pauses", KnowledgeSearch.Mode.HYBRID, 3)))
                .first().isEqualTo("oom");
    }

    @Test
    void neverReturnsAnotherTenantsChunks() {
        for (var mode : KnowledgeSearch.Mode.values()) {
            var hits = search.search("acme", "Globex secret procedure DB connection pool exhaustion timeout", mode, 10);
            assertThat(hits).allSatisfy(h -> assertThat(h.content()).doesNotContain("Globex"));
        }
        assertThat(search.search("initech", "DB connection pool", KnowledgeSearch.Mode.HYBRID, 10)).isEmpty();
    }

    @Test
    void queryWithOnlyStopWordsReturnsNoKeywordHits() {
        assertThat(search.search("acme", "the and of", KnowledgeSearch.Mode.KEYWORD, 5)).isEmpty();
    }
}
