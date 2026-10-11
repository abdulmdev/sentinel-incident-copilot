package dev.sentinel.gateway.knowledge;

import static org.assertj.core.api.Assertions.assertThatThrownBy;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.verifyNoInteractions;

import java.util.List;
import java.util.Map;

import org.junit.jupiter.api.Test;
import org.springframework.ai.rag.Query;

class TenantDocumentRetrieverTest {

    @Test
    void failsClosedWithoutTenant() {
        KnowledgeSearch search = mock(KnowledgeSearch.class);
        var retriever = new TenantDocumentRetriever(search);

        assertThatThrownBy(() -> retriever.retrieve(new Query("how do I roll back?", List.of(), Map.of())))
                .isInstanceOf(IllegalStateException.class);
        verifyNoInteractions(search);
    }
}
