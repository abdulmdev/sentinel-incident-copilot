package dev.sentinel.gateway.identity;

import java.nio.charset.StandardCharsets;
import java.text.ParseException;
import java.util.Set;

import javax.crypto.spec.SecretKeySpec;

import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.jwk.source.ImmutableJWKSet;
import com.nimbusds.jose.proc.JWSVerificationKeySelector;
import com.nimbusds.jose.proc.SecurityContext;
import com.nimbusds.jwt.JWT;
import com.nimbusds.jwt.JWTParser;
import com.nimbusds.jwt.proc.DefaultJWTProcessor;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.security.oauth2.core.DelegatingOAuth2TokenValidator;
import org.springframework.security.oauth2.core.OAuth2Error;
import org.springframework.security.oauth2.core.OAuth2TokenValidator;
import org.springframework.security.oauth2.core.OAuth2TokenValidatorResult;
import org.springframework.security.oauth2.jose.jws.MacAlgorithm;
import org.springframework.security.oauth2.jose.jws.SignatureAlgorithm;
import org.springframework.security.oauth2.jwt.BadJwtException;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.security.oauth2.jwt.JwtDecoder;
import org.springframework.security.oauth2.jwt.JwtException;
import org.springframework.security.oauth2.jwt.JwtValidators;
import org.springframework.security.oauth2.jwt.NimbusJwtDecoder;

/**
 * One decoder per trusted issuer, chosen by the token's (unverified) {@code iss}; each decoder then
 * verifies the signature with its own key material and a pinned algorithm, so a token can't pick its
 * verifier by lying about {@code alg} (no HS/ES confusion, no "none").
 *
 * <ul>
 *   <li>this gateway: ES256, own key ring. Service tokens only: the audience must be a service audience.</li>
 *   <li>the OIDC provider (users): RS256 via its JWKS; audience must include the API audience.</li>
 *   <li>dev (optional): HS256 user tokens without an issuer (or {@code sentinel-dev}); never a service audience.</li>
 * </ul>
 */
public class RoutingJwtDecoder implements JwtDecoder {

    private static final Logger log = LoggerFactory.getLogger(RoutingJwtDecoder.class);
    public static final String DEV_ISSUER = "sentinel-dev";

    private final String ownIssuer;
    private final JwtDecoder own;
    private final String oidcIssuer;
    private final JwtDecoder oidc;
    private final JwtDecoder dev;

    public RoutingJwtDecoder(AuthProperties props, SigningKeys keys, Set<String> serviceAudiences) {
        this.ownIssuer = props.issuer();
        var ownProcessor = new DefaultJWTProcessor<SecurityContext>();
        ownProcessor.setJWSKeySelector(new JWSVerificationKeySelector<>(JWSAlgorithm.ES256,
                new ImmutableJWKSet<>(keys.publicKeys())));
        var ownDecoder = new NimbusJwtDecoder(ownProcessor);
        ownDecoder.setJwtValidator(new DelegatingOAuth2TokenValidator<>(
                JwtValidators.createDefaultWithIssuer(ownIssuer),
                requireAudienceIn(serviceAudiences, "service tokens must carry a service audience")));
        this.own = ownDecoder;

        if (props.oidc() != null && props.oidc().enabled()) {
            this.oidcIssuer = props.oidc().issuerUri();
            String jwks = props.oidc().jwkSetUri() == null || props.oidc().jwkSetUri().isBlank()
                    ? oidcIssuer + "/protocol/openid-connect/certs" : props.oidc().jwkSetUri();
            var decoder = NimbusJwtDecoder.withJwkSetUri(jwks).jwsAlgorithm(SignatureAlgorithm.RS256).build();
            decoder.setJwtValidator(new DelegatingOAuth2TokenValidator<>(
                    JwtValidators.createDefaultWithIssuer(oidcIssuer),
                    requireAudienceIn(Set.of(props.oidc().audience()), "user token is not for this API")));
            this.oidc = decoder;
        } else {
            this.oidcIssuer = null;
            this.oidc = null;
        }

        if (props.dev() != null && props.dev().enabled()) {
            var key = new SecretKeySpec(props.dev().secret().getBytes(StandardCharsets.UTF_8), "HmacSHA256");
            var decoder = NimbusJwtDecoder.withSecretKey(key).macAlgorithm(MacAlgorithm.HS256).build();
            decoder.setJwtValidator(new DelegatingOAuth2TokenValidator<>(JwtValidators.createDefault(),
                    rejectAudienceIn(serviceAudiences)));
            this.dev = decoder;
            log.warn("Dev user tokens (HS256, shared secret) are ENABLED: development only (SENTINEL_DEV_TOKENS)");
        } else {
            this.dev = null;
        }
    }

    @Override
    public Jwt decode(String token) throws JwtException {
        JWT parsed;
        String iss;
        try {
            parsed = JWTParser.parse(token);
            iss = parsed.getJWTClaimsSet().getIssuer();
        } catch (ParseException e) {
            throw new BadJwtException("malformed token", e);
        }
        if (ownIssuer.equals(iss)) {
            return own.decode(token);
        }
        if (oidc != null && oidcIssuer.equals(iss)) {
            return oidc.decode(token);
        }
        if (dev != null && (iss == null || DEV_ISSUER.equals(iss))
                && JWSAlgorithm.HS256.equals(parsed.getHeader().getAlgorithm())) {
            return dev.decode(token);
        }
        throw new BadJwtException("untrusted token issuer");
    }

    public boolean isServiceToken(Jwt jwt) {
        return ownIssuer.equals(jwt.getClaimAsString("iss"));
    }

    private static OAuth2TokenValidator<Jwt> requireAudienceIn(Set<String> allowed, String message) {
        return jwt -> jwt.getAudience() != null && jwt.getAudience().stream().anyMatch(allowed::contains)
                ? OAuth2TokenValidatorResult.success()
                : OAuth2TokenValidatorResult.failure(new OAuth2Error("invalid_token", message, null));
    }

    private static OAuth2TokenValidator<Jwt> rejectAudienceIn(Set<String> serviceAudiences) {
        return jwt -> jwt.getAudience() == null || jwt.getAudience().stream().noneMatch(serviceAudiences::contains)
                ? OAuth2TokenValidatorResult.success()
                : OAuth2TokenValidatorResult.failure(new OAuth2Error("invalid_token",
                        "dev tokens cannot carry a service audience", null));
    }
}
