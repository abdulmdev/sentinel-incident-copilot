package dev.sentinel.gateway.integration;

import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Map;

import dev.sentinel.gateway.alert.AlertController;
import dev.sentinel.gateway.identity.Principals;
import dev.sentinel.gateway.identity.ServiceTokens;
import dev.sentinel.gateway.tenant.Tenants;
import jakarta.validation.Valid;
import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.Min;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.Pattern;
import org.springframework.http.HttpStatus;
import org.springframework.security.core.annotation.AuthenticationPrincipal;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.web.bind.annotation.DeleteMapping;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.server.ResponseStatusException;

/**
 * Tenant admins issue, list and revoke integration tokens (Alertmanager today).
 * The token is returned once; only its jti is stored, and the alerts webhook accepts it only
 * while it's active here, so revocation is immediate.
 */
@RestController
@RequestMapping("/api/v1/integrations")
public class IntegrationController {

    public static final String ADMIN_ROLE = "tenant-admin";

    public record Create(@NotBlank @Pattern(regexp = "[A-Za-z0-9 ._-]{1,64}") String name,
                         @Min(1) @Max(90) Integer ttlDays) {
    }

    public record Issued(String jti, String token, Instant expiresAt) {
    }

    private final ServiceTokens tokens;
    private final IntegrationTokenRepository repo;

    public IntegrationController(ServiceTokens tokens, IntegrationTokenRepository repo) {
        this.tokens = tokens;
        this.repo = repo;
    }

    @PostMapping("/alertmanager")
    @ResponseStatus(HttpStatus.CREATED)
    public Issued issue(@AuthenticationPrincipal Jwt jwt, @Valid @RequestBody Create req) {
        String tenant = Tenants.of(jwt);
        int days = req.ttlDays() == null ? 30 : req.ttlDays();
        var minted = tokens.mint("integration:alertmanager:" + req.name(), AlertController.AUDIENCE,
                Duration.ofDays(days), Map.of("tenant_id", tenant, "scope", AlertController.SCOPE));
        repo.record(tenant, minted.jti(), "alertmanager", req.name(), Principals.of(jwt), minted.expiresAt());
        return new Issued(minted.jti(), minted.token(), minted.expiresAt());
    }

    @GetMapping
    public List<IntegrationTokenRepository.Row> list(@AuthenticationPrincipal Jwt jwt) {
        return repo.list(Tenants.of(jwt));
    }

    @DeleteMapping("/{jti}")
    @ResponseStatus(HttpStatus.NO_CONTENT)
    public void revoke(@AuthenticationPrincipal Jwt jwt, @PathVariable String jti) {
        if (!repo.revoke(Tenants.of(jwt), jti, Principals.of(jwt))) {
            throw new ResponseStatusException(HttpStatus.NOT_FOUND, "no active integration token " + jti);
        }
    }
}
