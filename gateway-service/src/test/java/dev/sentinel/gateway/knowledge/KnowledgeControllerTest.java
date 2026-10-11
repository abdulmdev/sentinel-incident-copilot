package dev.sentinel.gateway.knowledge;

import static org.mockito.ArgumentMatchers.any;
import static org.mockito.ArgumentMatchers.argThat;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;
import static org.springframework.security.test.web.servlet.request.SecurityMockMvcRequestPostProcessors.jwt;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.jsonPath;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import java.util.List;
import java.util.concurrent.CompletableFuture;

import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.http.MediaType;
import org.springframework.kafka.core.KafkaTemplate;
import org.springframework.kafka.support.SendResult;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;

@SpringBootTest
@AutoConfigureMockMvc
class KnowledgeControllerTest {

    @Autowired
    MockMvc mvc;

    @MockitoBean
    KafkaTemplate<String, String> kafka;

    @MockitoBean
    KnowledgeSearch search;

    @Test
    void uploadPublishesWithTenantFromTokenAndPerDocumentKey() throws Exception {
        when(kafka.send(any(String.class), any(String.class), any(String.class)))
                .thenReturn(CompletableFuture.completedFuture((SendResult<String, String>) null));

        mvc.perform(post("/api/v1/knowledge/documents")
                        .with(jwt().jwt(j -> j.claim("tenant_id", "acme")))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("""
                                {"docId":"db-pool","source":"runbooks/db-pool.md","title":"DB pool","content":"# Fix","tenantId":"globex"}"""))
                .andExpect(status().isAccepted())
                .andExpect(jsonPath("$.status").value("QUEUED"));

        verify(kafka).send(eq("knowledge.documents"), eq("acme:db-pool"),
                argThat(v -> v.contains("\"tenantId\":\"acme\"") && !v.contains("globex")));
    }

    @Test
    void kafkaFailureIs503NotSilentDataLoss() throws Exception {
        when(kafka.send(any(String.class), any(String.class), any(String.class)))
                .thenReturn(CompletableFuture.failedFuture(new RuntimeException("broker down")));

        mvc.perform(post("/api/v1/knowledge/documents")
                        .with(jwt().jwt(j -> j.claim("tenant_id", "acme")))
                        .contentType(MediaType.APPLICATION_JSON)
                        .content("{\"docId\":\"x\",\"source\":\"s\",\"title\":\"t\",\"content\":\"c\"}"))
                .andExpect(status().isServiceUnavailable());
    }

    @Test
    void searchIsScopedToTokenTenant() throws Exception {
        when(search.search(eq("acme"), eq("db timeouts"), eq(KnowledgeSearch.Mode.KEYWORD), eq(3)))
                .thenReturn(List.of(new SearchHit(1, "db-pool", "DB pool", "s", "DB pool > Fix", "Raise pool", 0.5)));

        mvc.perform(get("/api/v1/knowledge/search").param("q", "db timeouts").param("mode", "keyword").param("k", "3")
                        .with(jwt().jwt(j -> j.claim("tenant_id", "acme"))))
                .andExpect(status().isOk())
                .andExpect(jsonPath("$[0].docId").value("db-pool"));
    }

    @Test
    void rejectsBadModeAndK() throws Exception {
        mvc.perform(get("/api/v1/knowledge/search").param("q", "x").param("mode", "magic")
                        .with(jwt().jwt(j -> j.claim("tenant_id", "acme"))))
                .andExpect(status().isBadRequest());
        mvc.perform(get("/api/v1/knowledge/search").param("q", "x").param("k", "500")
                        .with(jwt().jwt(j -> j.claim("tenant_id", "acme"))))
                .andExpect(status().isBadRequest());
    }
}
