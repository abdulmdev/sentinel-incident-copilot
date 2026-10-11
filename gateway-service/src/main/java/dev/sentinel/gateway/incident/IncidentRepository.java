package dev.sentinel.gateway.incident;

import java.math.BigDecimal;
import java.time.OffsetDateTime;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Repository;
import org.springframework.transaction.support.TransactionTemplate;
import tools.jackson.databind.json.JsonMapper;

/** Incident records, tenant-scoped by row-level security like every other table. */
@Repository
public class IncidentRepository {

    /** States after which nothing more happens automatically. */
    public static final Set<String> FINAL = Set.of("RESOLVED", "CLOSED_NO_ACTION", "FAILED");

    public record Row(UUID id, String service, String summary, String severity, String status,
                      String proposedAction, String openedBy, String reviewer, String source,
                      OffsetDateTime openedAt, OffsetDateTime createdAt, OffsetDateTime updatedAt,
                      BigDecimal timeToMitigateS, OffsetDateTime alertResolvedAt) {
    }

    private final JdbcClient jdbc;
    private final TransactionTemplate tx;
    private final JsonMapper json;

    public IncidentRepository(JdbcClient jdbc, TransactionTemplate tx, JsonMapper json) {
        this.jdbc = jdbc;
        this.tx = tx;
        this.json = json;
    }

    public void create(String tenant, UUID id, String service, String summary, String severity, String openedBy) {
        create(tenant, id, service, summary, severity, openedBy, "console", OffsetDateTime.now(), null);
    }

    public void create(String tenant, UUID id, String service, String summary, String severity, String openedBy,
                       String source, OffsetDateTime openedAt, String alertFingerprint) {
        inTenant(tenant, () -> jdbc.sql("""
                INSERT INTO incidents (id, tenant_id, service, summary, severity, status, opened_by, source,
                                       opened_at, alert_fingerprint)
                VALUES (?, ?, ?, ?, ?, 'INVESTIGATING', ?, ?, ?, ?)""")
                .params(id, tenant, service, summary, severity, openedBy, source, openedAt, alertFingerprint).update());
    }

    /** Mirrors the agent's view after a step: status, proposal, RCA, execution, verification, timeline. */
    @SuppressWarnings("unchecked")
    public void update(String tenant, UUID id, Map<String, Object> agentView, String reviewer) {
        Object rca = agentView.get("rca");
        String action = rca instanceof Map<?, ?> m && m.get("remediation") instanceof Map<?, ?> r
                ? (String) ((Map<String, Object>) r).get("action") : null;
        Map<String, Object> tl = agentView.get("timeline") instanceof Map<?, ?> t ? (Map<String, Object>) t : Map.of();
        inTenant(tenant, () -> jdbc.sql("""
                UPDATE incidents SET status = ?, proposed_action = coalesce(?, proposed_action),
                       rca = coalesce(?::jsonb, rca), reviewer = coalesce(?, reviewer),
                       execution = coalesce(?::jsonb, execution), verification = coalesce(?::jsonb, verification),
                       proposed_at = coalesce(?::timestamptz, proposed_at), decided_at = coalesce(?::timestamptz, decided_at),
                       executed_at = coalesce(?::timestamptz, executed_at), verified_at = coalesce(?::timestamptz, verified_at),
                       time_to_mitigate_s = coalesce(?::numeric, time_to_mitigate_s), updated_at = now()
                WHERE id = ?""")
                .params(String.valueOf(agentView.getOrDefault("status", "UNKNOWN")), action, jsonOrNull(rca), reviewer,
                        jsonOrNull(agentView.get("execution")), jsonOrNull(agentView.get("verification")),
                        str(tl.get("proposed_at")), str(tl.get("decided_at")), str(tl.get("executed_at")),
                        str(tl.get("verified_at")), str(tl.get("time_to_mitigate_s")), id)
                .update());
    }

    public void markFailed(String tenant, UUID id) {
        inTenant(tenant, () -> jdbc.sql("UPDATE incidents SET status = 'FAILED', updated_at = now() WHERE id = ?")
                .param(id).update());
    }

    public void markAlertResolved(String tenant, UUID id, OffsetDateTime at) {
        inTenant(tenant, () -> jdbc.sql("UPDATE incidents SET alert_resolved_at = ?, updated_at = now() WHERE id = ?")
                .params(at, id).update());
    }

    public List<Row> list(String tenant, int limit) {
        return tx.execute(s -> {
            bind(tenant);
            return jdbc.sql("""
                    SELECT id, service, summary, severity, status, proposed_action, opened_by, reviewer, source,
                           opened_at, created_at, updated_at, time_to_mitigate_s, alert_resolved_at
                    FROM incidents WHERE tenant_id = ? ORDER BY created_at DESC LIMIT ?""")
                    .params(tenant, limit)
                    .query((rs, n) -> new Row(rs.getObject("id", UUID.class), rs.getString("service"),
                            rs.getString("summary"), rs.getString("severity"), rs.getString("status"),
                            rs.getString("proposed_action"), rs.getString("opened_by"), rs.getString("reviewer"),
                            rs.getString("source"), rs.getObject("opened_at", OffsetDateTime.class),
                            rs.getObject("created_at", OffsetDateTime.class),
                            rs.getObject("updated_at", OffsetDateTime.class), rs.getBigDecimal("time_to_mitigate_s"),
                            rs.getObject("alert_resolved_at", OffsetDateTime.class)))
                    .list();
        });
    }

    private String jsonOrNull(Object value) {
        return value == null ? null : json.writeValueAsString(value);
    }

    private static String str(Object value) {
        return value == null ? null : String.valueOf(value);
    }

    private void inTenant(String tenant, Runnable work) {
        tx.executeWithoutResult(s -> {
            bind(tenant);
            work.run();
        });
    }

    private void bind(String tenant) {
        jdbc.sql("SELECT set_config('app.tenant_id', ?, true)").param(tenant).query(String.class).single();
    }
}
