package dev.sentinel.gateway.identity;

import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jose.jwk.source.ImmutableJWKSet;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.security.oauth2.jose.jws.SignatureAlgorithm;
import org.springframework.security.oauth2.jwt.JwsHeader;
import org.springframework.security.oauth2.jwt.JwtClaimsSet;
import org.springframework.security.oauth2.jwt.JwtEncoder;
import org.springframework.security.oauth2.jwt.JwtEncoderParameters;
import org.springframework.security.oauth2.jwt.NimbusJwtEncoder;
import org.springframework.stereotype.Component;

/**
 * Mints every service credential (tool tokens, agent calls, capabilities, integration tokens):
 * ES256 with the active key, {@code iss} = this gateway, a {@code kid} for rotation, a unique {@code jti}.
 */
@Component
public class ServiceTokens {

    public record Minted(String token, String jti, Instant expiresAt) {
    }

    private final JwtEncoder encoder;
    private final String issuer;
    private final String kid;
    private final Clock clock;

    @Autowired
    public ServiceTokens(SigningKeys keys, AuthProperties props) {
        this(keys, props, Clock.systemUTC());
    }

    public ServiceTokens(SigningKeys keys, AuthProperties props, Clock clock) {
        this.encoder = new NimbusJwtEncoder(new ImmutableJWKSet<>(new JWKSet(keys.active())));
        this.issuer = props.issuer();
        this.kid = keys.active().getKeyID();
        this.clock = clock;
    }

    public Minted mint(String subject, String audience, Duration ttl, Map<String, Object> claims) {
        Instant now = clock.instant();
        String jti = UUID.randomUUID().toString();
        var builder = JwtClaimsSet.builder()
                .issuer(issuer)
                .subject(subject)
                .audience(List.of(audience))
                .id(jti)
                .issuedAt(now)
                .expiresAt(now.plus(ttl));
        claims.forEach(builder::claim);
        var header = JwsHeader.with(SignatureAlgorithm.ES256).keyId(kid).type("JWT").build();
        String token = encoder.encode(JwtEncoderParameters.from(header, builder.build())).getTokenValue();
        return new Minted(token, jti, now.plus(ttl));
    }

    public String issuer() {
        return issuer;
    }
}
