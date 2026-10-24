"""Tests run against the real Sentinel Guard (built from ../../guard, memory mode), not a mock:
the SDK is only as correct as its contract with the actual API."""
import os
import shutil
import socket
import subprocess
import time
from pathlib import Path

import httpx
import pytest

ROOT = Path(__file__).resolve().parents[3]
AGENT_TOKEN, ADMIN_TOKEN = "sdk-agent-token", "sdk-admin-token"


def _go() -> str | None:
    local = ROOT / ".tools" / "go" / "bin" / "go"
    return str(local) if local.exists() else shutil.which("go")


@pytest.fixture(scope="session")
def guard_url(tmp_path_factory):
    binary = os.getenv("SENTINEL_GUARD_BIN")
    if not binary:
        go = _go()
        if not go:
            pytest.skip("needs Go (or SENTINEL_GUARD_BIN) to run the real guard")
        binary = str(tmp_path_factory.mktemp("guard") / "sentinel-guard")
        subprocess.run([go, "build", "-o", binary, "./cmd/sentinel-guard"], cwd=ROOT / "guard", check=True,
                       env={**os.environ, "GOFLAGS": "-modcacherw"})
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        metrics = s.getsockname()[1]
    proc = subprocess.Popen([binary], env={
        **os.environ, "SENTINEL_GUARD_MODE": "memory", "SENTINEL_GUARD_ADDR": f"127.0.0.1:{port}",
        "SENTINEL_GUARD_METRICS_ADDR": f"127.0.0.1:{metrics}",
        "SENTINEL_GUARD_AGENT_TOKENS": f"sdk-agent={AGENT_TOKEN}", "SENTINEL_GUARD_ADMIN_TOKEN": ADMIN_TOKEN},
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    url = f"http://127.0.0.1:{port}"
    for _ in range(100):
        try:
            if httpx.get(f"{url}/healthz", timeout=0.5).status_code == 200:
                break
        except httpx.HTTPError:
            time.sleep(0.05)
    else:
        proc.kill()
        raise RuntimeError("the guard did not start")
    yield url
    proc.terminate()
    proc.wait(timeout=5)


@pytest.fixture
def approve(guard_url):
    """What a human does (here through the single-process guard's admin API)."""
    def decide(request_id: str, approve: bool = True, approver: str = "alice@example.com"):
        r = httpx.post(f"{guard_url}/v1/actions/{request_id}/{'approve' if approve else 'deny'}",
                       headers={"Authorization": f"Bearer {ADMIN_TOKEN}"}, json={"approver": approver})
        assert r.status_code == 200, r.text
    return decide
