package transport

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Csneer/task-ledger-demo/internal/application"
	"github.com/Csneer/task-ledger-demo/internal/core"
)

func TestIdentityUsesCredentialAndRechecksRead(t *testing.T) {
	token, c := testCredential(42, core.ActorRef{Kind: "human", ID: "owner"})
	c.Admin = true
	api := &fakeAPI{}
	h := newHandler(t, api, c)
	r := httptest.NewRequest("GET", "/v1/identity", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"owner"`) || strings.Contains(w.Body.String(), token) {
		t.Fatalf("bad identity: %s", w.Body.String())
	}
	if api.called != "queries" || api.host.Actor.ID != "owner" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("read guard missing")
	}
	api.err = &application.Error{Scope: "binding", Code: "FORBIDDEN"}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("lost read still exposed identity")
	}
	r.Header.Del("Authorization")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("unauthenticated identity")
	}
}
