package dev.sentinel.gateway.tenant;

import static org.assertj.core.api.Assertions.assertThat;

import java.time.Clock;
import java.time.Duration;
import java.time.Instant;
import java.time.ZoneOffset;

import org.junit.jupiter.api.Test;

class TenantRateLimiterTest {

    static class MutableClock extends Clock {
        Instant now = Instant.parse("2026-10-05T00:00:00Z");

        @Override public ZoneOffset getZone() { return ZoneOffset.UTC; }
        @Override public Clock withZone(java.time.ZoneId zone) { return this; }
        @Override public Instant instant() { return now; }
    }

    @Test
    void exhaustsThenRefillsPerTenant() {
        var clock = new MutableClock();
        var limiter = new TenantRateLimiter(3, clock);

        assertThat(limiter.tryAcquire("acme")).isTrue();
        assertThat(limiter.tryAcquire("acme")).isTrue();
        assertThat(limiter.tryAcquire("acme")).isTrue();
        assertThat(limiter.tryAcquire("acme")).isFalse();

        // a noisy tenant does not affect others
        assertThat(limiter.tryAcquire("globex")).isTrue();

        clock.now = clock.now.plus(Duration.ofSeconds(20)); // 3/min -> 1 token per 20s
        assertThat(limiter.tryAcquire("acme")).isTrue();
        assertThat(limiter.tryAcquire("acme")).isFalse();
    }
}
