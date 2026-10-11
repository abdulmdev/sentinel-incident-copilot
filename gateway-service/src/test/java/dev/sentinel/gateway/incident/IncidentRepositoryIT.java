package dev.sentinel.gateway.incident;

import static org.assertj.core.api.Assertions.assertThat;

import java.util.Map;
import java.util.UUID;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;
import org.testcontainers.postgresql.PostgreSQLContainer;
import org.testcontainers.utility.DockerImageName;

@Testcontainers
@SpringBootTest(properties = {
        "spring.flyway.enabled=true",
        "spring.flyway.locations=filesystem:../ingestion-service/src/main/resources/db/migration",
        "spring.flyway.placeholders.app_password=sentinel_app"})
class IncidentRepositoryIT {

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
    IncidentRepository repo;

    @Test
    void lifecycleIsRecordedAndListedPerTenant() {
        UUID id = UUID.randomUUID();
        repo.create("acme", id, "checkout", "5xx above 20%", "SEV1", "alice@acme");
        repo.update("acme", id, Map.of("status", "AWAITING_APPROVAL",
                "rca", Map.of("remediation", Map.of("action", "rollback"))), null);
        repo.update("acme", id, Map.of("status", "RESOLVED"), "bob@acme");

        var rows = repo.list("acme", 10);
        assertThat(rows).singleElement().satisfies(r -> {
            assertThat(r.status()).isEqualTo("RESOLVED");
            assertThat(r.proposedAction()).isEqualTo("rollback");   // kept from the earlier step
            assertThat(r.openedBy()).isEqualTo("alice@acme");
            assertThat(r.reviewer()).isEqualTo("bob@acme");
        });
        assertThat(repo.list("globex", 10)).isEmpty();
    }

    @Test
    void failedIncidentsAreKept() {
        UUID id = UUID.randomUUID();
        repo.create("initech", id, "orders", "latency", "SEV2", "carol@initech");
        repo.markFailed("initech", id);
        assertThat(repo.list("initech", 10)).extracting(IncidentRepository.Row::status).containsExactly("FAILED");
    }
}
