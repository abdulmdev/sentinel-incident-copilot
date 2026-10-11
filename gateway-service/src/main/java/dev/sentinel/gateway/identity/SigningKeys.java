package dev.sentinel.gateway.identity;

import java.text.ParseException;
import java.time.Instant;
import java.util.List;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.jwk.Curve;
import com.nimbusds.jose.jwk.ECKey;
import com.nimbusds.jose.jwk.JWK;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jose.jwk.KeyUse;
import com.nimbusds.jose.jwk.gen.ECKeyGenerator;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Component;

/**
 * The gateway's signing key ring (ES256 / P-256).
 *
 * Only the gateway holds private keys; everyone else verifies with the public set served at
 * {@code /.well-known/jwks.json}. Rotation: add the new key to the set (published, not yet used),
 * make it active, and drop the old key once every token it signed has expired.
 */
@Component
public class SigningKeys {

    private static final Logger log = LoggerFactory.getLogger(SigningKeys.class);

    private final JWKSet keys;
    private final ECKey active;

    public SigningKeys(AuthProperties props) {
        var signing = props.signing();
        if (signing == null || signing.keys() == null || signing.keys().isBlank()) {
            if (signing != null && signing.requireConfigured()) {
                throw new IllegalStateException("sentinel.auth.signing.keys is required (SENTINEL_SIGNING_KEYS): "
                        + "an ephemeral key would differ per replica and be lost on restart");
            }
            this.active = generate("ephemeral-" + Instant.now().getEpochSecond());
            this.keys = new JWKSet(active);
            log.warn("No signing keys configured: using ephemeral key {} (dev only; set SENTINEL_SIGNING_KEYS)",
                    active.getKeyID());
            return;
        }
        try {
            this.keys = JWKSet.parse(signing.keys());
        } catch (ParseException e) {
            throw new IllegalStateException("SENTINEL_SIGNING_KEYS is not a valid JWK set", e);
        }
        for (JWK k : keys.getKeys()) {
            if (!(k instanceof ECKey ec) || !Curve.P_256.equals(ec.getCurve()) || !ec.isPrivate() || k.getKeyID() == null) {
                throw new IllegalStateException("every signing key must be a private EC P-256 JWK with a kid");
            }
        }
        String kid = signing.activeKid();
        JWK chosen = kid == null || kid.isBlank() ? keys.getKeys().getFirst() : keys.getKeyByKeyId(kid);
        if (chosen == null) {
            throw new IllegalStateException("active signing key " + kid + " is not in the key set");
        }
        this.active = (ECKey) chosen;
        log.info("Signing with key {} ({} key(s) published)", active.getKeyID(), keys.size());
    }

    public static ECKey generate(String kid) {
        try {
            return new ECKeyGenerator(Curve.P_256).keyID(kid).keyUse(KeyUse.SIGNATURE)
                    .algorithm(JWSAlgorithm.ES256).generate();
        } catch (JOSEException e) {
            throw new IllegalStateException(e);
        }
    }

    public ECKey active() {
        return active;
    }

    /** Public halves only: safe to publish. */
    public JWKSet publicKeys() {
        return keys.toPublicJWKSet();
    }

    List<String> kids() {
        return keys.getKeys().stream().map(JWK::getKeyID).toList();
    }
}
