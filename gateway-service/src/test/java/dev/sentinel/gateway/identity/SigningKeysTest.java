package dev.sentinel.gateway.identity;

import static org.assertj.core.api.Assertions.assertThat;
import static org.assertj.core.api.Assertions.assertThatThrownBy;

import com.nimbusds.jose.jwk.JWKSet;
import org.junit.jupiter.api.Test;

class SigningKeysTest {

    @Test
    void firstKeySignsByDefaultAndEveryKeyIsPublished() {
        var keys = TestKeys.signingKeys(TestKeys.key("2026-10"), TestKeys.key("2026-07"));
        assertThat(keys.active().getKeyID()).isEqualTo("2026-10");
        assertThat(keys.kids()).containsExactly("2026-10", "2026-07");
    }

    @Test
    void activeKidSelectsTheSigningKeyDuringRotation() {
        var set = TestKeys.set(TestKeys.key("new"), TestKeys.key("old"));
        var keys = new SigningKeys(TestKeys.props(set, "old", false));
        assertThat(keys.active().getKeyID()).isEqualTo("old"); // "new" is published first, used later
    }

    @Test
    void publishedSetNeverContainsPrivateKeys() {
        var json = TestKeys.signingKeys(TestKeys.key("k1")).publicKeys().toString(false);
        assertThat(json).doesNotContain("\"d\"");
    }

    @Test
    void publicOnlyOrUnknownActiveKeysAreRejected() {
        var publicOnly = new JWKSet(TestKeys.key("k1").toPublicJWK()).toString(false);
        assertThatThrownBy(() -> new SigningKeys(TestKeys.props(publicOnly, null, false)))
                .hasMessageContaining("private EC P-256");
        assertThatThrownBy(() -> new SigningKeys(TestKeys.props(TestKeys.set(TestKeys.key("k1")), "k9", false)))
                .hasMessageContaining("k9");
    }

    @Test
    void productionRefusesAnEphemeralKey() {
        var props = new AuthProperties(TestKeys.ISSUER, new AuthProperties.Signing("", null, true), null, null);
        assertThatThrownBy(() -> new SigningKeys(props)).hasMessageContaining("SENTINEL_SIGNING_KEYS");
    }

    @Test
    void devGetsAnEphemeralKey() {
        var props = new AuthProperties(TestKeys.ISSUER, new AuthProperties.Signing("", null, false), null, null);
        assertThat(new SigningKeys(props).active().getKeyID()).startsWith("ephemeral-");
    }
}
