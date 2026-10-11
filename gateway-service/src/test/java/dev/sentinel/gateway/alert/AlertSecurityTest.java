package dev.sentinel.gateway.alert;

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.List;

import javax.crypto.spec.SecretKeySpec;

import com.nimbusds.jose.jwk.source.ImmutableSecret;
import dev.sentinel.gateway.identity.ServiceTokens;
import dev.sentinel.gateway.incident.IncidentRepository;
import dev.sentinel.gateway.integration.IntegrationTokenRepository;
import dev.sentinel.gateway.mcp.ToolTokens;
import dev.sentinel.gateway.remediation.RemediationTokens;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.http.MediaType;
import org.springframework.security.oauth2.jose.jws.MacAlgorithm;
import org.springframework.security.oauth2.jwt.JwsHeader;
import org.springframework.security.oauth2.jwt.JwtClaimsSet;
import org.springframework.security.oauth2.jwt.JwtEncoderParameters;
import org.springframework.security.oauth2.jwt.NimbusJwtEncoder;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.request.MockHttpServletRequestBuilder;

/** Real signed tokens through Spring Security: each credential works on its own endpoint only. */
@SpringBootTest
@AutoConfigureMockMvc
class AlertSecurityTest {

    @Autowired
    MockMvc mvc;

    @Autowired
    ToolTokens toolTokens;

    @Autowired
    RemediationTokens remediationTokens;

    @MockitoBean
    AlertService alerts;

    @MockitoBean
    IncidentRepository incidents;

    @MockitoBean
    IntegrationTokenRepository integrations;

    @Autowired
    ServiceTokens serviceTokens;

    /** What a tenant admin gets from POST /api/v1/integrations/alertmanager. */
    String integration(String tenant) {
        var minted = serviceTokens.mint("integration:alertmanager:prod", "sentinel-alerts", java.time.Duration.ofDays(30),
                java.util.Map.of("tenant_id", tenant, "scope", "alerts:write"));
        when(integrations.useIfActive(tenant, minted.jti())).thenReturn(true);
        return minted.token();
    }

    static final String BODY = """
            {"version":"4","status":"firing","receiver":"sentinel","alerts":[{"status":"firing",
             "labels":{"alertname":"CheckoutErrors","service":"checkout"},"annotations":{},
             "startsAt":"2026-10-06T10:00:00Z","fingerprint":"abc"}]}""";

    static String token(String tenant, String scope, List<String> audience) {
        var key = new SecretKeySpec("dev-only-secret-change-me-0123456789abcdef".getBytes(StandardCharsets.UTF_8),
                "HmacSHA256");
        var claims = JwtClaimsSet.builder().subject("integration:alertmanager").claim("tenant_id", tenant)
                .issuedAt(Instant.now()).expiresAt(Instant.now().plusSeconds(600));
        if (scope != null) {
            claims.claim("scope", scope);
        }
        if (audience != null) {
            claims.audience(audience);
        }
        return new NimbusJwtEncoder(new ImmutableSecret<>(key))
                .encode(JwtEncoderParameters.from(JwsHeader.with(MacAlgorithm.HS256).build(), claims.build()))
                .getTokenValue();
    }


    static MockHttpServletRequestBuilder webhook(String bearer) {
        return post("/api/v1/alerts/alertmanager").header("Authorization", "Bearer " + bearer)
                .contentType(MediaType.APPLICATION_JSON).content(BODY);
    }

    @Test
    void integrationTokenIsAcceptedAndTheTenantComesFromIt() throws Exception {
        when(alerts.handle(any(), any())).thenReturn(new AlertService.Result(List.of("id-1"), 0, 0, 0, List.of()));

        mvc.perform(webhook(integration("acme")))
                .andExpect(status().isAccepted())
                .andExpect(jsonPath("$.opened[0]").value("id-1"));

        verify(alerts).handle(eq("acme"), any());
    }

    @Test
    void webhookRequiresAuthentication() throws Exception {
        mvc.perform(post("/api/v1/alerts/alertmanager").contentType(MediaType.APPLICATION_JSON).content(BODY))
                .andExpect(status().isUnauthorized());
    }

    @Test
    void userTokenCannotPostAlerts() throws Exception {
        mvc.perform(webhook(token("acme", null, null))).andExpect(status().isForbidden());
    }

    @Test
    void userTokenClaimingTheAlertsScopeWithoutTheAudienceIsRejected() throws Exception {
        mvc.perform(webhook(token("acme", "alerts:write", null))).andExpect(status().isForbidden());
    }

    @Test
    void forgedIntegrationTokenSignedWithTheDevSecretIsRejected() throws Exception {
        // Phase 7 integration tokens were HS256 with the shared secret. Now only the gateway's key works.
        mvc.perform(webhook(token("acme", "alerts:write", List.of("sentinel-alerts"))))
                .andExpect(status().isUnauthorized());
    }

    @Test
    void revokedOrUnknownIntegrationTokenIsRejected() throws Exception {
        String token = integration("acme");
        when(integrations.useIfActive(any(), any())).thenReturn(false); // revoked in the meantime
        mvc.perform(webhook(token)).andExpect(status().isForbidden());
    }

    @Test
    void agentToolTokenCannotPostAlerts() throws Exception {
        mvc.perform(webhook(toolTokens.mint("acme", "inc-1"))).andExpect(status().isForbidden());
    }

    @Test
    void integrationTokenCannotReadIncidentsOrCallTools() throws Exception {
        mvc.perform(get("/api/v1/incidents").header("Authorization", "Bearer " + integration("acme")))
                .andExpect(status().isForbidden());
        mvc.perform(post("/mcp").header("Authorization", "Bearer " + integration("acme"))
                        .contentType(MediaType.APPLICATION_JSON)
                        .accept(MediaType.APPLICATION_JSON, MediaType.TEXT_EVENT_STREAM)
                        .content("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}"))
                .andExpect(status().isForbidden());
    }

    @Test
    void remediationCapabilityIsUselessAtTheGateway() throws Exception {
        String cap = remediationTokens.mint("acme", "inc-1", "rollback", "checkout", "alice").token();
        mvc.perform(get("/api/v1/incidents").header("Authorization", "Bearer " + cap)).andExpect(status().isForbidden());
        mvc.perform(webhook(cap)).andExpect(status().isForbidden());
    }
}
