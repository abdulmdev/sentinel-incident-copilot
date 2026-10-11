package dev.sentinel.gateway.identity;

import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.JWKSet;

/** Builds the gateway's identity objects outside Spring, with real generated keys. */
public final class TestKeys {

    public static final String ISSUER = "http://gateway.test";

    private TestKeys() {
    }

    public static AuthProperties props(String keysJson, String activeKid, boolean dev) {
        return new AuthProperties(ISSUER, new AuthProperties.Signing(keysJson, activeKid, false),
                new AuthProperties.Oidc(null, null, "sentinel-api"),
                new AuthProperties.Dev(dev, "dev-only-secret-change-me-0123456789abcdef"));
    }

    public static ECKey key(String kid) {
        return SigningKeys.generate(kid);
    }

    public static String set(ECKey... keys) {
        return new JWKSet(java.util.List.of(keys)).toString(false); // includes private parts
    }

    public static SigningKeys signingKeys(ECKey... keys) {
        return new SigningKeys(props(set(keys), null, false));
    }
}
