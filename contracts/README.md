# Service contracts

Example messages that cross a service boundary. **Both sides test against these files**, so a
change that breaks the other service fails a build instead of production.

| File | Producer → consumer | Checked by |
|---|---|---|
| `agent-open-incident.request.json` | gateway → agent-service `POST /incidents` | `IncidentControllerTest` (Java), `test_contracts.py` (Python) |
| `knowledge-document.event.json` | gateway → Kafka → ingestion-service | `KnowledgeControllerTest`, `KnowledgeDocumentContractTest` |

| MCP tool results (observability) | mcp-observability → agent-service | `test_server.py` asserts one text block with one JSON object; the agent's `FakeToolBox` emits the same shapes |

The tool token travels in the `X-Sentinel-Tool-Token` header, not the body, so the agent can't persist it.
