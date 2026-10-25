package dev.sentinel.gateway.alert;

import java.time.OffsetDateTime;
import java.util.Optional;
import java.util.UUID;
import java.util.function.Supplier;

import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Repository;
import org.springframework.transaction.support.TransactionTemplate;

/** Alert fingerprint ↔ incident links; the partial unique index makes deduplication race-safe. */
@Repository
public class AlertLinkRepository {

    private final JdbcClient jdbc;
    private final TransactionTemplate tx;

    public AlertLinkRepository(JdbcClient jdbc, TransactionTemplate tx) {
        this.jdbc = jdbc;
        this.tx = tx;
    }

    /** What a firing alert did: opened a new incident, re-fired onto its own, or joined another alert's. */
    public enum Outcome { OPENED, DEDUPLICATED, CORRELATED }

    public record Link(UUID incidentId, Outcome outcome) {
    }

    /**
     * Links a firing alert to an incident, opening one (via {@code open}, in this transaction) only when
     * neither this alert nor another alert on the same service already has an active incident.
     *
     * Serialized per (tenant, service) with a transaction-scoped advisory lock, so concurrent deliveries
     * (re-sends, or several alert rules tripping on one fault) can't open duplicates; the partial unique
     * index on (tenant, fingerprint) WHERE active stays as the backstop.
     */
    public Link link(String tenant, String service, String fingerprint, String alertname, Supplier<UUID> open) {
        return tx.execute(s -> {
            bind(tenant);
            jdbc.sql("SELECT pg_advisory_xact_lock(hashtext(?))").param(tenant + "/" + service).query().singleRow();
            // An incident that ended by another path (marked failed, or closed outside the decision flow)
            // must not keep catching alerts: release its links first, so this alert opens a new incident.
            jdbc.sql("""
                    UPDATE alert_links l SET active = false FROM incidents i
                    WHERE l.incident_id = i.id AND l.tenant_id = ? AND l.active
                      AND i.status IN ('RESOLVED', 'CLOSED_NO_ACTION', 'FAILED')""")
                    .param(tenant).update();
            Optional<UUID> same = jdbc.sql("""
                    UPDATE alert_links SET last_seen = now(), fire_count = fire_count + 1
                    WHERE tenant_id = ? AND fingerprint = ? AND active RETURNING incident_id""")
                    .params(tenant, fingerprint).query(UUID.class).optional();
            if (same.isPresent()) {
                return new Link(same.get(), Outcome.DEDUPLICATED);
            }
            Optional<UUID> sibling = jdbc.sql("""
                    SELECT l.incident_id FROM alert_links l JOIN incidents i ON i.id = l.incident_id
                    WHERE l.tenant_id = ? AND l.active AND i.service = ? ORDER BY l.first_seen LIMIT 1""")
                    .params(tenant, service).query(UUID.class).optional();
            UUID incident = sibling.orElseGet(open);
            jdbc.sql("INSERT INTO alert_links (tenant_id, fingerprint, incident_id, alertname) VALUES (?, ?, ?, ?)")
                    .params(tenant, fingerprint, incident, alertname).update();
            return new Link(incident, sibling.isPresent() ? Outcome.CORRELATED : Outcome.OPENED);
        });
    }

    /**
     * The alert stopped firing: stamps the most recent unresolved link for it and releases it. That incident
     * may already be closed (verified fixes usually land before Alertmanager notices), so this does not
     * require the link to be active. One link per notification: the newest unresolved one. Returns the
     * incident only once its last linked alert has resolved (correlated alerts can clear at different times).
     */
    public Optional<UUID> resolve(String tenant, String fingerprint, OffsetDateTime endsAt) {
        return tx.execute(s -> {
            bind(tenant);
            Optional<UUID> incident = jdbc.sql("""
                    UPDATE alert_links SET resolved_at = ?, active = false
                    WHERE tenant_id = ? AND fingerprint = ? AND incident_id = (
                        SELECT incident_id FROM alert_links WHERE tenant_id = ? AND fingerprint = ? AND resolved_at IS NULL
                        ORDER BY first_seen DESC LIMIT 1)
                    RETURNING incident_id""")
                    .params(endsAt, tenant, fingerprint, tenant, fingerprint).query(UUID.class).optional();
            return incident.filter(id -> jdbc.sql(
                            "SELECT count(*) FROM alert_links WHERE tenant_id = ? AND incident_id = ? AND resolved_at IS NULL")
                    .params(tenant, id).query(Long.class).single() == 0);
        });
    }

    /** Called when an incident reaches a final state: the same alert firing later opens a new incident. */
    public void deactivate(String tenant, UUID incidentId) {
        tx.executeWithoutResult(s -> {
            bind(tenant);
            jdbc.sql("UPDATE alert_links SET active = false WHERE tenant_id = ? AND incident_id = ?")
                    .params(tenant, incidentId).update();
        });
    }

    private void bind(String tenant) {
        jdbc.sql("SELECT set_config('app.tenant_id', ?, true)").param(tenant).query(String.class).single();
    }
}
