package dev.sentinel.gateway.config;

import java.util.ArrayList;
import java.util.Collection;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.function.Predicate;

import dev.sentinel.gateway.alert.AlertController;
import dev.sentinel.gateway.identity.AuthProperties;
import dev.sentinel.gateway.identity.RoutingJwtDecoder;
import dev.sentinel.gateway.identity.SigningKeys;
import dev.sentinel.gateway.incident.AgentTokens;
import dev.sentinel.gateway.integration.IntegrationController;
import dev.sentinel.gateway.integration.IntegrationTokenRepository;
import dev.sentinel.gateway.mcp.ToolTokens;
import dev.sentinel.gateway.remediation.RemediationTokens;
import dev.sentinel.gateway.tenant.TenantRateLimitFilter;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.authorization.AuthorizationDecision;
import org.springframework.security.authorization.AuthorizationManager;
import org.springframework.security.config.annotation.web.builders.HttpSecurity;
import org.springframework.security.config.http.SessionCreationPolicy;
import org.springframework.security.core.GrantedAuthority;
import org.springframework.security.core.authority.SimpleGrantedAuthority;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationConverter;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationToken;
import org.springframework.security.oauth2.server.resource.authentication.JwtGrantedAuthoritiesConverter;
import org.springframework.security.oauth2.server.resource.web.authentication.BearerTokenAuthenticationFilter;
import org.springframework.security.web.SecurityFilterChain;
import org.springframework.security.web.access.intercept.RequestAuthorizationContext;

/**
 * Who may call what. Three kinds of principal, told apart by issuer (after signature verification):
 * <ul>
 *   <li><b>users</b>: OIDC (or dev) tokens, on the public API only;</li>
 *   <li><b>service tokens</b> minted by this gateway (ES256): each only on its own surface, matched by
 *       audience and scope (tool tokens → {@code /mcp}; integration tokens → the alerts webhook, and only
 *       while not revoked);</li>
 *   <li>nobody else: unknown issuers are rejected by the decoder.</li>
 * </ul>
 * Capabilities and agent-call tokens are verified by the adapter and the agent, never accepted here.
 */
@Configuration
public class SecurityConfig {

    /** Service-token audiences: each is accepted only on its own surface, never on the general API. */
    public static final Set<String> SERVICE_AUDIENCES = Set.of(ToolTokens.AUDIENCE, AlertController.AUDIENCE,
            RemediationTokens.AUDIENCE, AgentTokens.AUDIENCE);

    @Bean
    RoutingJwtDecoder jwtDecoder(AuthProperties props, SigningKeys keys) {
        return new RoutingJwtDecoder(props, keys, SERVICE_AUDIENCES);
    }

    @Bean
    SecurityFilterChain api(HttpSecurity http, TenantRateLimitFilter rateLimit, RoutingJwtDecoder decoder,
                            IntegrationTokenRepository integrations) throws Exception {
        Predicate<Jwt> service = decoder::isServiceToken;
        Predicate<Jwt> user = service.negate();
        return http
                .csrf(csrf -> csrf.disable())
                .sessionManagement(s -> s.sessionCreationPolicy(SessionCreationPolicy.STATELESS))
                .authorizeHttpRequests(auth -> auth
                        .requestMatchers("/actuator/health/**", "/actuator/prometheus", "/.well-known/**").permitAll()
                        // Agent tool tokens: MCP only, with the knowledge scope.
                        .requestMatchers("/mcp", "/mcp/**").access(jwt(t -> service.test(t.getToken())
                                && hasAuthority(t, "SCOPE_" + ToolTokens.SCOPE)
                                && audience(t.getToken(), ToolTokens.AUDIENCE)))
                        // Alertmanager: an integration token this gateway issued and hasn't revoked.
                        .requestMatchers("/api/v1/alerts/**").access(jwt(t -> service.test(t.getToken())
                                && hasAuthority(t, "SCOPE_" + AlertController.SCOPE)
                                && audience(t.getToken(), AlertController.AUDIENCE)
                                && integrations.useIfActive(t.getToken().getClaimAsString("tenant_id"),
                                        t.getToken().getId())))
                        // Issuing integration tokens: tenant admins (a user role), never a service.
                        .requestMatchers("/api/v1/integrations/**").access(jwt(t -> user.test(t.getToken())
                                && hasAuthority(t, "ROLE_" + IntegrationController.ADMIN_ROLE)))
                        // Everything else: users only.
                        .anyRequest().access(jwt(t -> user.test(t.getToken())
                                && (t.getToken().getAudience() == null
                                    || t.getToken().getAudience().stream().noneMatch(SERVICE_AUDIENCES::contains)))))
                .oauth2ResourceServer(o -> o.jwt(j -> j.decoder(decoder).jwtAuthenticationConverter(authorities())))
                .addFilterAfter(rateLimit, BearerTokenAuthenticationFilter.class)
                .build();
    }

    /** Scopes as {@code SCOPE_x}; roles from {@code roles} (dev) or Keycloak's {@code realm_access.roles} as {@code ROLE_x}. */
    static JwtAuthenticationConverter authorities() {
        var scopes = new JwtGrantedAuthoritiesConverter();
        var converter = new JwtAuthenticationConverter();
        converter.setJwtGrantedAuthoritiesConverter(jwt -> {
            Collection<GrantedAuthority> out = new ArrayList<>(scopes.convert(jwt));
            List<String> roles = new ArrayList<>();
            if (jwt.getClaimAsStringList("roles") != null) {
                roles.addAll(jwt.getClaimAsStringList("roles"));
            }
            if (jwt.getClaim("realm_access") instanceof Map<?, ?> realm && realm.get("roles") instanceof List<?> list) {
                list.forEach(r -> roles.add(String.valueOf(r)));
            }
            roles.forEach(r -> out.add(new SimpleGrantedAuthority("ROLE_" + r)));
            return out;
        });
        return converter;
    }

    private static AuthorizationManager<RequestAuthorizationContext> jwt(Predicate<JwtAuthenticationToken> rule) {
        return (authentication, ctx) -> new AuthorizationDecision(
                authentication.get() instanceof JwtAuthenticationToken t && rule.test(t));
    }

    private static boolean hasAuthority(JwtAuthenticationToken t, String authority) {
        return t.getAuthorities().stream().anyMatch(a -> authority.equals(a.getAuthority()));
    }

    private static boolean audience(Jwt jwt, String audience) {
        return jwt.getAudience() != null && jwt.getAudience().contains(audience);
    }
}
