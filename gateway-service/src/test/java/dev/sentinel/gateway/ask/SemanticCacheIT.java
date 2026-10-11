package dev.sentinel.gateway.ask;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.List;

import dev.sentinel.gateway.FakeEmbeddingModel;
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

@Testcontainers
@SpringBootTest(properties = {
        "spring.flyway.enabled=true",
        "spring.flyway.locations=filesystem:../ingestion-service/src/main/resources/db/migration",
        "spring.flyway.placeholders.app_password=sentinel_app",
        "sentinel.ask-cache.similarity-threshold=0.95"})
class SemanticCacheIT {

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
    SemanticCache cache;

    @Autowired
    JdbcClient jdbc;

    @Autowired
    TransactionTemplate tx;

    static final List<AskController.Citation> CITES =
            List.of(new AskController.Citation(1, "db-pool", "DB pool", "DB pool > Fix", "runbooks/db-pool.md"));

    static float[] vec(String q) {
        return FakeEmbeddingModel.vector(q);
    }

    void upsertDoc(String tenant, String hash) {
        tx.executeWithoutResult(s -> {
            jdbc.sql("SELECT set_config('app.tenant_id', ?, true)").param(tenant).query(String.class).single();
            jdbc.sql("""
                    INSERT INTO knowledge_documents (tenant_id, doc_id, source, title, content_hash) VALUES (?, 'db-pool', 's', 't', ?)
                    ON CONFLICT (tenant_id, doc_id) DO UPDATE SET content_hash = EXCLUDED.content_hash, updated_at = now()""")
                    .params(tenant, hash).update();
        });
    }

    @Test
    void hitsOnParaphraseMissesOnDifferentQuestion() {
        upsertDoc("acme", "h1");
        String v = cache.knowledgeVersion("acme");
        cache.store("acme", "how do I fix db connection pool timeouts", vec("how do I fix db connection pool timeouts"),
                "Raise the pool [1].", CITES, "m", v);

        var hit = cache.lookup("acme", vec("db connection pool timeouts: how do I fix"), "m", v);
        assertThat(hit).isPresent();
        assertThat(hit.get().citations()).extracting(AskController.Citation::docId).containsExactly("db-pool");
        assertThat(cache.lookup("acme", vec("how do I roll back checkout"), "m", v)).isEmpty();
    }

    @Test
    void changedKnowledgeInvalidatesCachedAnswers() {
        upsertDoc("acme", "h1");
        String before = cache.knowledgeVersion("acme");
        cache.store("acme", "pool size limit", vec("pool size limit"), "60 per pod [1].", CITES, "m", before);
        upsertDoc("acme", "h2"); // runbook edited
        String after = cache.knowledgeVersion("acme");

        assertThat(after).isNotEqualTo(before);
        assertThat(cache.lookup("acme", vec("pool size limit"), "m", after)).isEmpty();
    }

    @Test
    void neverServesAnotherTenantsOrAnotherModelsAnswer() {
        upsertDoc("acme", "h1");
        upsertDoc("globex", "h1");
        String acmeV = cache.knowledgeVersion("acme");
        cache.store("acme", "secret procedure", vec("secret procedure"), "acme-only [1].", CITES, "m", acmeV);

        assertThat(cache.lookup("globex", vec("secret procedure"), "m", cache.knowledgeVersion("globex"))).isEmpty();
        assertThat(cache.lookup("acme", vec("secret procedure"), "other-model", acmeV)).isEmpty();
        assertThat(cache.lookup("acme", vec("secret procedure"), "m", acmeV)).isPresent();
    }
}
