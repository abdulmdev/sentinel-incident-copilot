package dev.sentinel.gateway.knowledge;

import java.util.List;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;

import dev.sentinel.gateway.tenant.Tenants;
import jakarta.validation.Valid;
import jakarta.validation.constraints.Max;
import jakarta.validation.constraints.Min;
import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.Pattern;
import jakarta.validation.constraints.Size;
import org.springframework.http.HttpStatus;
import org.springframework.kafka.core.KafkaTemplate;
import org.springframework.security.core.annotation.AuthenticationPrincipal;
import org.springframework.security.oauth2.jwt.Jwt;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.ResponseStatus;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.web.server.ResponseStatusException;
import tools.jackson.databind.json.JsonMapper;

@RestController
@RequestMapping("/api/v1/knowledge")
public class KnowledgeController {

    static final String TOPIC = "knowledge.documents";

    public record Upload(
            @NotBlank @Pattern(regexp = "[a-z0-9][a-z0-9._-]{0,127}") String docId,
            @NotBlank @Size(max = 500) String source,
            @NotBlank @Size(max = 300) String title,
            @NotBlank @Size(max = 200_000) String content) {
    }

    /** Wire format shared with ingestion-service's KnowledgeDocument. */
    record KnowledgeDocument(String tenantId, String docId, String source, String title, String content) {
    }

    public record Accepted(String docId, String status) {
    }

    private final KafkaTemplate<String, String> kafka;
    private final JsonMapper json;
    private final KnowledgeSearch search;

    public KnowledgeController(KafkaTemplate<String, String> kafka, JsonMapper json, KnowledgeSearch search) {
        this.kafka = kafka;
        this.json = json;
        this.search = search;
    }

    /**
     * Accepted once Kafka has durably acknowledged the write. Indexing happens asynchronously
     * in ingestion-service, so a slow embedding model never blocks the API.
     */
    @PostMapping("/documents")
    @ResponseStatus(HttpStatus.ACCEPTED)
    public Accepted upload(@AuthenticationPrincipal Jwt jwt, @Valid @RequestBody Upload req)
            throws InterruptedException {
        String tenant = Tenants.of(jwt);
        var event = new KnowledgeDocument(tenant, req.docId(), req.source(), req.title(), req.content());
        try {
            kafka.send(TOPIC, tenant + ":" + req.docId(), json.writeValueAsString(event)).get(5, TimeUnit.SECONDS);
        } catch (ExecutionException | TimeoutException e) {
            throw new ResponseStatusException(HttpStatus.SERVICE_UNAVAILABLE, "ingestion queue unavailable", e);
        }
        return new Accepted(req.docId(), "QUEUED");
    }

    @GetMapping("/search")
    public List<SearchHit> search(@AuthenticationPrincipal Jwt jwt,
                                  @RequestParam @NotBlank @Size(max = 1000) String q,
                                  @RequestParam(defaultValue = "hybrid") @Pattern(regexp = "(?i)vector|keyword|hybrid") String mode,
                                  @RequestParam(defaultValue = "5") @Min(1) @Max(50) int k) {
        return search.search(Tenants.of(jwt), q, KnowledgeSearch.Mode.parse(mode), k);
    }
}
