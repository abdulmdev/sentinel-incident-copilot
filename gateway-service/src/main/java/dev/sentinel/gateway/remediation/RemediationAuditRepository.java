package dev.sentinel.gateway.remediation;

import java.util.List;
import java.util.UUID;

import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Repository;
import org.springframework.transaction.support.TransactionTemplate;

/** Append-only audit of remediations that reached execution (RLS-scoped like every tenant table). */
@Repository
public class RemediationAuditRepository {

    public record Entry(UUID incidentId, String action, String target, String approvedBy, String capabilityJti,
                        boolean executed, Boolean verified, String outcome, String error) {
    }

    private final JdbcClient jdbc;
    private final TransactionTemplate tx;

    public RemediationAuditRepository(JdbcClient jdbc, TransactionTemplate tx) {
        this.jdbc = jdbc;
        this.tx = tx;
    }

    public void record(String tenant, Entry e) {
        tx.executeWithoutResult(s -> {
            bind(tenant);
            jdbc.sql("""
                    INSERT INTO remediation_audit (tenant_id, incident_id, action, target, approved_by, capability_jti,
                                                   executed, verified, outcome, error)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""")
                    .params(tenant, e.incidentId(), e.action(), e.target(), e.approvedBy(), e.capabilityJti(),
                            e.executed(), e.verified(), e.outcome(), e.error())
                    .update();
        });
    }

    public List<Entry> forIncident(String tenant, UUID incidentId) {
        return tx.execute(s -> {
            bind(tenant);
            return jdbc.sql("""
                    SELECT incident_id, action, target, approved_by, capability_jti, executed, verified, outcome, error
                    FROM remediation_audit WHERE tenant_id = ? AND incident_id = ? ORDER BY id""")
                    .params(tenant, incidentId)
                    .query((rs, n) -> new Entry(rs.getObject("incident_id", UUID.class), rs.getString("action"),
                            rs.getString("target"), rs.getString("approved_by"), rs.getString("capability_jti"),
                            rs.getBoolean("executed"), (Boolean) rs.getObject("verified"), rs.getString("outcome"),
                            rs.getString("error")))
                    .list();
        });
    }

    private void bind(String tenant) {
        jdbc.sql("SELECT set_config('app.tenant_id', ?, true)").param(tenant).query(String.class).single();
    }
}
