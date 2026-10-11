package dev.sentinel.gateway.integration;

import java.time.Instant;
import java.time.OffsetDateTime;
import java.time.ZoneOffset;
import java.util.List;

import org.springframework.jdbc.core.simple.JdbcClient;
import org.springframework.stereotype.Repository;
import org.springframework.transaction.support.TransactionTemplate;

/** Issued integration tokens (by jti only), RLS-scoped. A token is accepted only while active here. */
@Repository
public class IntegrationTokenRepository {

    public record Row(String jti, String kind, String name, String createdBy, OffsetDateTime createdAt,
                      OffsetDateTime expiresAt, OffsetDateTime revokedAt, String revokedBy,
                      OffsetDateTime lastUsedAt) {
    }

    private final JdbcClient jdbc;
    private final TransactionTemplate tx;

    public IntegrationTokenRepository(JdbcClient jdbc, TransactionTemplate tx) {
        this.jdbc = jdbc;
        this.tx = tx;
    }

    public void record(String tenant, String jti, String kind, String name, String createdBy, Instant expiresAt) {
        tx.executeWithoutResult(s -> {
            bind(tenant);
            jdbc.sql("""
                    INSERT INTO integration_tokens (jti, tenant_id, kind, name, created_by, expires_at)
                    VALUES (?, ?, ?, ?, ?, ?)""")
                    .params(jti, tenant, kind, name, createdBy, OffsetDateTime.ofInstant(expiresAt, ZoneOffset.UTC))
                    .update();
        });
    }

    /** Active = issued for this tenant, not revoked, not expired. Marks it used. */
    public boolean useIfActive(String tenant, String jti) {
        return Boolean.TRUE.equals(tx.execute(s -> {
            bind(tenant);
            return jdbc.sql("""
                    UPDATE integration_tokens SET last_used_at = now()
                    WHERE tenant_id = ? AND jti = ? AND revoked_at IS NULL AND expires_at > now()""")
                    .params(tenant, jti).update() == 1;
        }));
    }

    public boolean revoke(String tenant, String jti, String by) {
        return Boolean.TRUE.equals(tx.execute(s -> {
            bind(tenant);
            return jdbc.sql("""
                    UPDATE integration_tokens SET revoked_at = now(), revoked_by = ?
                    WHERE tenant_id = ? AND jti = ? AND revoked_at IS NULL""")
                    .params(by, tenant, jti).update() == 1;
        }));
    }

    public List<Row> list(String tenant) {
        return tx.execute(s -> {
            bind(tenant);
            return jdbc.sql("""
                    SELECT jti, kind, name, created_by, created_at, expires_at, revoked_at, revoked_by, last_used_at
                    FROM integration_tokens WHERE tenant_id = ? ORDER BY created_at DESC""")
                    .param(tenant)
                    .query((rs, n) -> new Row(rs.getString("jti"), rs.getString("kind"), rs.getString("name"),
                            rs.getString("created_by"), rs.getObject("created_at", OffsetDateTime.class),
                            rs.getObject("expires_at", OffsetDateTime.class),
                            rs.getObject("revoked_at", OffsetDateTime.class), rs.getString("revoked_by"),
                            rs.getObject("last_used_at", OffsetDateTime.class)))
                    .list();
        });
    }

    private void bind(String tenant) {
        jdbc.sql("SELECT set_config('app.tenant_id', ?, true)").param(tenant).query(String.class).single();
    }
}
