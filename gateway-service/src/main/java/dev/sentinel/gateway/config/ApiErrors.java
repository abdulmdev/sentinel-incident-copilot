package dev.sentinel.gateway.config;

import com.anthropic.errors.AnthropicServiceException;
import com.anthropic.errors.PermissionDeniedException;
import com.anthropic.errors.RateLimitException;
import com.anthropic.errors.UnauthorizedException;
import dev.sentinel.gateway.remediation.GuardClient;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpStatus;
import org.springframework.http.ProblemDetail;
import org.springframework.web.bind.annotation.ExceptionHandler;
import org.springframework.web.bind.annotation.RestControllerAdvice;
import org.springframework.web.client.HttpClientErrorException;
import org.springframework.web.client.ResourceAccessException;

@RestControllerAdvice
public class ApiErrors {

    private static final Logger log = LoggerFactory.getLogger(ApiErrors.class);

    /** Pass the agent's 404/409 through so clients see the real outcome. */
    @ExceptionHandler(HttpClientErrorException.class)
    ProblemDetail agentClientError(HttpClientErrorException e) {
        return ProblemDetail.forStatusAndDetail(e.getStatusCode(), e.getStatusText());
    }

    /** The guard refused or couldn't record a decision: nothing was resumed, the incident still waits. */
    @ExceptionHandler(GuardClient.GuardException.class)
    ProblemDetail guardRefused(GuardClient.GuardException e) {
        return ProblemDetail.forStatusAndDetail(e.status(), "Sentinel Guard: " + e.getMessage());
    }

    @ExceptionHandler(ResourceAccessException.class)
    ProblemDetail agentUnavailable(ResourceAccessException e) {
        return ProblemDetail.forStatusAndDetail(HttpStatus.SERVICE_UNAVAILABLE, "agent-service unavailable");
    }

    /** Our credentials are missing or wrong: an operator problem, not the caller's. */
    @ExceptionHandler({UnauthorizedException.class, PermissionDeniedException.class})
    ProblemDetail llmNotConfigured(AnthropicServiceException e) {
        log.error("LLM provider rejected our credentials (is ANTHROPIC_API_KEY set?): {}", e.getMessage());
        return ProblemDetail.forStatusAndDetail(HttpStatus.SERVICE_UNAVAILABLE, "LLM provider not configured");
    }

    @ExceptionHandler(RateLimitException.class)
    ProblemDetail llmRateLimited(RateLimitException e) {
        return ProblemDetail.forStatusAndDetail(HttpStatus.TOO_MANY_REQUESTS, "LLM provider rate limit; retry shortly");
    }

    @ExceptionHandler(AnthropicServiceException.class)
    ProblemDetail llmFailed(AnthropicServiceException e) {
        log.warn("LLM provider error: {}", e.getMessage());
        return ProblemDetail.forStatusAndDetail(HttpStatus.BAD_GATEWAY, "LLM provider error");
    }
}
