package dev.sentinel.gateway.integration;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.ArgumentMatchers.any;
import static org.mockito.Mockito.when;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.delete;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.List;

import javax.crypto.spec.SecretKeySpec;

import com.nimbusds.jose.jwk.source.ImmutableSecret;
import dev.sentinel.gateway.alert.AlertService;
import dev.sentinel.gateway.identity.RoutingJwtDecoder;
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
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;
import org.testcontainers.postgresql.PostgreSQLContainer;
import org.testcontainers.utility.DockerImageName;
import tools.jackson.databind.ObjectMapper;

/** Issue → use → revoke, through the real filter chain and real Postgres (V7, RLS). */
@Testcontainers
@AutoConfigureMockMvc
@SpringBootTest(properties = {
        "spring.flyway.enabled=true",
        "spring.flyway.locations=filesystem:../ingestion-service/src/main/resources/db/migration",
        "spring.flyway.placeholders.app_password=sentinel_app"})
class IntegrationTokensIT {

    @Container
    static PostgreSQLContainer pg = new PostgreSQLContainer(
            DockerImageName.parse("pgvector/pgvector:pg16").asCompatibleSubstituteFor("postgres"));

    @DynamicPropertySource
    static void props(DynamicPropertyRegistry r) {
        r.add("spring.datasource.url", pg::getJdbcUrl);
        r.add("spring.flyway.user", pg::getUsername);
        r.add("spring.flyway.password", pg::getPassword);
    }

    @Autowired
    MockMvc mvc;

    @Autowired
    RoutingJwtDecoder decoder;

    @Autowired
    IntegrationTokenRepository repo;

    @MockitoBean
    AlertService alerts;

    static String user(String sub, String tenant, String... roles) {
        var key = new SecretKeySpec("dev-only-secret-change-me-0123456789abcdef".getBytes(StandardCharsets.UTF_8),
                "HmacSHA256");
        var claims = JwtClaimsSet.builder().subject(sub).claim("tenant_id", tenant).claim("roles", List.of(roles))
                .issuedAt(Instant.now()).expiresAt(Instant.now().plusSeconds(600)).build();
        return new NimbusJwtEncoder(new ImmutableSecret<>(key))
                .encode(JwtEncoderParameters.from(JwsHeader.with(MacAlgorithm.HS256).build(), claims)).getTokenValue();
    }

    String issue(String tenant) throws Exception {
        String body = mvc.perform(post("/api/v1/integrations/alertmanager")
                        .header("Authorization", "Bearer " + user("admin@" + tenant, tenant, "tenant-admin"))
                        .contentType(MediaType.APPLICATION_JSON).content("{\"name\":\"prod alertmanager\",\"ttlDays\":7}"))
                .andExpect(status().isCreated())
                .andReturn().getResponse().getContentAsString();
        return new ObjectMapper().readTree(body).get("token").asString();
    }

    int webhook(String token) throws Exception {
        return mvc.perform(post("/api/v1/alerts/alertmanager").header("Authorization", "Bearer " + token)
                        .contentType(MediaType.APPLICATION_JSON).content("{\"alerts\":[]}"))
                .andReturn().getResponse().getStatus();
    }

    @Test
    void issuedTokenWorksOnTheWebhookUntilRevoked() throws Exception {
        when(alerts.handle(any(), any())).thenReturn(new AlertService.Result(List.of(), 0, 0, 0, List.of()));
        String token = issue("acme");
        var jwt = decoder.decode(token);
        assertThat(jwt.getAudience()).containsExactly("sentinel-alerts");
        assertThat(jwt.getClaimAsString("tenant_id")).isEqualTo("acme");
        assertThat(jwt.getHeaders()).containsEntry("alg", "ES256");

        assertThat(webhook(token)).isEqualTo(202);
        assertThat(repo.list("acme")).singleElement().satisfies(r -> {
            assertThat(r.name()).isEqualTo("prod alertmanager");
            assertThat(r.createdBy()).isEqualTo("admin@acme");
            assertThat(r.lastUsedAt()).isNotNull();
        });

        mvc.perform(delete("/api/v1/integrations/" + jwt.getId())
                        .header("Authorization", "Bearer " + user("admin@acme", "acme", "tenant-admin")))
                .andExpect(status().isNoContent());
        assertThat(webhook(token)).isEqualTo(403);
    }

    @Test
    void onlyTenantAdminsCanIssueAndTokensStayInTheirTenant() throws Exception {
        mvc.perform(post("/api/v1/integrations/alertmanager")
                        .header("Authorization", "Bearer " + user("bob@acme", "acme"))
                        .contentType(MediaType.APPLICATION_JSON).content("{\"name\":\"x\"}"))
                .andExpect(status().isForbidden());

        String globexToken = issue("globex");
        String globexJti = decoder.decode(globexToken).getId();
        // An acme admin can neither see nor revoke globex's token.
        mvc.perform(get("/api/v1/integrations").header("Authorization", "Bearer " + user("admin@acme", "acme", "tenant-admin")))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$[?(@.jti == '" + globexJti + "')]").isEmpty());
        mvc.perform(delete("/api/v1/integrations/" + globexJti)
                        .header("Authorization", "Bearer " + user("admin@acme", "acme", "tenant-admin")))
                .andExpect(status().isNotFound());
    }

    @Test
    void expiredRecordIsNotActive() {
        repo.record("initech", "jti-old", "alertmanager", "old", "admin", Instant.now().minusSeconds(1));
        assertThat(repo.useIfActive("initech", "jti-old")).isFalse();
        repo.record("initech", "jti-new", "alertmanager", "new", "admin", Instant.now().plusSeconds(3600));
        assertThat(repo.useIfActive("initech", "jti-new")).isTrue();
        assertThat(repo.useIfActive("acme", "jti-new")).isFalse(); // RLS: another tenant's record doesn't exist
    }
}
