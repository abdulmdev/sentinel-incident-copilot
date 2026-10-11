package dev.sentinel.ingestion;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.context.annotation.Bean;
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
        "spring.ai.model.embedding=none",
        "spring.kafka.listener.auto-startup=false"})
class IngestionPipelineIT {

    @Container
    static PostgreSQLContainer pg = new PostgreSQLContainer(
            DockerImageName.parse("pgvector/pgvector:pg16").asCompatibleSubstituteFor("postgres"));

    @DynamicPropertySource
    static void props(DynamicPropertyRegistry r) {
        r.add("spring.datasource.url", pg::getJdbcUrl);
        r.add("spring.flyway.user", pg::getUsername);      // superuser runs migrations
        r.add("spring.flyway.password", pg::getPassword);
        // datasource stays sentinel_app: a non-superuser, so RLS is actually enforced
    }

    @TestConfiguration
    static class Fakes {
        @Bean
        FakeEmbeddingModel embeddingModel() {
            return new FakeEmbeddingModel();
        }
    }

    @Autowired
    IngestionPipeline pipeline;

    @Autowired
    JdbcClient jdbc;

    @Autowired
    TransactionTemplate tx;

    static KnowledgeDocument doc(String tenant, String id, String content) {
        return new KnowledgeDocument(tenant, id, "runbooks/" + id + ".md", "Runbook " + id, content);
    }

    long chunksVisibleTo(String tenant) {
        return tx.execute(s -> {
            if (tenant != null) {
                jdbc.sql("SELECT set_config('app.tenant_id', ?, true)").param(tenant).query(String.class).single();
            }
            return jdbc.sql("SELECT count(*) FROM knowledge_chunks").query(Long.class).single();
        });
    }

    @Test
    void indexesThenSkipsUnchangedThenReplacesOnChange() {
        var v1 = doc("acme", "db-pool", "## Symptoms\nTimeouts acquiring connections.\n## Fix\nRaise pool size.");
        assertThat(pipeline.ingest(v1)).isEqualTo(IngestionPipeline.Outcome.INDEXED);
        assertThat(pipeline.ingest(v1)).isEqualTo(IngestionPipeline.Outcome.UNCHANGED);

        var v2 = doc("acme", "db-pool", "## Fix\nRaise pool size and find the connection leak.");
        assertThat(pipeline.ingest(v2)).isEqualTo(IngestionPipeline.Outcome.INDEXED);

        long acmeDbPool = tx.execute(s -> {
            jdbc.sql("SELECT set_config('app.tenant_id', 'acme', true)").query(String.class).single();
            return jdbc.sql("SELECT count(*) FROM knowledge_chunks WHERE doc_id = 'db-pool'").query(Long.class).single();
        });
        assertThat(acmeDbPool).isEqualTo(1); // old v1 chunks are gone, not appended to
    }

    @Test
    void rowLevelSecurityHidesOtherTenantsAndUnscopedReads() {
        pipeline.ingest(doc("globex", "secret-runbook", "Globex internal failover procedure."));

        assertThat(chunksVisibleTo("globex")).isPositive();
        long acmeBefore = chunksVisibleTo("acme");
        pipeline.ingest(doc("globex", "another", "More globex content."));
        assertThat(chunksVisibleTo("acme")).isEqualTo(acmeBefore);
        assertThat(chunksVisibleTo(null)).isZero(); // forgot to bind a tenant -> sees nothing
    }

    @Test
    void rejectsInvalidDocuments() {
        assertThatThrownBy(() -> pipeline.ingest(new KnowledgeDocument("acme", "", "s", "t", "c")))
                .isInstanceOf(KnowledgeDocument.InvalidDocumentException.class);
    }
}
