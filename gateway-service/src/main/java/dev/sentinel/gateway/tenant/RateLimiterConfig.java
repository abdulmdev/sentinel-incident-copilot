package dev.sentinel.gateway.tenant;

import java.time.Clock;

import dev.sentinel.gateway.config.SentinelProperties;
import io.micrometer.core.instrument.MeterRegistry;
import org.springframework.beans.factory.ObjectProvider;
import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.data.redis.core.StringRedisTemplate;

@Configuration
public class RateLimiterConfig {

    /** {@code sentinel.rate-limit.backend}: {@code redis} (shared across replicas) or {@code memory}. */
    @Bean
    RateLimiter rateLimiter(SentinelProperties props, MeterRegistry meters,
                            @Value("${sentinel.rate-limit.backend:memory}") String backend,
                            ObjectProvider<StringRedisTemplate> redis) {
        var local = new TenantRateLimiter(props.requestsPerMinute(), Clock.systemUTC());
        if ("redis".equalsIgnoreCase(backend)) {
            return new RedisRateLimiter(redis.getObject(), props.requestsPerMinute(), local, meters);
        }
        return local;
    }
}
