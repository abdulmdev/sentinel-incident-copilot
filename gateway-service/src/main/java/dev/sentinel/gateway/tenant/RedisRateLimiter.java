package dev.sentinel.gateway.tenant;

import java.util.List;

import io.micrometer.core.instrument.Counter;
import io.micrometer.core.instrument.MeterRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.data.redis.core.StringRedisTemplate;
import org.springframework.data.redis.core.script.RedisScript;

/**
 * Token bucket shared by every gateway replica: one Redis hash per tenant, updated by one Lua
 * script so check-and-decrement is atomic. The script reads Redis's own clock (TIME), so replicas
 * with skewed clocks can't mint extra tokens.
 *
 * Failure policy: if Redis is unreachable, fall back to the per-instance limiter. Availability
 * beats exactness here: the worst case is N× the budget for the length of the outage, which the
 * {@code sentinel.ratelimit.fallback} counter makes visible.
 */
public class RedisRateLimiter implements RateLimiter {

    private static final Logger log = LoggerFactory.getLogger(RedisRateLimiter.class);

    static final RedisScript<Long> TOKEN_BUCKET = RedisScript.of("""
            local capacity = tonumber(ARGV[1])
            local refill_per_ms = tonumber(ARGV[2])
            local t = redis.call('TIME')
            local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
            local state = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
            local tokens = tonumber(state[1]) or capacity
            local ts = tonumber(state[2]) or now
            tokens = math.min(capacity, tokens + math.max(0, now - ts) * refill_per_ms)
            local allowed = 0
            if tokens >= 1 then
              tokens = tokens - 1
              allowed = 1
            end
            redis.call('HSET', KEYS[1], 'tokens', tokens, 'ts', now)
            redis.call('PEXPIRE', KEYS[1], math.ceil(capacity / refill_per_ms) + 1000)
            return allowed
            """, Long.class);

    private final StringRedisTemplate redis;
    private final int capacity;
    private final RateLimiter fallback;
    private final Counter fallbacks;

    public RedisRateLimiter(StringRedisTemplate redis, int requestsPerMinute, RateLimiter fallback,
                            MeterRegistry meters) {
        this.redis = redis;
        this.capacity = requestsPerMinute;
        this.fallback = fallback;
        this.fallbacks = meters.counter("sentinel.ratelimit.fallback");
    }

    @Override
    public boolean tryAcquire(String tenant) {
        try {
            Long allowed = redis.execute(TOKEN_BUCKET, List.of("ratelimit:" + tenant),
                    String.valueOf(capacity), String.valueOf(capacity / 60_000.0));
            return allowed != null && allowed == 1L;
        } catch (RuntimeException e) {
            fallbacks.increment();
            log.warn("rate limiter falling back to per-instance buckets: {}", e.getMessage());
            return fallback.tryAcquire(tenant);
        }
    }
}
