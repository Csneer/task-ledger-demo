package transport

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Csneer/task-ledger-demo/internal/application"
	"github.com/Csneer/task-ledger-demo/internal/contract"
	"github.com/Csneer/task-ledger-demo/internal/core"
	"github.com/Csneer/task-ledger-demo/internal/jsonvalue"
	"github.com/Csneer/task-ledger-demo/internal/storage"
)

type obj = map[string]any

type fakeAPI struct {
	called string
	host   application.Host
	raw    []byte
	result obj
	err    error
}

func (f *fakeAPI) record(name string, h application.Host, b []byte) (obj, error) {
	f.called = name
	f.host = h
	f.raw = b
	return f.result, f.err
}
func (f *fakeAPI) Execute(_ context.Context, h application.Host, b []byte) (obj, error) {
	return f.record("commands", h, b)
}
func (f *fakeAPI) Action(_ context.Context, h application.Host, b []byte) (obj, error) {
	return f.record("actions", h, b)
}
func (f *fakeAPI) Query(_ context.Context, h application.Host, b []byte) (obj, error) {
	return f.record("queries", h, b)
}

func testCredential(seed byte, actor core.ActorRef) (string, Credential) {
	secret := bytes.Repeat([]byte{seed}, 32)
	digest := sha256.Sum256(secret)
	return base64.RawURLEncoding.EncodeToString(secret), Credential{Digest: hex.EncodeToString(digest[:]), Actor: actor, Read: true, Write: true}
}
func newHandler(t *testing.T, api API, credentials ...Credential) http.Handler {
	t.Helper()
	h, e := New(api, credentials)
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func request(t *testing.T, h http.Handler, path, token string, value any, headers map[string]string) (int, obj) {
	t.Helper()
	raw, e := jsonvalue.Marshal(value)
	if e != nil {
		t.Fatal(e)
	}
	r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("response is cacheable")
	}
	v, e := jsonvalue.Parse(w.Body.Bytes())
	if e != nil {
		t.Fatal(e)
	}
	o, ok := v.(map[string]any)
	if !ok {
		t.Fatal("not object")
	}
	return w.Code, o
}
func success(t *testing.T, h http.Handler, path, token string, value any, headers map[string]string) obj {
	t.Helper()
	status, out := request(t, h, path, token, value, headers)
	if status != 200 {
		t.Fatalf("HTTP %d: %v", status, out)
	}
	return out
}
func asObj(v any) obj { return v.(map[string]any) }
func assertError(t *testing.T, status int, out obj, want int, scope, code string) {
	t.Helper()
	if status != want {
		t.Fatalf("want HTTP %d got %d: %v", want, status, out)
	}
	if e := contract.ValidateBinding("ErrorResponse", out); e != nil {
		t.Fatal(e)
	}
	err := asObj(out["error"])
	got := err["code"]
	if scope == "core" {
		got = asObj(err["error"])["kind"]
	}
	if err["scope"] != scope || got != code {
		t.Fatalf("want %s:%s got %v", scope, code, err)
	}
}

func TestCredentialValidationAndRevocation(t *testing.T) {
	token, c := testCredential(1, core.ActorRef{Kind: "agent", ID: "a"})
	for _, tc := range []struct {
		name        string
		credentials []Credential
	}{
		{"duplicate", []Credential{c, c}},
		{"raw_token", []Credential{{Digest: token, Actor: c.Actor}}},
		{"invalid_actor", []Credential{{Digest: c.Digest, Actor: core.ActorRef{Kind: "runner", ID: "a"}}}},
		{"uppercase_digest", []Credential{{Digest: strings.ToUpper(c.Digest), Actor: c.Actor}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, e := New(&fakeAPI{}, tc.credentials); e == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
	if _, e := New(nil, []Credential{c}); e == nil {
		t.Fatal("nil API accepted")
	}
	f := &fakeAPI{result: obj{"ok": true}}
	h := newHandler(t, f, c)
	success(t, h, "/v1/queries", token, obj{}, nil)
	status, out := request(t, newHandler(t, f), "/v1/queries", token, obj{}, nil)
	assertError(t, status, out, 401, "binding", "UNAUTHENTICATED")
}

func TestHTTPRouteAuthenticationMediaAndSizeGuards(t *testing.T) {
	token, c := testCredential(2, core.ActorRef{Kind: "human", ID: "owner"})
	for _, tc := range []struct {
		name, method, path, auth, media string
		body                            string
		duplicate                       string
		status                          int
		code                            string
	}{
		{"unknown", "POST", "/unknown", "Bearer " + token, "application/json", "{}", "", 404, "NOT_FOUND"},
		{"method", "GET", "/v1/queries", "Bearer " + token, "application/json", "{}", "", 405, "INVALID_MESSAGE"},
		{"missing_auth", "POST", "/v1/queries", "", "application/json", "{}", "", 401, "UNAUTHENTICATED"},
		{"wrong_auth", "POST", "/v1/queries", "Bearer " + strings.Repeat("a", 43), "application/json", "{}", "", 401, "UNAUTHENTICATED"},
		{"padded_auth", "POST", "/v1/queries", "Bearer " + token + "=", "application/json", "{}", "", 401, "UNAUTHENTICATED"},
		{"duplicate_auth", "POST", "/v1/queries", "Bearer " + token, "application/json", "{}", "Authorization", 401, "UNAUTHENTICATED"},
		{"duplicate_session", "POST", "/v1/queries", "Bearer " + token, "application/json", "{}", "X-Ledger-Session", 400, "INVALID_MESSAGE"},
		{"duplicate_proof", "POST", "/v1/queries", "Bearer " + token, "application/json", "{}", "X-Ledger-Session-Proof", 400, "INVALID_MESSAGE"},
		{"duplicate_target_proof", "POST", "/v1/queries", "Bearer " + token, "application/json", "{}", "X-Ledger-Target-Session-Proof", 400, "INVALID_MESSAGE"},
		{"wrong_media", "POST", "/v1/queries", "Bearer " + token, "text/plain", "{}", "", 415, "INVALID_MESSAGE"},
		{"wrong_charset", "POST", "/v1/queries", "Bearer " + token, "application/json; charset=latin1", "{}", "", 415, "INVALID_MESSAGE"},
		{"duplicate_media", "POST", "/v1/queries", "Bearer " + token, "application/json", "{}", "Content-Type", 415, "INVALID_MESSAGE"},
		{"oversize", "POST", "/v1/queries", "Bearer " + token, "application/json", strings.Repeat("x", application.MaxRequestBytes+1), "", 413, "CAPACITY_LIMIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeAPI{}
			h := newHandler(t, f, c)
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			r.Header.Set("Content-Type", tc.media)
			if tc.auth != "" {
				r.Header.Set("Authorization", tc.auth)
			}
			if tc.duplicate != "" {
				r.Header.Add(tc.duplicate, "first")
				r.Header.Add(tc.duplicate, "second")
			}
			if tc.name == "oversize" {
				r.ContentLength = -1
			} // streaming body, not only Content-Length
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			v, e := jsonvalue.Parse(w.Body.Bytes())
			if e != nil {
				t.Fatal(e)
			}
			assertError(t, w.Code, asObj(v), tc.status, "binding", tc.code)
			if f.called != "" {
				t.Fatal("rejected request reached application")
			}
			if tc.status == 405 && w.Header().Get("Allow") != "POST" {
				t.Fatal("missing Allow")
			}
		})
	}
}

func TestRoutesPreserveRawNumbersAndTrustedHeaders(t *testing.T) {
	token, c := testCredential(3, core.ActorRef{Kind: "agent", ID: "a"})
	c.Read = false
	c.Admin = true
	for _, route := range []string{"commands", "actions", "queries"} {
		t.Run(route, func(t *testing.T) {
			f := &fakeAPI{result: obj{"value": json.Number("4.80000000000000001")}}
			h := newHandler(t, f, c)
			raw := `{"value":4.80000000000000001}`
			r := httptest.NewRequest("POST", "/v1/"+route, strings.NewReader(raw))
			r.Header.Set("Authorization", "bearer "+token)
			r.Header.Set("Content-Type", "application/json; charset=UTF-8")
			r.Header.Set("X-Ledger-Session", "session-a")
			r.Header.Set("X-Ledger-Session-Proof", "proof-a")
			r.Header.Set("X-Ledger-Target-Session-Proof", "proof-b")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 200 || f.called != route || string(f.raw) != raw || !strings.Contains(w.Body.String(), "4.80000000000000001") {
				t.Fatalf("routing/precision failed: %d %s", w.Code, w.Body.String())
			}
			if f.host.Actor != c.Actor || f.host.Read || !f.host.Write || !f.host.Admin || f.host.SessionID != "session-a" || f.host.SessionProof != "proof-a" || f.host.TargetSessionProof != "proof-b" {
				t.Fatalf("wrong host: %+v", f.host)
			}
		})
	}
}

func TestErrorMappingsMatchPinnedContractAndHideInternalDetails(t *testing.T) {
	file, e := os.ReadFile("../../contract/v1/errors.json")
	if e != nil {
		t.Fatal(e)
	}
	var mapping map[string]map[string]int
	if e = json.Unmarshal(file, &mapping); e != nil {
		t.Fatal(e)
	}
	token, c := testCredential(4, core.ActorRef{Kind: "human", ID: "owner"})
	for scope, codes := range mapping {
		for code, status := range codes {
			t.Run(scope+"/"+code, func(t *testing.T) {
				f := &fakeAPI{err: &application.Error{Scope: scope, Code: code}}
				got, out := request(t, newHandler(t, f, c), "/v1/commands", token, obj{}, nil)
				assertError(t, got, out, status, scope, code)
			})
		}
	}
	for _, e := range []error{errors.New("private SQL secret detail"), &application.Error{Scope: "weird", Code: "FORBIDDEN"}, &application.Error{Scope: "core", Code: "UNKNOWN"}} {
		got, out := request(t, newHandler(t, &fakeAPI{err: e}, c), "/v1/actions", token, obj{}, nil)
		assertError(t, got, out, 500, "binding", "INTERNAL_ERROR")
	}
	if len(mapping["binding"]) != len(bindingStatuses) || len(mapping["core"]) != len(coreStatuses) {
		t.Fatal("mapping has unpinned codes")
	}
}

func TestInvalidApplicationOutputIsUnknownResultNotCapacityRejection(t *testing.T) {
	token, c := testCredential(8, core.ActorRef{Kind: "agent", ID: "a"})
	for _, result := range []obj{
		{"unsupported": make(chan int)},
		{"too_large": strings.Repeat("x", application.MaxResponseBytes)},
	} {
		status, out := request(t, newHandler(t, &fakeAPI{result: result}, c), "/v1/commands", token, obj{}, nil)
		assertError(t, status, out, 500, "binding", "INTERNAL_ERROR")
	}
}

// Commands and actions below reach the real application and file database. The
// query interface is independently exercised by its own package and routing test.
type commandApplication struct{ *application.Service }

func (a commandApplication) Query(context.Context, application.Host, []byte) (obj, error) {
	return nil, &application.Error{Scope: "binding", Code: "UNSUPPORTED_OPERATION"}
}

func TestHTTPHumanAgentLifecycleReplayAndInvalidInput(t *testing.T) {
	db, e := storage.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	s := application.New(db, "test")
	owner := core.ActorRef{Kind: "human", ID: "owner"}
	agent := core.ActorRef{Kind: "agent", ID: "a"}
	if e = s.Initialize(context.Background(), []core.ActorRef{owner}); e != nil {
		t.Fatal(e)
	}
	ownerToken, ownerCred := testCredential(5, owner)
	ownerCred.Admin = true
	agentToken, agentCred := testCredential(6, agent)
	h := newHandler(t, commandApplication{s}, ownerCred, agentCred)
	action := func(op, id string, rev int, body obj) obj {
		return obj{"binding_version": application.BindingVersion, "request_id": id, "operation": op, "expected_revision": rev, "body": body}
	}
	command := func(op, id string, rev int, body, app obj) obj {
		return obj{"binding_version": application.BindingVersion, "command": obj{"protocol_version": core.Version, "command_id": id, "task": obj{"authority_id": "test", "task_id": "task"}, "expected_revision": rev, "operation": op, "body": body}, "application": app}
	}
	success(t, h, "/v1/actions", ownerToken, action("participant.create", "register", 0, obj{"actor": obj{"kind": "agent", "id": "a"}, "display_name": "Agent A", "state": "enabled", "profile": obj{}}), nil)
	success(t, h, "/v1/actions", ownerToken, action("project.create", "project", 0, obj{"project_id": "p", "title": "Work", "description": "Synthetic"}), nil)
	proof, proofCred := testCredential(7, agent)
	success(t, h, "/v1/actions", agentToken, action("session.begin", "begin", 0, obj{"session_id": "s", "project_id": "p", "goal": "Complete task", "proof_digest": proofCred.Digest}), nil)
	headers := map[string]string{"X-Ledger-Session": "s", "X-Ledger-Session-Proof": proof}
	create := command("task.create", "create", 0, obj{"goal": "Calculate", "expected_result": "42"}, obj{"project_id": "p", "claim_policy": obj{"mode": "target", "target": obj{"kind": "agent", "id": "a"}}})
	success(t, h, "/v1/commands", ownerToken, create, nil)
	claim := command("task.claim", "claim", 1, obj{}, obj{"session_id": "s"})
	status, out := request(t, h, "/v1/commands", agentToken, claim, nil)
	assertError(t, status, out, 403, "binding", "SESSION_MISMATCH")
	claimed := success(t, h, "/v1/commands", agentToken, claim, headers)
	task := asObj(asObj(claimed["result"])["task"])
	assignment := task["current_assignment_id"]
	generation := task["generation"]
	started := success(t, h, "/v1/commands", agentToken, command("run.start", "start", 2, obj{"assignment_id": assignment, "generation": generation, "basis_version": 1}, obj{}), headers)
	run := asObj(asObj(started["result"])["task"])["current_run_id"]
	submit := command("run.submit", "submit", 3, obj{"assignment_id": assignment, "generation": generation, "run_id": run, "basis_version": 1, "summary": "42"}, obj{})
	submitted := success(t, h, "/v1/commands", agentToken, submit, headers)
	subTask := asObj(asObj(submitted["result"])["task"])
	if subTask["state"] != "submitted" {
		t.Fatal("submission auto-accepted")
	}
	decision := command("task.decide", "decide", 4, obj{"submission_id": subTask["pending_submission_id"], "outcome": "accepted", "reason": "Matches expected result"}, obj{})
	status, out = request(t, h, "/v1/commands", agentToken, decision, headers)
	assertError(t, status, out, 403, "core", "ROLE_MISMATCH")
	accepted := success(t, h, "/v1/commands", ownerToken, decision, nil)
	if asObj(asObj(accepted["result"])["task"])["state"] != "completed" {
		t.Fatal("issuer acceptance did not complete")
	}
	agentCred.Write = false
	h = newHandler(t, commandApplication{s}, ownerCred, agentCred)
	replay := success(t, h, "/v1/commands", agentToken, submit, nil)
	if asObj(replay["result"])["replayed"] != true {
		t.Fatal("no-proof read-only replay failed")
	}
	for _, raw := range []string{`{"binding_version":"ledger-http-draft-1","binding_version":"ledger-http-draft-1"}`, `{"x":"\ud800"}`, `{"x":1e309}`, `{"host":{"actor":{"kind":"human","id":"owner"}}}`} {
		r := httptest.NewRequest("POST", "/v1/commands", strings.NewReader(raw))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+agentToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid input accepted: %d", w.Code)
		}
	}
}
