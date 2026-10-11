package dev.sentinel.gateway.mcp;

import java.time.Duration;
import java.util.Map;

import dev.sentinel.gateway.identity.ServiceTokens;
import org.springframework.stereotype.Component;

/**
 * Mints downscoped, short-lived tokens for the agent's MCP tool calls.
 *
 * The agent never sees the user's token. It gets one that can only read knowledge and telemetry
 * ({@code scope=knowledge:read telemetry:read}), only through MCP servers ({@code aud=sentinel-mcp}),
 * only for one tenant, for 15 minutes. ES256-signed by the gateway: MCP servers verify it with the
 * gateway's public keys and can't mint one. A leaked tool token can't open or approve incidents.
 */
@Component
public class ToolTokens {

    public static final String AUDIENCE = "sentinel-mcp";
    public static final String SCOPE = "knowledge:read";
    public static final String TELEMETRY_SCOPE = "telemetry:read";
    static final Duration TTL = Duration.ofMinutes(15);

    private final ServiceTokens tokens;

    public ToolTokens(ServiceTokens tokens) {
        this.tokens = tokens;
    }

    public String mint(String tenantId, String incidentId) {
        return tokens.mint("agent:" + incidentId, AUDIENCE, TTL,
                Map.of("tenant_id", tenantId, "scope", SCOPE + " " + TELEMETRY_SCOPE)).token();
    }
}
