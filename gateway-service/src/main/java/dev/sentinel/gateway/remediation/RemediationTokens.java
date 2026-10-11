package dev.sentinel.gateway.remediation;

import java.time.Duration;
import java.util.Map;

import dev.sentinel.gateway.identity.ServiceTokens;
import org.springframework.stereotype.Component;

/**
 * Mints the capability that authorizes one remediation, at the moment a human approves it.
 *
 * Bound to one tenant, one incident, one action and one target service; valid 5 minutes;
 * single-use (the remediation MCP server records the jti). ES256 with a key only the gateway
 * holds: the adapter verifies against the gateway's JWKS, so nothing else (not the agent, not the
 * adapter itself) can create one. The agent can execute exactly what was approved, once.
 */
@Component
public class RemediationTokens {

    public static final String AUDIENCE = "sentinel-remediation";
    public static final String SCOPE = "remediation:execute";
    static final Duration TTL = Duration.ofMinutes(5);

    public record Capability(String token, String jti) {
    }

    private final ServiceTokens tokens;

    public RemediationTokens(ServiceTokens tokens) {
        this.tokens = tokens;
    }

    public Capability mint(String tenantId, String incidentId, String action, String target, String approvedBy) {
        var minted = tokens.mint("agent:" + incidentId, AUDIENCE, TTL, Map.of(
                "tenant_id", tenantId, "scope", SCOPE, "incident_id", incidentId,
                "action", action, "target", target, "approved_by", approvedBy));
        return new Capability(minted.token(), minted.jti());
    }
}
