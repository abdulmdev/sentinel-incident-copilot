package dev.sentinel.gateway.identity;

import org.springframework.boot.context.properties.ConfigurationProperties;

/**
 * Who Sentinel trusts, and how it signs.
 *
 * @param issuer  this gateway's issuer id (the {@code iss} of every service token it mints)
 * @param signing the gateway's ES256 key ring
 * @param oidc    the users' identity provider (blank issuer-uri = no OIDC)
 * @param dev     HS256 dev user tokens (scripts, tests, the console's dev login); off by default
 */
@ConfigurationProperties(prefix = "sentinel.auth")
public record AuthProperties(String issuer, Signing signing, Oidc oidc, Dev dev) {

    /**
     * @param keys              JWK set JSON with private EC P-256 keys; blank = an ephemeral key (single-replica dev only)
     * @param activeKid         the key that signs new tokens (default: the first in the set)
     * @param requireConfigured refuse to start with an ephemeral key (replicas would disagree)
     */
    public record Signing(String keys, String activeKid, boolean requireConfigured) {
    }

    /**
     * @param issuerUri exact {@code iss} of user tokens
     * @param jwkSetUri where to fetch the IdP's keys (may be an internal URL)
     * @param audience  required {@code aud} of user tokens
     */
    public record Oidc(String issuerUri, String jwkSetUri, String audience) {
        public boolean enabled() {
            return issuerUri != null && !issuerUri.isBlank();
        }
    }

    public record Dev(boolean enabled, String secret) {
    }
}
