package server

import (
	"strings"
	"testing"
)

func TestInboxIsServedOnlyWithHTTPApprovals(t *testing.T) {
	s, _ := newServer(t)
	h := s.Handler()

	r := get(h, "/", "")
	if r.Code != 302 || r.Header().Get("Location") != "/inbox/" {
		t.Fatalf("/ should open the inbox: %d %q", r.Code, r.Header().Get("Location"))
	}
	for path, want := range map[string]string{
		"/inbox/":          "text/html",
		"/inbox/inbox.js":  "javascript",
		"/inbox/inbox.css": "text/css",
	} {
		r := get(h, path, "")
		if r.Code != 200 || !strings.Contains(r.Header().Get("Content-Type"), want) {
			t.Fatalf("%s: %d %q", path, r.Code, r.Header().Get("Content-Type"))
		}
		csp := r.Header().Get("Content-Security-Policy")
		for _, directive := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
			if !strings.Contains(csp, directive) {
				t.Fatalf("%s: CSP %q lacks %q", path, csp, directive)
			}
		}
		if r.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: no nosniff", path)
		}
	}
	// The page holds no data: listing requests still needs the admin token.
	if r := get(h, "/v1/actions", ""); r.Code != 401 {
		t.Fatalf("listing without a token: %d", r.Code)
	}
	if r := get(h, "/inbox/nope.html", ""); r.Code != 404 {
		t.Fatalf("unknown inbox file: %d", r.Code)
	}

	// Agents' text reaches the page only as data: no script builds HTML from it.
	js := get(h, "/inbox/inbox.js", "").Body.String()
	for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval("} {
		if strings.Contains(js, sink) {
			t.Fatalf("inbox.js uses %s", sink)
		}
	}
	page := get(h, "/inbox/", "").Body.String()
	for _, inline := range []string{"<script>", " onclick=", " onsubmit=", " onload=", "javascript:"} {
		if strings.Contains(page, inline) {
			t.Fatalf("the inbox page has inline script (%s)", inline)
		}
	}

	// Without HTTP approvals (Kubernetes: people approve through the API server) there is no inbox.
	s.AdminToken = ""
	h = s.Handler()
	for _, path := range []string{"/", "/inbox/"} {
		if r := get(h, path, ""); r.Code != 404 {
			t.Fatalf("%s without an admin token: %d", path, r.Code)
		}
	}
}
