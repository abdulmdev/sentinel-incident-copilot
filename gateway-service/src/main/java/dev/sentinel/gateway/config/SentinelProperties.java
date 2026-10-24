package dev.sentinel.gateway.config;

import org.springframework.boot.context.properties.ConfigurationProperties;

/**
 * @param agentUrl          base URL of the LangGraph agent-service
 * @param requestsPerMinute per-tenant request budget
 * @param guard             Sentinel Guard, where reviewers' decisions are recorded
 */
@ConfigurationProperties(prefix = "sentinel")
public record SentinelProperties(String agentUrl, int requestsPerMinute, Guard guard) {

    /**
     * @param url       the guard's action API (in Kubernetes: its in-cluster plain-HTTP port)
     * @param token     a static delegate token (single-process guard)
     * @param tokenFile a projected ServiceAccount token (Kubernetes); takes precedence over {@code token}
     */
    public record Guard(String url, String token, String tokenFile) {
    }
}
