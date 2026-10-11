package dev.sentinel.gateway.mcp;

import java.nio.charset.StandardCharsets;
import java.time.Instant;

import javax.crypto.spec.SecretKeySpec;

import com.nimbusds.jose.jwk.source.ImmutableSecret;
import org.springframework.security.oauth2.jose.jws.MacAlgorithm;
import org.springframework.security.oauth2.jwt.JwsHeader;
import org.springframework.security.oauth2.jwt.JwtClaimsSet;
import org.springframework.security.oauth2.jwt.JwtEncoderParameters;
import org.springframework.security.oauth2.jwt.NimbusJwtEncoder;

/** Real HS256 user tokens signed with the default dev secret, like scripts/dev_token.py. */
final class DevTokens {

    private DevTokens() {
    }

    static String user(String sub, String tenant) {
        var key = new SecretKeySpec("dev-only-secret-change-me-0123456789abcdef".getBytes(StandardCharsets.UTF_8),
                "HmacSHA256");
        var claims = JwtClaimsSet.builder().subject(sub).claim("tenant_id", tenant)
                .issuedAt(Instant.now()).expiresAt(Instant.now().plusSeconds(600)).build();
        return new NimbusJwtEncoder(new ImmutableSecret<>(key))
                .encode(JwtEncoderParameters.from(JwsHeader.with(MacAlgorithm.HS256).build(), claims)).getTokenValue();
    }
}
