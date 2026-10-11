"""Fault injector + traffic generator for the demo.

  python chaos-lab/inject.py checkout bad_deploy      # break checkout
  python chaos-lab/inject.py checkout none            # heal it
"""
import sys
import time
import urllib.request
import json

PORTS = {"checkout": 8101, "inventory": 8102}


def post(url: str, body: dict) -> str:
    req = urllib.request.Request(url, json.dumps(body).encode(), {"Content-Type": "application/json"})
    with urllib.request.urlopen(req) as r:
        return r.read().decode()


def main() -> None:
    if len(sys.argv) != 3 or sys.argv[1] not in PORTS:
        sys.exit(f"usage: inject.py <{'|'.join(PORTS)}> <fault>")
    service, fault = sys.argv[1], sys.argv[2]
    base = f"http://localhost:{PORTS[service]}"
    print(post(f"{base}/chaos/fault", {"type": fault}))
    print("generating traffic for 20s ...")
    end = time.time() + 20
    ok = err = 0
    while time.time() < end:
        try:
            urllib.request.urlopen(f"{base}/work", timeout=5)
            ok += 1
        except Exception:
            err += 1
    print(f"done: ok={ok} errors={err}")


if __name__ == "__main__":
    main()
