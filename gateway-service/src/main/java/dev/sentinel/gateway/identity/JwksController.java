package dev.sentinel.gateway.identity;

import java.time.Duration;
import java.util.Map;

import org.springframework.http.CacheControl;
import org.springframework.http.ResponseEntity;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

/** Public keys for verifying the gateway's service tokens (agent, MCP servers, remediation adapter). */
@RestController
public class JwksController {

    private final SigningKeys keys;
    private final AuthProperties props;

    public JwksController(SigningKeys keys, AuthProperties props) {
        this.keys = keys;
        this.props = props;
    }

    @GetMapping("/.well-known/jwks.json")
    public ResponseEntity<Map<String, Object>> jwks() {
        // Short cache: verifiers also refetch on an unknown kid, so rotation needs no coordination.
        return ResponseEntity.ok().cacheControl(CacheControl.maxAge(Duration.ofMinutes(5)).cachePublic())
                .body(keys.publicKeys().toJSONObject());
    }

    @GetMapping("/.well-known/sentinel-issuer")
    public Map<String, Object> issuer() {
        return Map.of("issuer", props.issuer(), "jwks_uri", props.issuer() + "/.well-known/jwks.json",
                "id_token_signing_alg_values_supported", new String[] {"ES256"});
    }
}
