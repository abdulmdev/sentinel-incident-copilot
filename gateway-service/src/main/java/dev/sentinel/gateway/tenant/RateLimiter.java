package dev.sentinel.gateway.tenant;

/** Per-tenant request budget. */
public interface RateLimiter {

    boolean tryAcquire(String tenant);
}
