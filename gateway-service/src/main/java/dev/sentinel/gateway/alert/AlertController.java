package dev.sentinel.gateway.alert;

import dev.sentinel.gateway.tenant.Tenants;
import org.springframework.http.HttpStatus;
import org.springframework.security.core.annotation.AuthenticationPrincipal;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;

/**
 * Alertmanager webhook receiver. Authenticated with a per-tenant integration token
 * (scope alerts:write, aud sentinel-alerts) configured in Alertmanager's http_config.
 * Responds immediately; investigations run in the background.
 */
@RestController
@RequestMapping("/api/v1/alerts")
public class AlertController {

    public static final String AUDIENCE = "sentinel-alerts";
    public static final String SCOPE = "alerts:write";

    private final AlertService alerts;

    public AlertController(AlertService alerts) {
        this.alerts = alerts;
    }

    @PostMapping("/alertmanager")
    @ResponseStatus(HttpStatus.ACCEPTED)
    public AlertService.Result alertmanager(@AuthenticationPrincipal Jwt jwt, @RequestBody AlertmanagerWebhook body) {
        return alerts.handle(Tenants.of(jwt), body);
    }
}
