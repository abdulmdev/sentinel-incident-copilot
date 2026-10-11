package dev.sentinel.gateway.remediation;

import static org.assertj.core.api.Assertions.assertThat;

import java.time.Instant;
import java.util.Set;

import dev.sentinel.gateway.identity.RoutingJwtDecoder;
import dev.sentinel.gateway.identity.ServiceTokens;
import dev.sentinel.gateway.identity.SigningKeys;
import dev.sentinel.gateway.identity.TestKeys;
import org.junit.jupiter.api.Test;

class RemediationTokensTest {

    final SigningKeys keys = TestKeys.signingKeys(TestKeys.key("k1"));
    final RemediationTokens tokens = new RemediationTokens(new ServiceTokens(keys, TestKeys.props(null, null, false)));
    final RoutingJwtDecoder decoder = new RoutingJwtDecoder(TestKeys.props(null, null, false), keys,
            Set.of(RemediationTokens.AUDIENCE));

    @Test
    void capabilityIsBoundToOneApprovedActionOnOneTarget() {
        var cap = tokens.mint("acme", "inc-7", "rollback", "checkout", "alice@acme");
        var jwt = decoder.decode(cap.token());

        assertThat(jwt.getHeaders()).containsEntry("alg", "ES256");
        assertThat(jwt.getClaimAsString("iss")).isEqualTo(TestKeys.ISSUER);
        assertThat(jwt.getAudience()).containsExactly("sentinel-remediation");
        assertThat(jwt.getClaimAsString("scope")).isEqualTo("remediation:execute");
        assertThat(jwt.getClaimAsString("tenant_id")).isEqualTo("acme");
        assertThat(jwt.getClaimAsString("incident_id")).isEqualTo("inc-7");
        assertThat(jwt.getClaimAsString("action")).isEqualTo("rollback");
        assertThat(jwt.getClaimAsString("target")).isEqualTo("checkout");
        assertThat(jwt.getClaimAsString("approved_by")).isEqualTo("alice@acme");
        assertThat(jwt.getId()).isEqualTo(cap.jti());
        assertThat(jwt.getExpiresAt()).isBefore(Instant.now().plus(RemediationTokens.TTL).plusSeconds(5));
    }

    @Test
    void everyApprovalGetsAFreshSingleUseId() {
        var a = tokens.mint("acme", "inc-7", "rollback", "checkout", "alice@acme");
        var b = tokens.mint("acme", "inc-7", "rollback", "checkout", "alice@acme");
        assertThat(a.jti()).isNotEqualTo(b.jti());
    }
}
