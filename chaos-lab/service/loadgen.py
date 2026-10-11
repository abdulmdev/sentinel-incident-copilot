"""Continuous background traffic so Prometheus sees error rates and latency (alerts need a signal).

  python3 chaos-lab/service/loadgen.py                       # ~5 req/s to checkout (host ports)
  LOADGEN_TARGETS=http://checkout:8000 python loadgen.py   # in a container
"""
import os
import time
import urllib.request

TARGETS = [t for t in os.getenv("LOADGEN_TARGETS", "http://localhost:8101").split(",") if t]
INTERVAL = float(os.getenv("LOADGEN_INTERVAL_S", "0.2"))

while True:
    for base in TARGETS:
        try:
            urllib.request.urlopen(f"{base}/work", timeout=5).read()
        except Exception:
            pass  # errors are the point when a fault is active
    time.sleep(INTERVAL)
