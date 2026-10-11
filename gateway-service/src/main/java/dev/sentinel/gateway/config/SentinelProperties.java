package dev.sentinel.gateway.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

/**
 * @param agentUrl          base URL of the LangGraph agent-service
 * @param requestsPerMinute per-tenant request budget
 */
@ConfigurationProperties(prefix = "sentinel")
public record SentinelProperties(String agentUrl, int requestsPerMinute) {
}
