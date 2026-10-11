package dev.sentinel.gateway.incident;

import java.time.OffsetDateTime;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import dev.sentinel.gateway.identity.Principals;
import dev.sentinel.gateway.tenant.Tenants;
import jakarta.validation.Valid;
import org.springframework.security.core.annotation.AuthenticationPrincipal;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;

@RestController
@RequestMapping("/api/v1/incidents")
public class IncidentController {

    private final IncidentService service;
    private final IncidentRepository incidents;
    private final AgentClient agent;

    public IncidentController(IncidentService service, IncidentRepository incidents, AgentClient agent) {
        this.service = service;
        this.incidents = incidents;
        this.agent = agent;
    }

    @GetMapping
    public List<IncidentRepository.Row> list(@AuthenticationPrincipal Jwt jwt,
                                             @RequestParam(defaultValue = "50") int limit) {
        return incidents.list(Tenants.of(jwt), Math.clamp(limit, 1, 200));
    }

    @PostMapping
    public Map<String, Object> open(@AuthenticationPrincipal Jwt jwt, @Valid @RequestBody IncidentRequests.Open req) {
        var incident = new IncidentService.NewIncident(Tenants.of(jwt), req.service(), req.summary(),
                req.severity() == null ? "SEV2" : req.severity(), Principals.of(jwt), "console",
                OffsetDateTime.now(), null);
        // Record first: if the agent call fails, the incident still exists (as FAILED), never vanishes.
        UUID id = service.record(incident);
        return service.investigate(incident, id);
    }

    @GetMapping("/{id}")
    public Map<String, Object> get(@AuthenticationPrincipal Jwt jwt, @PathVariable UUID id) {
        return agent.get(Tenants.of(jwt), id.toString());
    }

    @PostMapping("/{id}/decision")
    public Map<String, Object> decide(@AuthenticationPrincipal Jwt jwt, @PathVariable UUID id,
                                      @Valid @RequestBody IncidentRequests.Decide req) {
        // The reviewer is the authenticated identity; approval mints the single-use remediation capability.
        return service.decide(Tenants.of(jwt), id, req.approved(), req.note(), Principals.of(jwt));
    }
}
