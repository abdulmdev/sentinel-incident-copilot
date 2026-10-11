package dev.sentinel.gateway.tenant;

import java.time.Clock;
import java.util.concurrent.ConcurrentHashMap;

/**
 * In-memory per-tenant token bucket. Exact for one instance; with N replicas a tenant gets N×
 * the budget, which is why production uses {@link RedisRateLimiter} and keeps this as its fallback.
 */
public class TenantRateLimiter implements RateLimiter {

    private final double capacity;
    private final double refillPerMilli;
    private final Clock clock;
    private final ConcurrentHashMap<String, Bucket> buckets = new ConcurrentHashMap<>();

    public TenantRateLimiter(int requestsPerMinute, Clock clock) {
        this.capacity = requestsPerMinute;
        this.refillPerMilli = requestsPerMinute / 60_000.0;
        this.clock = clock;
    }

    @Override
    public boolean tryAcquire(String tenant) {
        long now = clock.millis();
        Bucket b = buckets.computeIfAbsent(tenant, t -> new Bucket(capacity, now));
        synchronized (b) {
            b.tokens = Math.min(capacity, b.tokens + (now - b.lastRefill) * refillPerMilli);
            b.lastRefill = now;
            if (b.tokens < 1) {
                return false;
            }
            b.tokens -= 1;
            return true;
        }
    }

    private static final class Bucket {
        double tokens;
        long lastRefill;

        Bucket(double tokens, long lastRefill) {
            this.tokens = tokens;
            this.lastRefill = lastRefill;
        }
    }
}
