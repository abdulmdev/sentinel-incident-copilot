package dev.sentinel.gateway.incident;

import java.net.http.HttpClient;
import java.time.Duration;
import java.util.Map;

import dev.sentinel.gateway.config.SentinelProperties;
import org.springframework.core.ParameterizedTypeReference;
import org.springframework.http.client.JdkClientHttpRequestFactory;
import org.springframework.resilience.annotation.Retryable;
import org.springframework.stereotype.Component;
import org.springframework.web.client.ResourceAccessException;
import org.springframework.web.client.RestClient;

/**
 * HTTP client for the Python agent-service.
 *
 * Only network failures are retried. Opening an incident is idempotent because the agent
 * keys on (tenant, incident id) and returns 409 on a duplicate, so a retry after a timeout
 * cannot start the workflow twice.
 */
@Component
public class AgentClient {

    private static final ParameterizedTypeReference<Map<String, Object>> JSON = new ParameterizedTypeReference<>() {
    };

    private final RestClient http;
    private final AgentTokens tokens;

    public AgentClient(RestClient.Builder builder, SentinelProperties props, AgentTokens tokens) {
        this.tokens = tokens;
        // HTTP/1.1: the JDK client otherwise tries an h2c upgrade that uvicorn rejects.
        var jdk = HttpClient.newBuilder()
                .version(HttpClient.Version.HTTP_1_1)
                .connectTimeout(Duration.ofSeconds(2))
                .build();
        var factory = new JdkClientHttpRequestFactory(jdk);
        factory.setReadTimeout(Duration.ofSeconds(120)); // RCA on the large model can take a while
        this.http = builder.baseUrl(props.agentUrl()).requestFactory(factory).build();
    }

    @Retryable(includes = ResourceAccessException.class, maxRetries = 2, delay = 200, multiplier = 2)
    public Map<String, Object> open(Map<String, Object> incident, String toolToken) {
        // Sent as a header, not in the body: the agent must never checkpoint the credential.
        return http.post().uri("/incidents").header("X-Sentinel-Tool-Token", toolToken)
                .headers(h -> h.setBearerAuth(tokens.mint(String.valueOf(incident.get("tenant_id")))))
                .body(incident).retrieve().body(JSON);
    }

    @Retryable(includes = ResourceAccessException.class, maxRetries = 2, delay = 200, multiplier = 2)
    public Map<String, Object> get(String tenant, String id) {
        return http.get().uri("/tenants/{t}/incidents/{id}", tenant, id)
                .headers(h -> h.setBearerAuth(tokens.mint(tenant))).retrieve().body(JSON);
    }

    public Map<String, Object> decide(String tenant, String id, Map<String, Object> decision, String capability,
                                      String toolToken) {
        // Not retried: a decision is a state change the caller should see fail and confirm again.
        // Credentials go in headers so the agent never checkpoints them: the capability (if any) for
        // execution, the tool token for verification probes.
        var request = http.post().uri("/tenants/{t}/incidents/{id}/decision", tenant, id)
                .headers(h -> h.setBearerAuth(tokens.mint(tenant)))
                .header("X-Sentinel-Tool-Token", toolToken);
        if (capability != null) {
            request = request.header("X-Sentinel-Remediation-Token", capability);
        }
        return request.body(decision).retrieve().body(JSON);
    }
}
