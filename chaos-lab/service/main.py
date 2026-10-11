"""A small demo microservice that can be broken on purpose.

One image runs as several services (SERVICE_NAME env). Faults are injected through
POST /chaos/fault, and the service then shows realistic symptoms in its metrics and logs.
Sentinel's agents investigate those symptoms.
"""
import asyncio
import json
import logging
import os
import random
import time
from collections import deque
from datetime import datetime, timezone

import httpx
from fastapi import FastAPI, HTTPException
from fastapi.responses import PlainTextResponse
from prometheus_client import CONTENT_TYPE_LATEST, Counter, Gauge, Histogram, generate_latest
from pydantic import BaseModel

SERVICE = os.getenv("SERVICE_NAME", "svc")
DOWNSTREAM = os.getenv("DOWNSTREAM_URL")
FAULTS = {"none", "latency", "errors", "memory_leak", "db_pool_exhaustion", "bad_deploy"}

REQUESTS = Counter("chaos_requests_total", "Requests", ["service", "status"])
LATENCY = Histogram("chaos_request_seconds", "Latency", ["service"])
MEMORY = Gauge("chaos_memory_mb", "Simulated heap usage", ["service"])
DB_POOL = Gauge("chaos_db_pool_in_use", "DB connections in use (max 20)", ["service"])
VERSION = Gauge("chaos_deploy_version", "Currently deployed build", ["service", "version"])

app = FastAPI(title=f"chaos-lab:{SERVICE}")
log_buffer: deque = deque(maxlen=500)
state = {"fault": "none", "version": "1.4.2", "leak": [], "pool": 4, "deploys": []}
logger = logging.getLogger(SERVICE)
logging.basicConfig(level=logging.INFO, format="%(message)s")


def log(level: str, msg: str, **fields) -> None:
    entry = {"ts": datetime.now(timezone.utc).isoformat(), "service": SERVICE,
             "level": level, "msg": msg, "version": state["version"], **fields}
    log_buffer.append(entry)
    logger.info(json.dumps(entry))


class FaultRequest(BaseModel):
    type: str


@app.post("/chaos/fault")
def inject(req: FaultRequest):
    if req.type not in FAULTS:
        raise HTTPException(400, f"unknown fault, expected one of {sorted(FAULTS)}")
    state["fault"] = req.type
    if req.type == "bad_deploy" and state["version"] != "1.5.0":
        VERSION.labels(SERVICE, state["version"]).set(0)
        state["version"] = "1.5.0"
        state["deploys"].append({"version": "1.5.0", "commit": "a1b2c3d",
                                 "ts": datetime.now(timezone.utc).isoformat(),
                                 "change": "switch payment client to async pool"})
        log("INFO", "deployment completed", deploy_id="d-7731", commit="a1b2c3d",
            change="switch payment client to async pool")
    if req.type == "none":  # full heal: roll back and forget past symptoms
        VERSION.labels(SERVICE, state["version"]).set(0)
        state.update(leak=[], pool=4, version="1.4.2", deploys=[])
        log_buffer.clear()
    # stdout only: if this went into the log buffer, agents would read the answer as evidence
    logger.info(json.dumps({"service": SERVICE, "msg": "chaos fault injected", "fault": req.type}))
    return {"service": SERVICE, "fault": state["fault"]}


# --- Operations: what a deploy/ops tool does. The remediation MCP server calls these. ------------
# Each operation fixes only the fault it actually addresses (a rollback does not cure a memory
# leak), so post-remediation verification is meaningful: a wrong fix is caught by the probe.
BASELINE_VERSION = "1.4.2"
FIXED_BY = {"rollback": {"bad_deploy"}, "restart": {"memory_leak"}, "increase_pool": {"db_pool_exhaustion"},
            "failover": {"errors", "latency"}, "scale_out": set()}  # more replicas don't fix any lab fault


class PoolRequest(BaseModel):
    size: int = 40


class ScaleRequest(BaseModel):
    replicas: int = 4


def _apply(op: str, **details) -> dict:
    fixed = state["fault"] in FIXED_BY[op]
    if fixed:
        state["fault"] = "none"
    log("INFO", f"remediation applied: {op}", fixed_active_fault=fixed, **details)
    return {"service": SERVICE, "operation": op, "fixed_active_fault": fixed, "version": state["version"], **details}


@app.post("/ops/rollback")
def op_rollback():
    previous = state["version"]
    if previous != BASELINE_VERSION:
        VERSION.labels(SERVICE, previous).set(0)
        state["version"] = BASELINE_VERSION
        state["deploys"].append({"version": BASELINE_VERSION, "commit": "rollback",
                                 "ts": datetime.now(timezone.utc).isoformat(), "change": f"rollback from {previous}"})
    return _apply("rollback", from_version=previous, to_version=BASELINE_VERSION)


@app.post("/ops/restart")
def op_restart():
    state.update(leak=[], pool=4)
    return _apply("restart")


@app.post("/ops/pool")
def op_pool(req: PoolRequest):
    state["pool"] = 4
    return _apply("increase_pool", pool_max=req.size)


@app.post("/ops/scale")
def op_scale(req: ScaleRequest):
    return _apply("scale_out", replicas=req.replicas)


@app.post("/ops/failover")
def op_failover():
    return _apply("failover", upstream="pricing-api", served_from="cache")


@app.get("/work")
async def work():
    start = time.perf_counter()
    fault = state["fault"]
    try:
        if fault == "latency":
            await asyncio.sleep(random.uniform(1.5, 3.0))
            log("WARN", "slow upstream call", upstream="pricing-api", elapsed_ms=2400)
        elif fault == "errors" and random.random() < 0.6:
            log("ERROR", "upstream returned 503", upstream="pricing-api")
            raise HTTPException(503, "upstream unavailable")
        elif fault == "memory_leak":
            state["leak"].append(bytearray(256 * 1024))
            if len(state["leak"]) % 20 == 0:
                log("WARN", "GC pause exceeded threshold", gc_pause_ms=850,
                    heap_mb=len(state["leak"]) // 4)
        elif fault == "db_pool_exhaustion":
            state["pool"] = min(20, state["pool"] + 1)
            if state["pool"] >= 20:
                log("ERROR", "timeout acquiring DB connection", pool_max=20, wait_ms=30000)
                raise HTTPException(500, "db pool exhausted")
        elif fault == "bad_deploy" and random.random() < 0.4:
            log("ERROR", "NullPointerException in PaymentClient.charge",
                stack="PaymentClient.charge:88 <- CheckoutHandler.handle:41")
            raise HTTPException(500, "internal error")

        if DOWNSTREAM:
            async with httpx.AsyncClient(timeout=5) as client:
                r = await client.get(f"{DOWNSTREAM}/work")
                if r.status_code >= 500:
                    log("ERROR", "downstream failure", downstream=DOWNSTREAM, status=r.status_code)
                    raise HTTPException(502, "downstream failure")
        REQUESTS.labels(SERVICE, "ok").inc()
        return {"service": SERVICE, "ok": True}
    except HTTPException:
        REQUESTS.labels(SERVICE, "error").inc()
        raise
    finally:
        LATENCY.labels(SERVICE).observe(time.perf_counter() - start)
        MEMORY.labels(SERVICE).set(120 + len(state["leak"]) / 4)
        DB_POOL.labels(SERVICE).set(state["pool"])
        VERSION.labels(SERVICE, state["version"]).set(1)


@app.get("/chaos/logs")
def recent_logs(limit: int = 100, level: str | None = None):
    entries = [e for e in log_buffer if level is None or e["level"] == level]
    return entries[-limit:]


@app.get("/chaos/deploys")
def deploys():
    """Deploys since the service started. The baseline build is not listed."""
    return state["deploys"]


@app.get("/metrics")
def metrics():
    return PlainTextResponse(generate_latest(), media_type=CONTENT_TYPE_LATEST)


@app.get("/healthz")
def healthz():
    return {"status": "ok"}
