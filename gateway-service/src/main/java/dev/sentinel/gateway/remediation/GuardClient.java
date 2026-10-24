package dev.sentinel.gateway.remediation;

import java.io.IOException;
import java.net.http.HttpClient;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.HashMap;
import java.util.Map;

import dev.sentinel.gateway.config.SentinelProperties;
import org.springframework.core.ParameterizedTypeReference;
import org.springframework.http.HttpStatus;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.client.JdkClientHttpRequestFactory;
import org.springframework.stereotype.Component;
import org.springframework.web.client.ResourceAccessException;
import org.springframework.web.client.RestClient;
import org.springframework.web.client.RestClientResponseException;

/**
 * Records a reviewer's decision in Sentinel Guard, as a <em>delegate</em>: the gateway has authenticated
 * the person (OIDC) and enforced their tenant, and the guard records the decision as theirs, "via" the
 * gateway. The guard then issues the single-use capability to the agent; the gateway never holds one
 * (since Phase 11 it mints no capabilities at all).
 *
 * The gateway authenticates to the guard with its own token: a projected ServiceAccount token in
 * Kubernetes (re-read on every call, kubelet rotates it) or a static token for a single-process guard.
 */
@Component
public class GuardClient {

    public static class GuardException extends RuntimeException {
        private final HttpStatusCode status;

        public GuardException(HttpStatusCode status, String message) {
            super(message);
            this.status = status;
        }

        public HttpStatusCode status() {
            return status;
        }
    }

    private static final ParameterizedTypeReference<Map<String, Object>> JSON = new ParameterizedTypeReference<>() {
    };

    private final RestClient http;
    private final SentinelProperties.Guard config;

    public GuardClient(RestClient.Builder builder, SentinelProperties props) {
        this.config = props.guard();
        var jdk = HttpClient.newBuilder().version(HttpClient.Version.HTTP_1_1).connectTimeout(Duration.ofSeconds(2)).build();
        var factory = new JdkClientHttpRequestFactory(jdk);
        factory.setReadTimeout(Duration.ofSeconds(10));
        this.http = builder.clone().baseUrl(config.url()).requestFactory(factory).build();
    }

    /** Approves or denies the guard request the agent registered for this incident's proposal. */
    public Map<String, Object> decide(String requestId, boolean approve, String approver, String note) {
        Map<String, Object> body = new HashMap<>();
        body.put("approver", approver);
        body.put("note", note == null ? "" : note);
        try {
            return http.post().uri("/v1/actions/{id}/{verb}", requestId, approve ? "approve" : "deny")
                    .headers(h -> h.setBearerAuth(token())).body(body).retrieve().body(JSON);
        } catch (RestClientResponseException e) {
            // 409: already decided or expired; 403: refused (e.g. the gateway isn't a configured delegate).
            String detail = e.getResponseBodyAs(JSON) instanceof Map<?, ?> m && m.get("error") != null
                    ? String.valueOf(m.get("error")) : e.getStatusText();
            throw new GuardException(e.getStatusCode(), detail);
        } catch (ResourceAccessException e) {
            throw new GuardException(HttpStatus.SERVICE_UNAVAILABLE, "unavailable, so nothing was approved or resumed");
        }
    }

    private String token() {
        if (config.tokenFile() != null && !config.tokenFile().isBlank()) {
            try {
                return Files.readString(Path.of(config.tokenFile())).trim();
            } catch (IOException e) {
                throw new GuardException(HttpStatus.SERVICE_UNAVAILABLE, "cannot read the guard token: " + e.getMessage());
            }
        }
        return config.token() == null ? "" : config.token();
    }
}
