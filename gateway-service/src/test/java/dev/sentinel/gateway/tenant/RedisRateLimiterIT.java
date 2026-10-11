package dev.sentinel.gateway.tenant;

import static org.assertj.core.api.Assertions.assertThat;

import java.time.Clock;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.stream.IntStream;

import io.micrometer.core.instrument.simple.SimpleMeterRegistry;
import org.junit.jupiter.api.AfterEach;
import org.junit.jupiter.api.Test;
import org.springframework.data.redis.connection.RedisStandaloneConfiguration;
import org.springframework.data.redis.connection.lettuce.LettuceConnectionFactory;
import org.springframework.data.redis.core.StringRedisTemplate;
import org.testcontainers.containers.GenericContainer;
import org.testcontainers.junit.jupiter.Container;
import org.testcontainers.junit.jupiter.Testcontainers;

@Testcontainers
class RedisRateLimiterIT {

    @Container
    static GenericContainer<?> redis = new GenericContainer<>("redis:7.4-alpine").withExposedPorts(6379);

    LettuceConnectionFactory factory;

    StringRedisTemplate template() {
        factory = new LettuceConnectionFactory(new RedisStandaloneConfiguration(redis.getHost(), redis.getMappedPort(6379)));
        factory.afterPropertiesSet();
        return new StringRedisTemplate(factory);
    }

    @AfterEach
    void close() {
        if (factory != null) {
            factory.destroy();
        }
    }

    RedisRateLimiter replica(StringRedisTemplate redis, int perMinute, SimpleMeterRegistry meters) {
        return new RedisRateLimiter(redis, perMinute, new TenantRateLimiter(perMinute, Clock.systemUTC()), meters);
    }

    @Test
    void replicasShareOneBudgetUnderConcurrency() throws Exception {
        var redis = template();
        var meters = new SimpleMeterRegistry();
        var a = replica(redis, 10, meters);   // two gateway replicas, same tenant
        var b = replica(redis, 10, meters);
        var allowed = new AtomicInteger();
        try (var pool = Executors.newFixedThreadPool(16)) {
            IntStream.range(0, 60).forEach(i -> pool.submit(() -> {
                if ((i % 2 == 0 ? a : b).tryAcquire("acme-shared")) {
                    allowed.incrementAndGet();
                }
            }));
        }
        assertThat(allowed.get()).isEqualTo(10);  // not 20: the bucket is global and atomic
        assertThat(meters.counter("sentinel.ratelimit.fallback").count()).isZero();
    }

    @Test
    void tenantsHaveIndependentBuckets() {
        var limiter = replica(template(), 2, new SimpleMeterRegistry());
        assertThat(limiter.tryAcquire("t1")).isTrue();
        assertThat(limiter.tryAcquire("t1")).isTrue();
        assertThat(limiter.tryAcquire("t1")).isFalse();
        assertThat(limiter.tryAcquire("t2")).isTrue();
    }

    @Test
    void tokensRefillOverTime() throws Exception {
        var limiter = replica(template(), 60, new SimpleMeterRegistry()); // 1 token/second
        for (int i = 0; i < 60; i++) {   // drains in ~50 ms, refilling ~0.05 tokens meanwhile
            limiter.tryAcquire("refill");
        }
        assertThat(limiter.tryAcquire("refill")).isFalse();
        Thread.sleep(1_100);
        assertThat(limiter.tryAcquire("refill")).isTrue();
        assertThat(limiter.tryAcquire("refill")).isFalse();
    }

    @Test
    void redisOutageFallsBackToLocalBucketsAndIsCounted() {
        var dead = new LettuceConnectionFactory(new RedisStandaloneConfiguration("localhost", 1));
        dead.afterPropertiesSet();
        try {
            var meters = new SimpleMeterRegistry();
            var limiter = replica(new StringRedisTemplate(dead), 2, meters);
            assertThat(limiter.tryAcquire("t")).isTrue();
            assertThat(limiter.tryAcquire("t")).isTrue();
            assertThat(limiter.tryAcquire("t")).isFalse();  // still bounded, just per-instance
            assertThat(meters.counter("sentinel.ratelimit.fallback").count()).isEqualTo(3);
        } finally {
            dead.destroy();
        }
    }
}
