package dev.sentinel.gateway.identity;

import org.springframework.security.oauth2.jwt.Jwt;

/** A human-readable identity for audit fields: OIDC subjects are opaque ids, usernames are not. */
public final class Principals {

    private Principals() {
    }

    public static String of(Jwt jwt) {
        for (String claim : new String[] {"preferred_username", "email"}) {
            String v = jwt.getClaimAsString(claim);
            if (v != null && !v.isBlank()) {
                return v;
            }
        }
        return jwt.getSubject();
    }
}
