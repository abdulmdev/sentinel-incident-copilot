package dev.sentinel.gateway.incident;

import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Size;

public final class IncidentRequests {

    private IncidentRequests() {
    }

    public record Open(
            @NotBlank @Pattern(regexp = "[a-z0-9-]{1,63}") String service,
            @NotBlank @Size(max = 500) String summary,
            @Pattern(regexp = "SEV[123]") String severity) {
    }

    public record Decide(boolean approved, @Size(max = 1000) String note) {
    }
}
