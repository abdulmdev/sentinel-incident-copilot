"""Stands in for Slack in the quickstart: prints each notice Sentinel Guard sends to its webhook
(SENTINEL_GUARD_NOTIFY_URL). Watch it with `docker compose logs -f notifications`."""
import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class Sink(BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        try:
            print(json.loads(body)["text"], flush=True)
        except (ValueError, KeyError):
            print(body.decode(errors="replace"), flush=True)
        self.send_response(204)
        self.end_headers()

    def log_message(self, *args):  # only the notices
        pass


if __name__ == "__main__":
    print("notification sink listening on :8000", file=sys.stderr, flush=True)
    HTTPServer(("", 8000), Sink).serve_forever()
