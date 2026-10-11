package dev.sentinel.gateway.alert;

import static org.assertj.core.api.Assertions.assertThat;

import java.math.BigDecimal;
import java.time.OffsetDateTime;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.Callable;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;

import dev.sentinel.gateway.incident.IncidentRepository;
import dev.sentinel.gateway.remediation.RemediationAuditRepository;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;
import org.testcontainers.postgresql.PostgreSQLContainer;
import org.testcontainers.utility.DockerImageName;

/** V6 against real Postgres (as the RLS-bound sentinel_app role): dedupe locks, timeline, audit. */
@Testcontainers
@SpringBootTest(properties = {
        "spring.flyway.enabled=true",
        "spring.flyway.locations=filesystem:../ingestion-service/src/main/resources/db/migration",
        "spring.flyway.placeholders.app_password=sentinel_app"})
class ClosedLoopRepositoryIT {

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
    AlertLinkRepository links;

    @Autowired
    IncidentRepository incidents;

    @Autowired
    RemediationAuditRepository audit;

    UUID incident(String tenant) {
        return incident(tenant, "checkout");
    }

    UUID incident(String tenant, String service) {
        UUID id = UUID.randomUUID();
        incidents.create(tenant, id, service, "5xx", "SEV1", "alertmanager:X", "alertmanager",
                OffsetDateTime.parse("2026-10-06T10:00:00Z"), "fp");
        return id;
    }

    AlertLinkRepository.Link fire(String tenant, String service, String fp) {
        return links.link(tenant, service, fp, "X", () -> incident(tenant, service));
    }

    List<AlertLinkRepository.Link> concurrently(List<Callable<AlertLinkRepository.Link>> calls) throws Exception {
        List<AlertLinkRepository.Link> results = new ArrayList<>();
        try (var pool = Executors.newFixedThreadPool(calls.size())) {
            for (Future<AlertLinkRepository.Link> f : pool.invokeAll(calls)) {
                results.add(f.get());
            }
        }
        return results;
    }

    @Test
    void concurrentDeliveriesOfOneAlertOpenExactlyOneIncident() throws Exception {
        List<Callable<AlertLinkRepository.Link>> deliveries = new ArrayList<>();
        for (int i = 0; i < 8; i++) {
            deliveries.add(() -> fire("acme", "checkout", "fp-race"));
        }
        var results = concurrently(deliveries);
        assertThat(results).filteredOn(l -> l.outcome() == AlertLinkRepository.Outcome.OPENED).hasSize(1);
        assertThat(results).extracting(AlertLinkRepository.Link::incidentId).containsOnly(results.getFirst().incidentId());
        assertThat(incidents.list("acme", 200)).filteredOn(r -> r.service().equals("checkout")).hasSize(1); // no spares
    }

    @Test
    void differentRulesTrippingOnOneServiceConcurrentlyShareOneIncident() throws Exception {
        List<Callable<AlertLinkRepository.Link>> rules = new ArrayList<>();
        for (int i = 0; i < 6; i++) {
            String fp = "fp-rule-" + i;
            rules.add(() -> fire("hooli", "orders", fp));
        }
        var results = concurrently(rules);
        assertThat(results).extracting(AlertLinkRepository.Link::outcome)
                .containsOnlyOnce(AlertLinkRepository.Outcome.OPENED)
                .contains(AlertLinkRepository.Outcome.CORRELATED);
        assertThat(results).extracting(AlertLinkRepository.Link::incidentId).containsOnly(results.getFirst().incidentId());
    }

    @Test
    void otherServicesGetTheirOwnIncident() {
        UUID cart = fire("acme", "cart", "fp-a").incidentId();
        var payments = fire("acme", "payments", "fp-b");
        assertThat(payments.outcome()).isEqualTo(AlertLinkRepository.Outcome.OPENED);
        assertThat(payments.incidentId()).isNotEqualTo(cart);
    }

    @Test
    void finishedIncidentReleasesTheAlertForANewOne() {
        var first = fire("acme", "search", "fp-cycle");
        assertThat(fire("acme", "search", "fp-cycle").outcome()).isEqualTo(AlertLinkRepository.Outcome.DEDUPLICATED);

        links.deactivate("acme", first.incidentId());

        var next = fire("acme", "search", "fp-cycle");
        assertThat(next.outcome()).isEqualTo(AlertLinkRepository.Outcome.OPENED);
        assertThat(next.incidentId()).isNotEqualTo(first.incidentId());
    }

    @Test
    void sameFingerprintInAnotherTenantIsIndependentAndInvisible() {
        var acme = fire("acme", "shared", "fp-shared");
        var globex = fire("globex", "shared", "fp-shared");
        assertThat(globex.outcome()).isEqualTo(AlertLinkRepository.Outcome.OPENED);
        assertThat(globex.incidentId()).isNotEqualTo(acme.incidentId());
    }

    @Test
    void incidentIsReportedResolvedOnlyWhenItsLastAlertClears() {
        UUID id = fire("acme", "ledger", "fp-res-1").incidentId();
        fire("acme", "ledger", "fp-res-2"); // correlated
        var ends = OffsetDateTime.parse("2026-10-06T10:07:00Z");

        assertThat(links.resolve("acme", "fp-res-1", ends)).isEmpty();   // the other alert still fires
        assertThat(links.resolve("acme", "fp-res-2", ends)).contains(id);
        incidents.markAlertResolved("acme", id, ends);

        var row = incidents.list("acme", 200).stream().filter(r -> r.id().equals(id)).findFirst().orElseThrow();
        assertThat(row.alertResolvedAt().toInstant()).isEqualTo(ends.toInstant());
    }

    @Test
    void resolutionAfterTheIncidentClosedStillReachesItButNotOlderOnes() {
        UUID older = fire("acme", "late", "fp-late").incidentId();
        links.deactivate("acme", older);            // closed without the alert resolving
        UUID latest = fire("acme", "late", "fp-late").incidentId();
        links.deactivate("acme", latest);           // verified fix closed it before Alertmanager noticed

        assertThat(links.resolve("acme", "fp-late", OffsetDateTime.now())).contains(latest);
        assertThat(links.resolve("acme", "fp-late", OffsetDateTime.now())).contains(older); // next unresolved
        assertThat(links.resolve("acme", "fp-late", OffsetDateTime.now())).isEmpty();
    }

    @Test
    void timelineAndTimeToMitigateAreMirroredFromTheAgent() {
        UUID id = incident("initech");
        incidents.update("initech", id, Map.of("status", "AWAITING_APPROVAL",
                "rca", Map.of("remediation", Map.of("action", "rollback")),
                "timeline", Map.of("opened_at", "2026-10-06T10:00:00Z", "proposed_at", "2026-10-06T10:00:40Z")), null);
        incidents.update("initech", id, Map.of("status", "RESOLVED",
                "execution", Map.of("executed", true), "verification", Map.of("healthy", true),
                "timeline", Map.of("decided_at", "2026-10-06T10:02:00Z", "executed_at", "2026-10-06T10:02:01Z",
                        "verified_at", "2026-10-06T10:02:31Z", "time_to_mitigate_s", 151.0)), "bob@initech");

        var row = incidents.list("initech", 10).getFirst();
        assertThat(row.status()).isEqualTo("RESOLVED");
        assertThat(row.source()).isEqualTo("alertmanager");
        assertThat(row.proposedAction()).isEqualTo("rollback"); // kept from the earlier step
        assertThat(row.timeToMitigateS()).isEqualByComparingTo(new BigDecimal("151"));
    }

    @Test
    void auditIsTenantScoped() {
        UUID id = incident("umbrella");
        audit.record("umbrella", new RemediationAuditRepository.Entry(id, "rollback", "checkout", "alice",
                "jti-1", true, true, "RESOLVED", null));

        assertThat(audit.forIncident("umbrella", id)).singleElement()
                .satisfies(e -> assertThat(e.capabilityJti()).isEqualTo("jti-1"));
        assertThat(audit.forIncident("acme", id)).isEmpty();
    }
}
