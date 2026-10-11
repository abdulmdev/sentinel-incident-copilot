package dev.sentinel.gateway.incident;

import static org.assertj.core.api.Assertions.assertThat;
import static org.springframework.security.test.web.servlet.request.SecurityMockMvcRequestPostProcessors.jwt;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import java.io.IOException;
import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.util.Map;
import java.util.concurrent.ConcurrentHashMap;

import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.http.MediaType;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.web.servlet.MockMvc;

/** Real AgentClient against a stub agent: the trace and the scoped tool token must cross the hop. */
@SpringBootTest
@AutoConfigureMockMvc
class TracePropagationTest {

    static final Map<String, String> received = new ConcurrentHashMap<>();
    static final HttpServer agent;

    static {
        try {
            agent = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        } catch (IOException e) {
            throw new IllegalStateException(e);
        }
        agent.createContext("/incidents", exchange -> {
            exchange.getRequestHeaders().forEach((k, v) -> received.put(k.toLowerCase(), v.get(0)));
            byte[] body = "{\"status\":\"AWAITING_APPROVAL\"}".getBytes(StandardCharsets.UTF_8);
            exchange.getResponseHeaders().add("Content-Type", "application/json");
            exchange.sendResponseHeaders(200, body.length);
            exchange.getResponseBody().write(body);
            exchange.close();
        });
        agent.start();
    }

    @DynamicPropertySource
    static void props(DynamicPropertyRegistry r) {
        r.add("sentinel.agent-url", () -> "http://127.0.0.1:" + agent.getAddress().getPort());
    }

    @AfterAll
    static void stop() {
        agent.stop(0);
    }

    @Autowired
    MockMvc mvc;

    @Autowired
    dev.sentinel.gateway.identity.RoutingJwtDecoder decoder;

    @org.springframework.test.context.bean.override.mockito.MockitoBean
    IncidentRepository incidents;

    @Test
    void incidentCallToAgentCarriesW3cTraceContextAndToolToken() throws Exception {
        mvc.perform(post("/api/v1/incidents")
                        .with(jwt().jwt(j -> j.claim("tenant_id", "acme")))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"service\":\"checkout\",\"summary\":\"5xx\",\"severity\":\"SEV1\"}"))
                .andExpect(status().isOk());

        assertThat(received.get("traceparent")).matches("00-[0-9a-f]{32}-[0-9a-f]{16}-01");
        assertThat(received.get("x-sentinel-tool-token")).isNotBlank();
        // The agent no longer trusts the network: a per-request ES256 token, bound to the tenant.
        var agentToken = decoder.decode(received.get("authorization").substring("Bearer ".length()));
        assertThat(agentToken.getAudience()).containsExactly("sentinel-agent");
        assertThat(agentToken.getClaimAsString("tenant_id")).isEqualTo("acme");
        assertThat(agentToken.getExpiresAt()).isBefore(java.time.Instant.now().plusSeconds(61));
    }
}
