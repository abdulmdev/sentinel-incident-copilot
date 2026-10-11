"""The tenant of the current request, as proven by the gateway's token.

Set by the auth dependency; read by the checkpointer to bind Postgres row-level security. Unset
(no authenticated request) means no tenant, which RLS treats as "see nothing".
"""
from contextvars import ContextVar

CURRENT_TENANT: ContextVar[str | None] = ContextVar("sentinel_tenant", default=None)
