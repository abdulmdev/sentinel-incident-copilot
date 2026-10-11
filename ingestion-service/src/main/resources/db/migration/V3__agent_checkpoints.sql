-- LangGraph checkpoints (agent-service) live in their own schema, owned by the app role so the
-- checkpointer can create and migrate its tables without superuser rights.
-- Tenant isolation here is by thread key (tenant:incident), enforced in agent-service.
CREATE SCHEMA IF NOT EXISTS langgraph AUTHORIZATION sentinel_app;
