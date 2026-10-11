package dev.sentinel.gateway.mcp;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import java.time.Clock;
import java.time.Instant;
import java.time.ZoneOffset;
import java.util.Set;

import dev.sentinel.gateway.identity.RoutingJwtDecoder;
import dev.sentinel.gateway.identity.ServiceTokens;
import dev.sentinel.gateway.identity.SigningKeys;
import dev.sentinel.gateway.identity.TestKeys;
import org.junit.jupiter.api.Test;
import org.springframework.security.oauth2.jwt.JwtException;

class ToolTokensTest {

    final SigningKeys keys = TestKeys.signingKeys(TestKeys.key("k1"));
    final RoutingJwtDecoder decoder = new RoutingJwtDecoder(TestKeys.props(null, null, false), keys,
            Set.of(ToolTokens.AUDIENCE));

    @Test
    void tokenIsDownscopedToOneTenantAndTheMcpAudience() {
        var jwt = decoder.decode(new ToolTokens(new ServiceTokens(keys, TestKeys.props(null, null, false)))
                .mint("acme", "inc-9"));

        assertThat(jwt.getSubject()).isEqualTo("agent:inc-9");
        assertThat(jwt.getClaimAsString("tenant_id")).isEqualTo("acme");
        assertThat(jwt.getClaimAsString("scope")).isEqualTo("knowledge:read telemetry:read");
        assertThat(jwt.getAudience()).containsExactly("sentinel-mcp");
        assertThat(jwt.getHeaders()).containsEntry("alg", "ES256").containsEntry("kid", "k1");
        assertThat(jwt.getExpiresAt()).isBefore(Instant.now().plus(ToolTokens.TTL).plusSeconds(5));
    }

    @Test
    void expiredTokenIsRejected() {
        var past = Clock.fixed(Instant.now().minus(ToolTokens.TTL).minusSeconds(120), ZoneOffset.UTC);
        String token = new ToolTokens(new ServiceTokens(keys, TestKeys.props(null, null, false), past))
                .mint("acme", "inc-9");
        assertThatThrownBy(() -> decoder.decode(token)).isInstanceOf(JwtException.class).hasMessageContaining("Expired");
    }
}
