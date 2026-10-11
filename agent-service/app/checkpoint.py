"""Checkpointer selection.

Set SENTINEL_CHECKPOINT_DSN to persist incident state in Postgres, which is required for
anything beyond one process: a paused incident must survive restarts and be resumable from
any replica. Without it, state lives in memory (tests and quick local runs).

In Postgres, checkpoints are tenant-isolated by row-level security like every other table: each
cursor binds `app.tenant_id` to the tenant of the authenticated request (app/tenancy.py), so even a
bug that addressed another tenant's thread id would read and write nothing.
"""
from __future__ import annotations

import os
from contextlib import asynccontextmanager

from langgraph.checkpoint.memory import InMemorySaver

from .tenancy import CURRENT_TENANT

TABLES = ("checkpoints", "checkpoint_blobs", "checkpoint_writes")  # all keyed by thread_id = "<tenant>:<id>"


def tenant_scoped_saver_class():
    from langgraph.checkpoint.postgres.aio import AsyncPostgresSaver

    class TenantScopedSaver(AsyncPostgresSaver):
        @asynccontextmanager
        async def _cursor(self, *, pipeline: bool = False):
            async with super()._cursor(pipeline=pipeline) as cur:
                # Session-level (autocommit pool): re-bound on every borrow, so a pooled connection
                # never carries the previous tenant. No tenant → '' → RLS shows nothing.
                await cur.execute("SELECT set_config('app.tenant_id', %s, false)", (CURRENT_TENANT.get() or "",))
                yield cur

        async def setup(self) -> None:
            # LangGraph's own migrations must see every row: lift FORCE while they run, then (re)apply
            # isolation. Idempotent; safe on every start.
            async with self._cursor() as cur:
                for t in TABLES:
                    await cur.execute(f"ALTER TABLE IF EXISTS {t} NO FORCE ROW LEVEL SECURITY")
            await super().setup()
            async with self._cursor() as cur:
                for t in TABLES:
                    await cur.execute(f"ALTER TABLE {t} ENABLE ROW LEVEL SECURITY")
                    await cur.execute(f"ALTER TABLE {t} FORCE ROW LEVEL SECURITY")
                    await cur.execute(
                        "SELECT 1 FROM pg_policies WHERE schemaname = current_schema() AND tablename = %s "
                        "AND policyname = 'tenant_isolation'", (t,))
                    if not await cur.fetchone():
                        await cur.execute(
                            f"CREATE POLICY tenant_isolation ON {t} "
                            "USING (split_part(thread_id, ':', 1) = current_setting('app.tenant_id', true))")

    return TenantScopedSaver


@asynccontextmanager
async def open_checkpointer(dsn: str | None = None):
    dsn = dsn or os.getenv("SENTINEL_CHECKPOINT_DSN")
    if not dsn:
        yield InMemorySaver()
        return
    from psycopg.rows import dict_row
    from psycopg_pool import AsyncConnectionPool

    # autocommit + dict_row are what AsyncPostgresSaver expects; prepare_threshold=0 keeps it
    # safe behind PgBouncer-style poolers.
    async with AsyncConnectionPool(dsn, open=False, max_size=10, kwargs={
            "autocommit": True, "prepare_threshold": 0, "row_factory": dict_row}) as pool:
        saver = tenant_scoped_saver_class()(pool)
        await saver.setup()  # idempotent: creates/migrates the checkpoint tables, then applies RLS
        yield saver
