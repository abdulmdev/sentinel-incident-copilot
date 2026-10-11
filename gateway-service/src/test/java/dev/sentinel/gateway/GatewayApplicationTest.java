package dev.sentinel.gateway;

import org.junit.jupiter.api.Test;
import org.springframework.boot.test.context.SpringBootTest;

/** Full context with no mocked beans, so missing auto-configuration fails the build. */
@SpringBootTest(properties = "spring.ai.anthropic.api-key=test")
class GatewayApplicationTest {

    @Test
    void contextLoads() {
    }
}
