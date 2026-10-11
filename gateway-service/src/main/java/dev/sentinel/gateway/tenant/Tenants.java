package dev.sentinel.gateway.tenant;

import org.springframework.security.access.AccessDeniedException;
import org.springframework.security.oauth2.jwt.Jwt;

/** The tenant always comes from the verified token, never from the request body or path. */
public final class Tenants {

    public static final String CLAIM = "tenant_id";

    private Tenants() {
    }

    public static String of(Jwt jwt) {
        String tenant = jwt.getClaimAsString(CLAIM);
        if (tenant == null || tenant.isBlank()) {
            throw new AccessDeniedException("token has no " + CLAIM + " claim");
        }
        return tenant;
    }
}
