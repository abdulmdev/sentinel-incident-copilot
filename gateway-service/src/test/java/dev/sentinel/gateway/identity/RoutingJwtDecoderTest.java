package dev.sentinel.gateway.identity;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import java.net.InetSocketAddress;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.Date;
import java.util.List;
import java.util.Map;
import java.util.Set;

import com.nimbusds.jose.JOSEObjectType;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.crypto.ECDSASigner;
import com.nimbusds.jose.crypto.MACSigner;
import com.nimbusds.jose.crypto.RSASSASigner;
import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jose.jwk.RSAKey;
import com.nimbusds.jose.jwk.gen.RSAKeyGenerator;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.PlainJWT;
import com.nimbusds.jwt.SignedJWT;
import com.sun.net.httpserver.HttpServer;
import org.junit.jupiter.api.Test;
import org.springframework.security.oauth2.jwt.JwtException;

/** The decoder is the trust boundary: every way to forge or confuse a token must fail. */
class RoutingJwtDecoderTest {

    static final String SECRET = "dev-only-secret-change-me-0123456789abcdef";
    static final Set<String> SERVICE = Set.of("sentinel-mcp", "sentinel-alerts", "sentinel-remediation", "sentinel-agent");

    final ECKey signing = TestKeys.key("k1");
    final SigningKeys ring = TestKeys.signingKeys(signing);

    RoutingJwtDecoder decoder(boolean dev) {
        return new RoutingJwtDecoder(TestKeys.props(TestKeys.set(signing), null, dev), ring, SERVICE);
    }

    String minted(String audience) {
        return new ServiceTokens(ring, TestKeys.props(null, null, false))
                .mint("agent:inc-1", audience, Duration.ofMinutes(5), Map.of("tenant_id", "acme")).token();
    }

    static JWTClaimsSet.Builder claims(String iss, String... aud) {
        var b = new JWTClaimsSet.Builder().subject("x").claim("tenant_id", "acme")
                .issueTime(new Date()).expirationTime(Date.from(Instant.now().plusSeconds(300)));
        if (iss != null) {
            b.issuer(iss);
        }
        if (aud.length > 0) {
            b.audience(List.of(aud));
        }
        return b;
    }

    static String hs256(JWTClaimsSet c) throws Exception {
        var jwt = new SignedJWT(new JWSHeader(JWSAlgorithm.HS256), c);
        jwt.sign(new MACSigner(SECRET.getBytes(StandardCharsets.UTF_8)));
        return jwt.serialize();
    }

    @Test
    void ownServiceTokensVerify() {
        var jwt = decoder(false).decode(minted("sentinel-mcp"));
        assertThat(jwt.getClaimAsString("iss")).isEqualTo(TestKeys.ISSUER);
        assertThat(decoder(false).isServiceToken(jwt)).isTrue();
    }

    @Test
    void tokenSignedWithAnotherKeyClaimingOurIssuerIsRejected() throws Exception {
        var attacker = TestKeys.key("k1"); // same kid, different key
        var jwt = new SignedJWT(new JWSHeader.Builder(JWSAlgorithm.ES256).keyID("k1").build(),
                claims(TestKeys.ISSUER, "sentinel-remediation").build());
        jwt.sign(new ECDSASigner(attacker));
        assertThatThrownBy(() -> decoder(true).decode(jwt.serialize())).isInstanceOf(JwtException.class);
    }

    @Test
    void algorithmConfusionHs256ClaimingOurIssuerIsRejected() throws Exception {
        // Someone holding the dev secret tries to forge a capability.
        String forged = hs256(claims(TestKeys.ISSUER, "sentinel-remediation").claim("action", "rollback").build());
        assertThatThrownBy(() -> decoder(true).decode(forged)).isInstanceOf(JwtException.class);
    }

    @Test
    void unsignedTokensAreRejected() {
        String none = new PlainJWT(claims(TestKeys.ISSUER, "sentinel-mcp").build()).serialize();
        assertThatThrownBy(() -> decoder(true).decode(none)).isInstanceOf(JwtException.class);
    }

    @Test
    void ourIssuerWithoutAServiceAudienceIsRejected() {
        // The gateway never mints user tokens: an own-issuer token on the user API would be a bug or a forgery.
        assertThatThrownBy(() -> decoder(false).decode(minted("sentinel-api"))).isInstanceOf(JwtException.class);
    }

    @Test
    void devTokensWorkOnlyWhenEnabledAndNeverAsServiceTokens() throws Exception {
        String user = hs256(claims(null).build());
        assertThat(decoder(true).decode(user).getClaimAsString("tenant_id")).isEqualTo("acme");
        assertThatThrownBy(() -> decoder(false).decode(user)).hasMessageContaining("untrusted");
        String devService = hs256(claims(null, "sentinel-alerts").build());
        assertThatThrownBy(() -> decoder(true).decode(devService)).isInstanceOf(JwtException.class);
    }

    @Test
    void unknownIssuerIsRejected() throws Exception {
        assertThatThrownBy(() -> decoder(true).decode(hs256(claims("https://evil.example").build())))
                .hasMessageContaining("untrusted");
    }

    @Test
    void oidcUserTokensVerifyAgainstTheProvidersJwksWithIssuerAndAudience() throws Exception {
        RSAKey idp = new RSAKeyGenerator(2048).keyID("idp-1").generate();
        HttpServer server = HttpServer.create(new InetSocketAddress("127.0.0.1", 0), 0);
        byte[] body = new JWKSet(idp.toPublicJWK()).toString().getBytes(StandardCharsets.UTF_8);
        server.createContext("/certs", ex -> {
            ex.sendResponseHeaders(200, body.length);
            ex.getResponseBody().write(body);
            ex.close();
        });
        server.start();
        try {
            String issuer = "http://idp.test/realms/sentinel";
            var props = new AuthProperties(TestKeys.ISSUER, new AuthProperties.Signing(TestKeys.set(signing), null, false),
                    new AuthProperties.Oidc(issuer, "http://127.0.0.1:" + server.getAddress().getPort() + "/certs", "sentinel-api"),
                    new AuthProperties.Dev(false, SECRET));
            var decoder = new RoutingJwtDecoder(props, ring, SERVICE);

            assertThat(decoder.decode(rs256(idp, claims(issuer, "sentinel-api").claim("preferred_username", "alice@acme").build()))
                    .getClaimAsString("preferred_username")).isEqualTo("alice@acme");
            assertThat(decoder.isServiceToken(decoder.decode(rs256(idp, claims(issuer, "sentinel-api").build())))).isFalse();
            // Right IdP, wrong audience (a token for some other app).
            assertThatThrownBy(() -> decoder.decode(rs256(idp, claims(issuer, "other-app").build())))
                    .isInstanceOf(JwtException.class);
            // Signed by a different RSA key.
            RSAKey other = new RSAKeyGenerator(2048).keyID("idp-1").generate();
            assertThatThrownBy(() -> decoder.decode(rs256(other, claims(issuer, "sentinel-api").build())))
                    .isInstanceOf(JwtException.class);
        } finally {
            server.stop(0);
        }
    }

    static String rs256(RSAKey key, JWTClaimsSet claims) throws Exception {
        var jwt = new SignedJWT(new JWSHeader.Builder(JWSAlgorithm.RS256).keyID(key.getKeyID())
                .type(JOSEObjectType.JWT).build(), claims);
        jwt.sign(new RSASSASigner(key));
        return jwt.serialize();
    }
}
