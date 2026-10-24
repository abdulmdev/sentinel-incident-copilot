# Database connection pool exhaustion

Owner: data-platform. Applies to every service using HikariCP against the orders Postgres cluster.

## Symptoms
- Log line: `timeout acquiring DB connection` with `wait_ms=30000`.
- `chaos_db_pool_in_use` pinned at the pool maximum (20).
- Requests fail with HTTP 500 after ~30 seconds rather than immediately.

## Likely causes
- A connection leak: code path that borrows a connection and never returns it (missing try-with-resources).
- Long-running transactions holding connections, often a slow query after a missing index.
- Traffic spike beyond what the pool was sized for.

## Mitigation
1. Short term: raise `maximumPoolSize` from 20 to 40 via the config service. The database allows 400 connections in total, so check `pg_stat_activity` headroom first.
2. Restart the affected pods to release leaked connections.
3. Enable `leakDetectionThreshold=20000` to log the stack trace of the leaking borrower.

## Do not
Do not raise the pool above 60 per pod: it moves the bottleneck onto Postgres and can take the whole cluster down.
