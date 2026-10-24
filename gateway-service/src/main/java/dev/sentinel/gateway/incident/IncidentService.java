package dev.sentinel.gateway.incident;

import java.time.OffsetDateTime;
import java.util.HashMap;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import dev.sentinel.gateway.alert.AlertLinkRepository;
import dev.sentinel.gateway.mcp.ToolTokens;
import dev.sentinel.gateway.remediation.GuardClient;
import dev.sentinel.gateway.remediation.RemediationAuditRepository;
import io.micrometer.context.ContextExecutorService;
import io.micrometer.context.ContextSnapshotFactory;
import jakarta.annotation.PreDestroy;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;

/**
 * The incident lifecycle, shared by humans (console/API) and alerts (Alertmanager).
 *
 * Open: record first (so a failure leaves FAILED, never nothing), mint the read-only tool token,
 * hand the incident to the agent. Decide: record the reviewer's decision in Sentinel Guard, on the
 * request the agent registered for its proposal (the guard then issues the agent a single-use
 * capability for exactly that action); resume the agent to execute and verify; audit the outcome.
 */
@Service
public class IncidentService {

    private static final Logger log = LoggerFactory.getLogger(IncidentService.class);

    private final AgentClient agent;
    private final ToolTokens toolTokens;
    private final GuardClient guard;
    private final IncidentRepository incidents;
    private final RemediationAuditRepository audit;
    private final AlertLinkRepository alertLinks;
    // Background investigations (alerts must be acknowledged within Alertmanager's timeout).
    // Context-propagating, so the trace started by the webhook continues into the investigation.
    private final ExecutorService background = ContextExecutorService.wrap(
            Executors.newVirtualThreadPerTaskExecutor(), ContextSnapshotFactory.builder().build()::captureAll);

    public IncidentService(AgentClient agent, ToolTokens toolTokens, GuardClient guard,
                           IncidentRepository incidents, RemediationAuditRepository audit,
                           AlertLinkRepository alertLinks) {
        this.agent = agent;
        this.toolTokens = toolTokens;
        this.guard = guard;
        this.incidents = incidents;
        this.audit = audit;
        this.alertLinks = alertLinks;
    }

    public record NewIncident(String tenant, String service, String summary, String severity, String openedBy,
                              String source, OffsetDateTime openedAt, String alertFingerprint) {
    }

    /** Records the incident; the investigation then runs synchronously ({@link #investigate}) or in the background. */
    public UUID record(NewIncident n) {
        UUID id = UUID.randomUUID();
        incidents.create(n.tenant(), id, n.service(), n.summary(), n.severity(), n.openedBy(), n.source(),
                n.openedAt(), n.alertFingerprint());
        return id;
    }

    public Map<String, Object> investigate(NewIncident n, UUID id) {
        Map<String, Object> payload = new HashMap<>();
        payload.put("id", id.toString());
        payload.put("tenant_id", n.tenant());
        payload.put("service", n.service());
        payload.put("summary", n.summary());
        payload.put("severity", n.severity());
        payload.put("opened_at", n.openedAt().toInstant().toString());
        try {
            // Downscoped credential for the agent's MCP calls (runbook search), never the user's token.
            Map<String, Object> view = agent.open(payload, toolTokens.mint(n.tenant(), id.toString()));
            incidents.update(n.tenant(), id, view, null);
            return view;
        } catch (RuntimeException e) {
            incidents.markFailed(n.tenant(), id);
            alertLinks.deactivate(n.tenant(), id);
            throw e;
        }
    }

    public void investigateInBackground(NewIncident n, UUID id) {
        background.submit(() -> {
            try {
                investigate(n, id);
            } catch (RuntimeException e) {
                log.warn("background investigation of incident {} failed: {}", id, e.getMessage());
            }
        });
    }

    @SuppressWarnings("unchecked")
    public Map<String, Object> decide(String tenant, UUID id, boolean approved, String note, String reviewer) {
        Map<String, Object> current = agent.get(tenant, id.toString());
        Map<String, Object> rem = current.get("rca") instanceof Map<?, ?> rca && rca.get("remediation") instanceof Map<?, ?> r
                ? (Map<String, Object>) r : Map.of();
        String action = (String) rem.get("action");
        String target = (String) rem.get("target");
        String guardRequest = current.get("approval") instanceof Map<?, ?> a ? (String) a.get("request") : null;

        // Record the decision in the guard first: if it refuses (expired, already decided, guard down),
        // nothing is resumed and the incident keeps waiting. A rejection is recorded too, so that
        // request can never be approved later. Escalations register nothing (nothing to execute).
        if (guardRequest != null) {
            guard.decide(guardRequest, approved, reviewer, note);
        }
        Map<String, Object> decision = new HashMap<>();
        decision.put("approved", approved);
        decision.put("reviewer", reviewer); // audit trail uses the authenticated identity
        decision.put("note", note);
        Map<String, Object> view = agent.decide(tenant, id.toString(), decision, toolTokens.mint(tenant, id.toString()));
        incidents.update(tenant, id, view, reviewer);

        String status = String.valueOf(view.get("status"));
        if (view.get("execution") instanceof Map<?, ?> exec) {
            Boolean verified = view.get("verification") instanceof Map<?, ?> v ? (Boolean) v.get("healthy") : null;
            audit.record(tenant, new RemediationAuditRepository.Entry(id, action, target, reviewer,
                    (String) exec.get("capability_jti"), guardRequest, Boolean.TRUE.equals(exec.get("executed")),
                    verified, status, (String) exec.get("error")));
        }
        if (IncidentRepository.FINAL.contains(status)) {
            alertLinks.deactivate(tenant, id);
        }
        return view;
    }

    @PreDestroy
    void shutdown() {
        background.shutdown();
    }
}
