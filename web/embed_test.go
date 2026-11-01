package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPublicAssetsAndAPIBoundary(t *testing.T) {
	h := Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	for _, tc := range []struct {
		path, method string
		status       int
	}{{"/", "GET", 200}, {"/app.js", "GET", 200}, {"/graph.css", "HEAD", 200}, {"/embed.go", "GET", 404}, {"/model.test.mjs", "GET", 404}, {"/", "POST", 405}, {"/v1/queries", "POST", 401}, {"/../README.md", "GET", 404}} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d", tc.method, tc.path, w.Code)
		}
		if tc.status == 200 && !strings.Contains(w.Header().Get("Content-Security-Policy"), "connect-src 'self'") {
			t.Fatal("same-origin policy missing")
		}
		if tc.method == "HEAD" && w.Body.Len() != 0 {
			t.Fatal("HEAD returned a body")
		}
	}
}
