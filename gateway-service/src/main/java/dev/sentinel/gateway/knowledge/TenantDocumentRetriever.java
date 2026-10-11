package dev.sentinel.gateway.knowledge;

import java.util.List;
import java.util.Map;

import org.springframework.ai.document.Document;
import org.springframework.ai.rag.Query;
import org.springframework.ai.rag.retrieval.search.DocumentRetriever;
import org.springframework.stereotype.Component;

/** Plugs hybrid search into Spring AI's RetrievalAugmentationAdvisor. */
@Component
public class TenantDocumentRetriever implements DocumentRetriever {

    /** Advisor param carrying the caller's tenant, taken from the verified JWT. */
    public static final String TENANT = "sentinel.tenant_id";
    static final int TOP_K = 5;

    private final KnowledgeSearch search;

    public TenantDocumentRetriever(KnowledgeSearch search) {
        this.search = search;
    }

    @Override
    public List<Document> retrieve(Query query) {
        Object tenant = query.context().get(TENANT);
        if (!(tenant instanceof String tenantId) || tenantId.isBlank()) {
            // Fail closed: retrieving without a tenant must never fall back to "all tenants".
            throw new IllegalStateException("retrieval requires a tenant");
        }
        return search.search(tenantId, query.text(), KnowledgeSearch.Mode.HYBRID, TOP_K).stream()
                .map(h -> Document.builder()
                        .id(String.valueOf(h.chunkId()))
                        .text(h.content())
                        .metadata(Map.of("docId", h.docId(), "title", h.title(), "heading", h.heading(),
                                "source", h.source()))
                        .score(h.score())
                        .build())
                .toList();
    }
}
