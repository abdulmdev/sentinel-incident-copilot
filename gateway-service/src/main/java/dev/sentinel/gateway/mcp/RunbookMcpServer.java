package dev.sentinel.gateway.mcp;

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

import dev.sentinel.gateway.knowledge.KnowledgeSearch;
import dev.sentinel.gateway.knowledge.SearchHit;
import dev.sentinel.gateway.tenant.Tenants;
import io.modelcontextprotocol.common.McpTransportContext;
import io.modelcontextprotocol.json.McpJsonMapper;
import io.modelcontextprotocol.server.McpStatelessServerFeatures;
import io.modelcontextprotocol.spec.McpSchema;
import org.springframework.ai.mcp.server.webmvc.transport.WebMvcStatelessServerTransport;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.security.oauth2.server.resource.authentication.JwtAuthenticationToken;
import tools.jackson.databind.json.JsonMapper;

/**
 * Exposes tenant-scoped runbook search as an MCP tool at {@code /mcp}.
 *
 * Stateless streamable HTTP: no MCP sessions, so any gateway replica can serve any call.
 * The tenant comes from the request's verified JWT. The context extractor runs on the
 * servlet thread where Spring Security authenticated the request, and copies the tenant into
 * the MCP transport context. Tool handlers read it from there, never from a thread-local,
 * because tool execution may hop threads.
 */
@Configuration
public class RunbookMcpServer {

    static final String TENANT = "sentinel.tenant_id";
    static final int MAX_RESULTS = 8;

    @Bean
    WebMvcStatelessServerTransport webMvcStatelessServerTransport(JsonMapper jsonMapper) {
        return WebMvcStatelessServerTransport.builder()
                .jsonMapper(new io.modelcontextprotocol.json.jackson3.JacksonMcpJsonMapper(jsonMapper))
                .messageEndpoint("/mcp")
                .contextExtractor(request -> request.principal()
                        .filter(JwtAuthenticationToken.class::isInstance)
                        .map(p -> McpTransportContext.create(Map.of(TENANT, Tenants.of(((JwtAuthenticationToken) p).getToken()))))
                        .orElse(McpTransportContext.EMPTY))
                .build();
    }

    /**
     * Registered as the server's tool list. With {@code tool-callback-converter=false} this is the whole
     * MCP tool surface, so no {@code @Tool} method elsewhere can be exposed to agents by accident.
     */
    @Bean
    List<McpStatelessServerFeatures.SyncToolSpecification> mcpTools(KnowledgeSearch search, JsonMapper json) {
        return List.of(searchRunbooksTool(search, json));
    }

    static McpStatelessServerFeatures.SyncToolSpecification searchRunbooksTool(KnowledgeSearch search, JsonMapper json) {
        var schema = new McpSchema.JsonSchema("object",
                Map.of("query", Map.of("type", "string",
                                "description", "Symptoms, error messages or the question, in natural language"),
                        "limit", Map.of("type", "integer", "minimum", 1, "maximum", MAX_RESULTS, "default", 4)),
                List.of("query"), false, null, null);
        var tool = McpSchema.Tool.builder()
                .name("search_runbooks")
                .description("Search this team's runbooks and past postmortems (hybrid keyword + semantic search). "
                        + "Returns the most relevant sections with docId, heading and text. Cite results by docId.")
                .inputSchema(schema)
                .build();
        return McpStatelessServerFeatures.SyncToolSpecification.builder()
                .tool(tool)
                .callHandler((ctx, call) -> {
                    Object tenant = ctx.get(TENANT);
                    if (!(tenant instanceof String tenantId)) {
                        return error("unauthenticated: no tenant on this request");
                    }
                    Object q = call.arguments().get("query");
                    if (!(q instanceof String query) || query.isBlank()) {
                        return error("query is required");
                    }
                    int limit = call.arguments().get("limit") instanceof Number n
                            ? Math.clamp(n.intValue(), 1, MAX_RESULTS) : 4;
                    List<SearchHit> hits = search.search(tenantId, query, KnowledgeSearch.Mode.HYBRID, limit);
                    List<Map<String, Object>> results = hits.stream().map(h -> {
                        Map<String, Object> m = new LinkedHashMap<>();
                        m.put("docId", h.docId());
                        m.put("title", h.title());
                        m.put("heading", h.heading());
                        m.put("text", h.content());
                        return m;
                    }).toList();
                    return McpSchema.CallToolResult.builder()
                            .addTextContent(json.writeValueAsString(results))
                            .isError(false)
                            .build();
                })
                .build();
    }

    private static McpSchema.CallToolResult error(String message) {
        return McpSchema.CallToolResult.builder().addTextContent(message).isError(true).build();
    }
}
