package dev.sentinel.gateway.tenant;

import java.io.IOException;

import io.micrometer.core.instrument.MeterRegistry;
import jakarta.servlet.FilterChain;
import jakarta.servlet.ServletException;
import jakarta.servlet.http.HttpServletRequest;
import jakarta.servlet.http.HttpServletResponse;
import dev.sentinel.gateway.alert.AlertController;
import dev.sentinel.gateway.mcp.ToolTokens;
import org.springframework.http.HttpStatus;
import org.springframework.security.core.context.SecurityContextHolder;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationToken;
import org.springframework.stereotype.Component;
import org.springframework.web.filter.OncePerRequestFilter;

@Component
public class TenantRateLimitFilter extends OncePerRequestFilter {

    private final RateLimiter limiter;
    private final MeterRegistry meters;

    public TenantRateLimitFilter(RateLimiter limiter, MeterRegistry meters) {
        this.limiter = limiter;
        this.meters = meters;
    }

    @Override
    protected void doFilterInternal(HttpServletRequest req, HttpServletResponse res, FilterChain chain)
            throws ServletException, IOException {
        if (SecurityContextHolder.getContext().getAuthentication() instanceof JwtAuthenticationToken auth) {
            String tenant = auth.getToken().getClaimAsString(Tenants.CLAIM);
            // Machine traffic gets its own bucket per tenant: an incident's MCP calls or an alert storm
            // must not starve the humans' API budget, and vice versa.
            var audience = auth.getToken().getAudience();
            String bucket = tenant;
            if (audience != null && audience.contains(ToolTokens.AUDIENCE)) {
                bucket = tenant + ":agent";
            } else if (audience != null && audience.contains(AlertController.AUDIENCE)) {
                bucket = tenant + ":alerts";
            }
            if (tenant != null && !limiter.tryAcquire(bucket)) {
                meters.counter("sentinel.ratelimit.rejected", "tenant", tenant).increment();
                res.setStatus(HttpStatus.TOO_MANY_REQUESTS.value());
                res.setHeader("Retry-After", "1");
                return;
            }
        }
        chain.doFilter(req, res);
    }
}
