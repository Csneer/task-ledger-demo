// Package web embeds the task-first browser client. Only listed public assets
// are served; source files, tests and local configuration are never exposed.
package web

import (
	"embed"
	"net/http"
	"strings"
)

//go:embed index.html *.js *.css
var assets embed.FS

func Handler(api http.Handler) http.Handler {
	public := map[string]string{"/": "index.html", "/app.js": "app.js", "/api.js": "api.js", "/model.js": "model.js", "/graph.js": "graph.js", "/history.js": "history.js", "/journeys.js": "journeys.js", "/ui.css": "ui.css", "/graph.css": "graph.css"}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/") {
			api.ServeHTTP(w, r)
			return
		}
		name, ok := public[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		data, err := assets.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; font-src 'self'; base-uri 'none'; object-src 'none'; frame-ancestors 'none'; form-action 'none'")
		switch {
		case strings.HasSuffix(name, ".js"):
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		case strings.HasSuffix(name, ".css"):
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
		default:
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	})
}
