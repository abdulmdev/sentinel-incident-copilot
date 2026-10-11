package dev.sentinel.gateway.mcp;

import static org.hamcrest.Matchers.containsString;
import static org.mockito.ArgumentMatchers.anyInt;
import static org.mockito.ArgumentMatchers.anyString;
import static org.mockito.ArgumentMatchers.eq;
import static org.mockito.Mockito.verify;
import static org.mockito.Mockito.when;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.get;
import static org.springframework.test.web.servlet.request.MockMvcRequestBuilders.post;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.content;
import static org.springframework.test.web.servlet.result.MockMvcResultMatchers.status;

import java.util.List;

import dev.sentinel.gateway.knowledge.KnowledgeSearch;
import dev.sentinel.gateway.knowledge.SearchHit;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.webmvc.test.autoconfigure.AutoConfigureMockMvc;
import org.springframework.http.MediaType;
import org.springframework.test.context.bean.override.mockito.MockitoBean;
import org.springframework.test.web.servlet.MockMvc;
import org.springframework.test.web.servlet.request.MockHttpServletRequestBuilder;

/** End to end through Spring Security with real minted tokens, not mocked authentication. */
@SpringBootTest
@AutoConfigureMockMvc
class McpEndpointTest {

    @Autowired
    MockMvc mvc;

    @Autowired
    ToolTokens toolTokens;

    @MockitoBean
    KnowledgeSearch search;

    static final String CALL = """
            {"jsonrpc":"2.0","id":1,"method":"tools/call",
             "params":{"name":"search_runbooks","arguments":{"query":"db connection timeouts","limit":2}}}""";

    static MockHttpServletRequestBuilder mcp(String body, String token) {
        return post("/mcp").header("Authorization", "Bearer " + token)
                .contentType(MediaType.APPLICATION_JSON)
                .accept(MediaType.APPLICATION_JSON, MediaType.TEXT_EVENT_STREAM)
                .content(body);
    }

    @Test
    void listsTheRunbookTool() throws Exception {
        mvc.perform(mcp("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}", toolTokens.mint("acme", "inc-1")))
                .andExpect(status().isOk())
                .andExpect(content().string(containsString("search_runbooks")));
    }

    @Test
    void toolCallIsScopedToTheTokensTenant() throws Exception {
        when(search.search(eq("acme"), eq("db connection timeouts"), eq(KnowledgeSearch.Mode.HYBRID), eq(2)))
                .thenReturn(List.of(new SearchHit(1, "db-connection-pool", "DB pool", "s", "DB pool > Mitigation",
                        "Raise maximumPoolSize", 0.03)));

        mvc.perform(mcp(CALL, toolTokens.mint("acme", "inc-1")))
                .andExpect(status().isOk())
                .andExpect(content().string(containsString("db-connection-pool")));
        verify(search).search(eq("acme"), anyString(), eq(KnowledgeSearch.Mode.HYBRID), anyInt());
    }

    @Test
    void userTokensCannotCallMcp() throws Exception {
        String userToken = DevTokens.user("alice@acme", "acme");
        mvc.perform(mcp(CALL, userToken)).andExpect(status().isForbidden());
    }

    @Test
    void toolTokensCannotCallTheApi() throws Exception {
        String toolToken = toolTokens.mint("acme", "inc-1");
        mvc.perform(get("/api/v1/knowledge/search").param("q", "x").header("Authorization", "Bearer " + toolToken))
                .andExpect(status().isForbidden());
        mvc.perform(post("/api/v1/incidents/7f9c2c4e-1a2b-4c3d-9e8f-0a1b2c3d4e5f/decision")
                        .header("Authorization", "Bearer " + toolToken)
                        .contentType(MediaType.APPLICATION_JSON).content("{\"approved\":true}"))
                .andExpect(status().isForbidden());
    }

    @Test
    void anonymousIsRejected() throws Exception {
        mvc.perform(post("/mcp").contentType(MediaType.APPLICATION_JSON).content(CALL))
                .andExpect(status().isUnauthorized());
    }
}
