package server

import (
	"embed"
	"io/fs"
	"net/http"
)

// The approvals inbox: a small page for deciding on agents' requests in a browser, for
// single-process mode (docker run, docker compose), where people approve with their own approver
// token (or the shared admin token).
// In Kubernetes people approve through the API server (kubectl sentinel approve), so the inbox
// is not served there. The page holds no state of its own: it calls the same HTTP API.
//
//go:embed inbox
var inboxFiles embed.FS

// inboxCSP allows only the page's own script, styles and API calls: everything it shows comes
// from agents, and it never loads anything from elsewhere.
const inboxCSP = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; " +
	"base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func (s *Server) mountInbox(mux *http.ServeMux) {
	if s.AdminToken == "" && len(s.ApproverTokens) == 0 {
		return
	}
	files, _ := fs.Sub(inboxFiles, "inbox")
	static := http.StripPrefix("/inbox/", http.FileServerFS(files))
	mux.Handle("GET /inbox/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", inboxCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		static.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/inbox/", http.StatusFound)
	})
}
