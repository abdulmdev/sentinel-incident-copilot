package dev.sentinel.gateway.incident;

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.ArgumentMatchers.isNull;
import static org.mockito.Mockito.never;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;
import static org.springframework.security.test.web.servlet.request.SecurityMockMvcRequestPostProcessors.jwt;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import java.util.Map;

import dev.sentinel.gateway.alert.AlertLinkRepository;
import dev.sentinel.gateway.remediation.RemediationAuditRepository;
import dev.sentinel.gateway.remediation.RemediationTokens;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.web.client.HttpClientErrorException;

@SpringBootTest(properties = {"spring.ai.anthropic.api-key=test", "sentinel.requests-per-minute=1000"})
@AutoConfigureMockMvc
class IncidentControllerTest {

    private static final String ID = "7f9c2c4e-1a2b-4c3d-9e8f-0a1b2c3d4e5f";

    @Autowired
    MockMvc mvc;

    @MockitoBean
    AgentClient agent;

    @MockitoBean
    IncidentRepository incidents;

    @MockitoBean
    RemediationAuditRepository audit;

    @MockitoBean
    AlertLinkRepository alertLinks;

    @MockitoBean
    RemediationTokens remediationTokens;

    private static final Map<String, Object> AWAITING_ROLLBACK = Map.of("status", "AWAITING_APPROVAL",
            "rca", Map.of("remediation", Map.of("action", "rollback", "target", "checkout")));

    @org.junit.jupiter.api.BeforeEach
    void stubs() {
        when(agent.get(any(), any())).thenReturn(AWAITING_ROLLBACK);
        when(remediationTokens.mint(any(), any(), any(), any(), any()))
                .thenReturn(new RemediationTokens.Capability("cap-token", "jti-1"));
    }

    @Test
    void rejectsUnauthenticatedRequests() throws Exception {
        mvc.perform(get("/api/v1/incidents/" + ID)).andExpect(status().isUnauthorized());
    }

    @Test
    void tenantComesFromTokenNotRequestBody() throws Exception {
        when(agent.open(any(), any())).thenReturn(Map.of("status", "AWAITING_APPROVAL"));

        mvc.perform(post("/api/v1/incidents")
                        .with(jwt().jwt(j -> j.claim("tenant_id", "acme")))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("""
                                {"service":"checkout","summary":"5xx spike","severity":"SEV1","tenant_id":"globex"}"""))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$.status").value("AWAITING_APPROVAL"));

        verify(agent).open(argThat(m -> "acme".equals(m.get("tenant_id")) && "checkout".equals(m.get("service"))),
                argThat(token -> token != null && !token.isBlank()));
    }

    @Test
    void agentPayloadMatchesSharedContract() throws Exception {
        when(agent.open(any(), any())).thenReturn(Map.of());
        mvc.perform(post("/api/v1/incidents")
                .with(jwt().jwt(j -> j.claim("tenant_id", "acme")))
                .contentType(MediaType.APPLICATION_JSON)
                .content("{\"service\":\"checkout\",\"summary\":\"5xx\",\"severity\":\"SEV1\"}"));

        var contract = new tools.jackson.databind.ObjectMapper().readTree(
                java.nio.file.Path.of("../contracts/agent-open-incident.request.json").toFile());
        var expectedKeys = new java.util.TreeSet<String>(contract.propertyNames());
        verify(agent).open(argThat(m -> new java.util.TreeSet<>(m.keySet()).equals(expectedKeys)), any());
    }

    @Test
    void reviewerIsTheAuthenticatedSubject() throws Exception {
        when(agent.decide(eq("acme"), eq(ID), any(), any(), any())).thenReturn(Map.of("status", "RESOLVED"));

        mvc.perform(post("/api/v1/incidents/" + ID + "/decision")
                        .with(jwt().jwt(j -> j.subject("alice@acme").claim("tenant_id", "acme")))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"approved\":true}"))
                .andExpect(status().isOk());

        verify(agent).decide(eq("acme"), eq(ID), argThat(d -> "alice@acme".equals(d.get("reviewer"))), any(), any());
    }

    @Test
    void tokenWithoutTenantIsForbidden() throws Exception {
        mvc.perform(get("/api/v1/incidents/" + ID).with(jwt()))
                .andExpect(status().isForbidden());
    }

    @Test
    void agentConflictIsPassedThrough() throws Exception {
        when(agent.decide(any(), any(), any(), any(), any()))
                .thenThrow(HttpClientErrorException.create(HttpStatus.CONFLICT, "Conflict", null, null, null));

        mvc.perform(post("/api/v1/incidents/" + ID + "/decision")
                        .with(jwt().jwt(j -> j.claim("tenant_id", "initech")))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"approved\":true}"))
                .andExpect(status().isConflict());
    }

    @Test
    void invalidServiceNameIsRejected() throws Exception {
        mvc.perform(post("/api/v1/incidents")
                        .with(jwt().jwt(j -> j.claim("tenant_id", "umbrella")))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"service\":\"../etc\",\"summary\":\"x\"}"))
                .andExpect(status().isBadRequest());
    }

    @Test
    void agentOutageLeavesAFailedRecordNotNothing() throws Exception {
        when(agent.open(any(), any())).thenThrow(new org.springframework.web.client.ResourceAccessException("down"));

        mvc.perform(post("/api/v1/incidents")
                        .with(jwt().jwt(j -> j.subject("alice@acme").claim("tenant_id", "acme")))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"service\":\"checkout\",\"summary\":\"5xx\"}"))
                .andExpect(status().isServiceUnavailable());

        verify(incidents).create(eq("acme"), any(), eq("checkout"), eq("5xx"), eq("SEV2"), eq("alice@acme"),
                eq("console"), any(), isNull());
        verify(incidents).markFailed(eq("acme"), any());
    }

    @Test
    void decisionIsRecordedWithTheReviewer() throws Exception {
        when(agent.decide(eq("acme"), eq(ID), any(), any(), any())).thenReturn(Map.of("status", "RESOLVED"));

        mvc.perform(post("/api/v1/incidents/" + ID + "/decision")
                        .with(jwt().jwt(j -> j.subject("bob@acme").claim("tenant_id", "acme")))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"approved\":true}"))
                .andExpect(status().isOk());

        verify(incidents).update(eq("acme"), eq(java.util.UUID.fromString(ID)), eq(Map.of("status", "RESOLVED")), eq("bob@acme"));
    }

    @Test
    void listIsScopedToTheTokensTenant() throws Exception {
        when(incidents.list("acme", 50)).thenReturn(java.util.List.of());
        mvc.perform(get("/api/v1/incidents").with(jwt().jwt(j -> j.claim("tenant_id", "acme"))))
                .andExpect(status().isOk());
        verify(incidents).list("acme", 50);
    }

    @Test
    void approvalMintsACapabilityBoundToTheProposedActionAndPassesItToTheAgent() throws Exception {
        when(agent.decide(any(), any(), any(), any(), any())).thenReturn(Map.of("status", "RESOLVED"));

        decide("carol@acme", true);

        verify(remediationTokens).mint("acme", ID, "rollback", "checkout", "carol@acme");
        verify(agent).decide(eq("acme"), eq(ID), any(), eq("cap-token"), any());
    }

    @Test
    void rejectionMintsNoCapability() throws Exception {
        when(agent.decide(any(), any(), any(), any(), any())).thenReturn(Map.of("status", "CLOSED_NO_ACTION"));

        decide("carol@acme", false);

        verify(remediationTokens, never()).mint(any(), any(), any(), any(), any());
        verify(agent).decide(eq("acme"), eq(ID), any(), isNull(), any());
    }

    @Test
    void approvedEscalationIsNotExecutableSoMintsNoCapability() throws Exception {
        when(agent.get(any(), any())).thenReturn(Map.of("status", "AWAITING_APPROVAL",
                "rca", Map.of("remediation", Map.of("action", "escalate", "target", "checkout"))));
        when(agent.decide(any(), any(), any(), any(), any())).thenReturn(Map.of("status", "ESCALATED"));

        decide("carol@acme", true);

        verify(remediationTokens, never()).mint(any(), any(), any(), any(), any());
        verify(agent).decide(eq("acme"), eq(ID), any(), isNull(), any());
        verify(audit, never()).record(any(), any());
    }

    @Test
    void executionIsAuditedWithTheCapabilityAndVerificationOutcome() throws Exception {
        when(agent.decide(any(), any(), any(), any(), any())).thenReturn(Map.of("status", "RESOLVED",
                "execution", Map.of("executed", true), "verification", Map.of("healthy", true)));

        decide("carol@acme", true);

        verify(audit).record("acme", new RemediationAuditRepository.Entry(java.util.UUID.fromString(ID),
                "rollback", "checkout", "carol@acme", "jti-1", true, true, "RESOLVED", null));
        verify(alertLinks).deactivate("acme", java.util.UUID.fromString(ID)); // final: the alert may open a new incident
    }

    @Test
    void failedRemediationIsAuditedAndKeepsTheAlertLinked() throws Exception {
        when(agent.decide(any(), any(), any(), any(), any())).thenReturn(Map.of("status", "REMEDIATION_FAILED",
                "execution", Map.of("executed", false, "error", "capability already used")));

        decide("carol@acme", true);

        verify(audit).record(eq("acme"), argThat(e -> !e.executed() && e.verified() == null
                && "capability already used".equals(e.error()) && "REMEDIATION_FAILED".equals(e.outcome())));
        verify(alertLinks, never()).deactivate(any(), any());
    }

    private void decide(String reviewer, boolean approved) throws Exception {
        mvc.perform(post("/api/v1/incidents/" + ID + "/decision")
                        .with(jwt().jwt(j -> j.subject(reviewer).claim("tenant_id", "acme")))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"approved\":" + approved + "}"))
                .andExpect(status().isOk());
    }
}
