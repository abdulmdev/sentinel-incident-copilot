package dev.sentinel.gateway.incident;

import java.time.Duration;
import java.util.Map;

import dev.sentinel.gateway.identity.ServiceTokens;
import org.springframework.stereotype.Component;

/**
 * Per-request credentials for gateway → agent calls. The agent no longer trusts the network:
 * every call carries a 60-second ES256 token for one tenant, and the agent checks that the tenant
 * in the URL or body is the tenant in the token.
 */
@Component
public class AgentTokens {

    public static final String AUDIENCE = "sentinel-agent";
    static final Duration TTL = Duration.ofSeconds(60);

    private final ServiceTokens tokens;

    public AgentTokens(ServiceTokens tokens) {
        this.tokens = tokens;
    }

    public String mint(String tenantId) {
        return tokens.mint("gateway", AUDIENCE, TTL, Map.of("tenant_id", tenantId, "scope", "incidents:write")).token();
    }
}
