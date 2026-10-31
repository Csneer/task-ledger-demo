package application

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Csneer/task-ledger-demo/internal/core"
	"github.com/Csneer/task-ledger-demo/internal/jsonvalue"
	"github.com/Csneer/task-ledger-demo/internal/storage"
)

var owner = core.ActorRef{Kind: "human", ID: "owner"}
var agentA = core.ActorRef{Kind: "agent", ID: "agent-a"}
var agentB = core.ActorRef{Kind: "agent", ID: "agent-b"}
var task1 = core.TaskRef{AuthorityID: "personal", TaskID: "task-1"}

func host(a core.ActorRef) Host {
	return Host{Actor: a, Read: true, Write: true, Admin: a == owner}
}
func bytesOf(t *testing.T, v any) []byte {
	t.Helper()
	b, e := jsonvalue.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func actionRequest(op, id string, rev int64, body Object) Object {
	return Object{"binding_version": BindingVersion, "request_id": id, "operation": op, "expected_revision": rev, "body": body}
}
func commandRequest(op, id string, rev int64, body, app Object) Object {
	return Object{"binding_version": BindingVersion, "command": Object{"protocol_version": core.Version, "command_id": id, "task": taskObject(task1), "expected_revision": rev, "operation": op, "body": body}, "application": app}
}
func testService(t *testing.T) (*Service, *storage.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, e := storage.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := New(db, "personal")
	if e = s.Initialize(context.Background(), []core.ActorRef{owner}); e != nil {
		t.Fatal(e)
	}
	for _, a := range []core.ActorRef{agentA, agentB} {
		mustAction(t, s, host(owner), actionRequest("participant.create", "register-"+a.ID, 0, Object{"actor": actorObject(a), "display_name": a.ID, "state": "enabled", "profile": Object{}}))
	}
	mustAction(t, s, host(owner), actionRequest("project.create", "project", 0, Object{"project_id": "p", "title": "Work", "description": "Synthetic test"}))
	return s, db, path
}
func mustAction(t *testing.T, s *Service, h Host, v Object) Object {
	t.Helper()
	r, e := s.Action(context.Background(), h, bytesOf(t, v))
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func mustCommand(t *testing.T, s *Service, h Host, v Object) Object {
	t.Helper()
	r, e := s.Execute(context.Background(), h, bytesOf(t, v))
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func sessionHost(t *testing.T, s *Service, a core.ActorRef, id string, seed byte) Host {
	t.Helper()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = seed
	}
	d := sha256.Sum256(secret)
	h := host(a)
	mustAction(t, s, h, actionRequest("session.begin", "begin-"+id, 0, Object{"session_id": id, "project_id": "p", "goal": "Implement", "proof_digest": hex.EncodeToString(d[:])}))
	h.SessionID = id
	h.SessionProof = base64.RawURLEncoding.EncodeToString(secret)
	return h
}
func createRequest(id string) Object {
	return commandRequest("task.create", id, 0, Object{"goal": "Implement", "expected_result": "Working result"}, Object{"project_id": "p", "claim_policy": Object{"mode": "workspace"}})
}
func create(t *testing.T, s *Service) Object {
	return mustCommand(t, s, host(owner), createRequest("create"))
}
func resultTask(r Object) Object { return object(object(r["result"])["task"]) }
func execution(r Object) Object {
	task := resultTask(r)
	return Object{"assignment_id": task["current_assignment_id"], "generation": task["generation"]}
}
func current(t *testing.T, s *Service) (Object, int64) {
	t.Helper()
	v, w, e := s.Snapshot(context.Background(), host(owner), task1)
	if e != nil {
		t.Fatal(e)
	}
	return v, w
}
func wantCode(t *testing.T, e error, scope, code string) {
	t.Helper()
	var f *Error
	if !errors.As(e, &f) || f.Scope != scope || f.Code != code {
		t.Fatalf("want %s:%s got %v", scope, code, e)
	}
}
func cloneObject(t *testing.T, o Object) Object {
	t.Helper()
	v, e := jsonvalue.Clone(o)
	if e != nil {
		t.Fatal(e)
	}
	return object(v)
}

func TestAdminActionsRequireCredentialCapabilityAndOperatorIdentity(t *testing.T) {
	for _, tc := range []struct {
		name string
		host Host
	}{
		{"operator_without_admin", Host{Actor: owner, Read: true, Write: true}},
		{"operator_without_write", Host{Actor: owner, Read: true, Admin: true}},
		{"unregistered_operator", Host{Actor: agentA, Read: true, Write: true, Admin: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := testService(t)
			req := actionRequest("participant.create", "new-participant", 0, Object{"actor": actorObject(core.ActorRef{Kind: "human", ID: "reviewer"}), "display_name": "Reviewer", "state": "enabled", "profile": Object{}})
			eventsBefore, e := s.Events(context.Background(), host(owner), 0, 100)
			if e != nil {
				t.Fatal(e)
			}
			_, e = s.Action(context.Background(), tc.host, bytesOf(t, req))
			wantCode(t, e, "binding", "FORBIDDEN")
			eventsAfter, e := s.Events(context.Background(), host(owner), 0, 100)
			if e != nil || len(eventsBefore) != len(eventsAfter) {
				t.Fatalf("rejected admin action wrote facts: %v, %d -> %d", e, len(eventsBefore), len(eventsAfter))
			}
			created := mustAction(t, s, host(owner), req)
			if created["replayed"] != false {
				t.Fatal("rejected action retained a receipt")
			}
			// Historical receipt access depends on current read capability,
			// not a renewed grant to execute the original administrative action.
			replayed := mustAction(t, s, Host{Actor: owner, Read: true}, req)
			if replayed["replayed"] != true {
				t.Fatal("read-only receipt replay did not succeed")
			}
		})
	}
	t.Run("project_owned_by_another_actor", func(t *testing.T) {
		s, _, _ := testService(t)
		mustAction(t, s, host(agentA), actionRequest("project.create", "agent-project", 0, Object{"project_id": "agent-project", "title": "Agent", "description": "Synthetic project"}))
		req := actionRequest("project.update", "project-override", 1, Object{"project_id": "agent-project", "title": "Updated", "description": "Synthetic project"})
		_, e := s.Action(context.Background(), Host{Actor: owner, Read: true, Write: true}, bytesOf(t, req))
		wantCode(t, e, "binding", "FORBIDDEN")
		updated := mustAction(t, s, host(owner), req)
		if updated["replayed"] != false || !equal(object(updated["result"])["created_by"], actorObject(agentA)) {
			t.Fatal("admin project update lost original creator or retained rejected receipt")
		}
	})
}

func TestDurableHumanAgentLifecycleAndReplay(t *testing.T) {
	s, db, path := testService(t)
	h := sessionHost(t, s, agentA, "a", 1)
	create(t, s)
	claimed := mustCommand(t, s, h, commandRequest("task.claim", "claim", 1, Object{}, Object{"session_id": "a"}))
	start := execution(claimed)
	start["basis_version"] = int64(1)
	active := mustCommand(t, s, h, commandRequest("run.start", "start", 2, start, Object{}))
	b := execution(active)
	b["run_id"] = resultTask(active)["current_run_id"]
	b["message"] = "Validated output"
	mustCommand(t, s, h, commandRequest("run.report", "report", 3, b, Object{}))
	sub := execution(active)
	sub["run_id"] = resultTask(active)["current_run_id"]
	sub["basis_version"] = int64(1)
	sub["summary"] = "Artifact ready"
	request := commandRequest("run.submit", "submit", 4, sub, Object{})
	submitted := mustCommand(t, s, h, request)
	if resultTask(submitted)["state"] != "submitted" {
		t.Fatal("submit completed task")
	}
	snap, _ := current(t, s)
	if object(snap["application"])["execution_binding"] != nil {
		t.Fatal("submission kept current binding")
	}
	sid := resultTask(submitted)["pending_submission_id"]
	mustCommand(t, s, host(owner), commandRequest("task.decide", "decide", 5, Object{"submission_id": sid, "outcome": "accepted", "reason": "Result meets expectations"}, Object{}))
	if e := db.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := storage.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	s = New(reopened, "personal")
	h.Write = false
	h.SessionProof = ""
	replay := mustCommand(t, s, h, request)
	if object(replay["result"])["replayed"] != true || resultTask(replay)["state"] != "submitted" {
		t.Fatal("replay did not preserve historical response")
	}
	now, _ := current(t, s)
	if object(object(now["snapshot"])["task"])["state"] != "completed" {
		t.Fatal("replay rewound current state")
	}
	rows, e := s.Events(context.Background(), host(owner), 0, 200)
	if e != nil {
		t.Fatal(e)
	}
	counts := map[string]int{}
	for _, row := range rows {
		event, e := parseObject(row.Body)
		if e != nil {
			t.Fatal(e)
		}
		if event["source"] == "core" {
			counts[str(object(event["event"])["command_id"])]++
		}
	}
	if len(counts) != 6 {
		t.Fatalf("core commits=%v", counts)
	}
	for k, n := range counts {
		if n != 1 {
			t.Fatalf("duplicate core commit %s", k)
		}
	}
}

func TestSameCommandConcurrentReplayAcrossIndependentStores(t *testing.T) {
	s, _, path := testService(t)
	second, e := storage.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer second.Close()
	s2 := New(second, "personal")
	req := bytesOf(t, createRequest("same-create"))
	ready := make(chan struct{})
	errCh := make(chan error, 12)
	fresh := 0
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-ready
			service := s
			if i%2 == 1 {
				service = s2
			}
			r, e := service.Execute(context.Background(), host(owner), req)
			if e != nil {
				errCh <- e
				return
			}
			if object(r["result"])["replayed"] == false {
				mu.Lock()
				fresh++
				mu.Unlock()
			}
		}(i)
	}
	close(ready)
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Error(e)
	}
	if fresh != 1 {
		t.Fatalf("fresh commits %d", fresh)
	}
	snap, _ := current(t, s)
	if number(object(object(snap["snapshot"])["task"])["revision"]) != 1 {
		t.Fatal("duplicate revised task")
	}
}

func TestConcurrentClaimIsOneWinnerAcrossIndependentStores(t *testing.T) {
	s, _, path := testService(t)
	h1 := sessionHost(t, s, agentA, "s-a", 1)
	h2 := sessionHost(t, s, agentB, "s-b", 2)
	create(t, s)
	db2, e := storage.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer db2.Close()
	other := New(db2, "personal")
	start := make(chan struct{})
	results := make(chan error, 2)
	for i, h := range []Host{h1, h2} {
		go func(i int, h Host) {
			<-start
			svc := s
			if i == 1 {
				svc = other
			}
			_, e := svc.Execute(context.Background(), h, bytesOf(t, commandRequest("task.claim", fmt.Sprint("claim", i), 1, Object{}, Object{"session_id": h.SessionID})))
			results <- e
		}(i, h)
	}
	close(start)
	wins := 0
	for i := 0; i < 2; i++ {
		e := <-results
		if e == nil {
			wins++
		} else {
			wantCode(t, e, "core", "STALE_REVISION")
		}
	}
	if wins != 1 {
		t.Fatalf("winners %d", wins)
	}
	snap, _ := current(t, s)
	records := object(snap["snapshot"])["records"].([]any)
	if len(records) != 1 {
		t.Fatalf("record count %d", len(records))
	}
}

func TestApplicationGuardRollbackLeavesNoFactsOrReceipt(t *testing.T) {
	s, _, _ := testService(t)
	h := sessionHost(t, s, agentA, "a", 1)
	create(t, s)
	before, w := current(t, s)
	req := commandRequest("task.claim", "claim-retry", 1, Object{}, Object{"session_id": "a"})
	bad := h
	bad.SessionProof = "invalid"
	_, e := s.Execute(context.Background(), bad, bytesOf(t, req))
	wantCode(t, e, "binding", "SESSION_PROOF_REQUIRED")
	after, w2 := current(t, s)
	if !equal(before, after) || w != w2 {
		t.Fatal("rejected host guard persisted candidate state")
	}
	r := mustCommand(t, s, h, req)
	if object(r["result"])["replayed"] != false {
		t.Fatal("failure occupied receipt")
	}
}

func TestSessionRotationTransferAndEndedPresence(t *testing.T) {
	s, _, _ := testService(t)
	h1 := sessionHost(t, s, agentA, "one", 1)
	h2 := sessionHost(t, s, agentA, "two", 2)
	create(t, s)
	claimed := mustCommand(t, s, h1, commandRequest("task.claim", "claim", 1, Object{}, Object{"session_id": "one"}))
	start := execution(claimed)
	start["basis_version"] = int64(1)
	active := mustCommand(t, s, h1, commandRequest("run.start", "start", 2, start, Object{}))
	before, _ := current(t, s)
	mustAction(t, s, h1, actionRequest("session.end", "end", 1, Object{"session_id": "one", "outcome": "done", "summary": "Session ended, task pending"}))
	after, _ := current(t, s)
	if !equal(before, after) {
		t.Fatal("session end changed Task")
	}
	b := execution(active)
	b["run_id"] = resultTask(active)["current_run_id"]
	b["message"] = "Progress"
	_, e := s.Execute(context.Background(), h1, bytesOf(t, commandRequest("run.report", "report", 3, b, Object{})))
	wantCode(t, e, "binding", "SESSION_ENDED")
	transfer := execution(active)
	transfer["task"] = taskObject(task1)
	transfer["target_session_id"] = "two"
	transfer["reason"] = "Continue work"
	h1.TargetSessionProof = h2.SessionProof
	mustAction(t, s, h1, actionRequest("session.transfer", "transfer", 2, transfer))
	_, e = s.Execute(context.Background(), h1, bytesOf(t, commandRequest("run.report", "late", 3, b, Object{})))
	wantCode(t, e, "binding", "SESSION_MISMATCH")
	mustCommand(t, s, h2, commandRequest("run.report", "report", 3, b, Object{}))
	old := h2
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = 3
	}
	digest := sha256.Sum256(secret)
	rot := actionRequest("session.rotate", "rotate", 1, Object{"session_id": "two", "proof_digest": hex.EncodeToString(digest[:])})
	mustAction(t, s, h2, rot)
	replay := mustAction(t, s, old, rot)
	if replay["replayed"] != true {
		t.Fatal("old proof prevented historical replay")
	}
	_, e = s.Execute(context.Background(), old, bytesOf(t, commandRequest("run.report", "old-proof", 4, b, Object{})))
	wantCode(t, e, "binding", "SESSION_PROOF_REQUIRED")
	h2.SessionProof = base64.RawURLEncoding.EncodeToString(secret)
	mustCommand(t, s, h2, commandRequest("run.report", "new-proof", 4, b, Object{}))
	view, e := s.Session(context.Background(), host(owner), "two")
	if e != nil {
		t.Fatal(e)
	}
	raw := string(bytesOf(t, view))
	if strings.Contains(raw, "digest") || strings.Contains(raw, h2.SessionProof) {
		t.Fatal("session leaked proof")
	}
}

func TestCoreSemanticsReviseRejectRetryAndStaleAttempt(t *testing.T) {
	s, _, _ := testService(t)
	h := sessionHost(t, s, agentA, "a", 1)
	create(t, s)
	claimed := mustCommand(t, s, h, commandRequest("task.claim", "claim", 1, Object{}, Object{"session_id": "a"}))
	b := execution(claimed)
	b["basis_version"] = int64(1)
	active := mustCommand(t, s, h, commandRequest("run.start", "start", 2, b, Object{}))
	mustCommand(t, s, host(owner), commandRequest("task.revise", "revise", 3, Object{"goal": "Implement", "expected_result": "Changed expectation", "reason": "Clarify"}, Object{}))
	sub := execution(active)
	sub["run_id"] = resultTask(active)["current_run_id"]
	sub["basis_version"] = int64(1)
	sub["summary"] = "Original-basis result"
	submitted := mustCommand(t, s, h, commandRequest("run.submit", "submit", 4, sub, Object{}))
	mustCommand(t, s, host(owner), commandRequest("task.decide", "reject", 5, Object{"submission_id": resultTask(submitted)["pending_submission_id"], "outcome": "rejected", "reason": "Need changed expectation"}, Object{}))
	mustCommand(t, s, host(owner), commandRequest("task.reopen", "reopen", 6, Object{"reason": "Try again"}, Object{}))
	mustCommand(t, s, h, commandRequest("task.claim", "claim2", 7, Object{}, Object{"session_id": "a"}))
	_, e := s.Execute(context.Background(), h, bytesOf(t, commandRequest("run.submit", "late", 8, sub, Object{})))
	wantCode(t, e, "core", "STALE_ASSIGNMENT")
	snap, _ := current(t, s)
	state := object(snap["snapshot"])
	task := object(state["task"])
	if number(task["generation"]) != 2 || number(task["expectation_version"]) != 2 {
		t.Fatal("lost versions")
	}
	for _, item := range state["records"].([]any) {
		r := object(item)
		if r["record_type"] == "Run" && object(r["record"])["state"] != "submitted" {
			t.Fatal("decision rewrote old Run")
		}
	}
}

func TestResponseCapacityRejectionRollsBackAllRows(t *testing.T) {
	if testing.Short() {
		t.Skip("large history capacity scenario")
	}
	s, _, _ := testService(t)
	create(t, s)
	rev := int64(1)
	payload := strings.Repeat("x", 800000)
	rejected := false
	for i := 0; i < 14; i++ {
		_, beforeW := current(t, s)
		req := commandRequest("task.revise", fmt.Sprint("large", i), rev, Object{"goal": "Implement", "expected_result": payload, "reason": "Retain version"}, Object{})
		_, e := s.Execute(context.Background(), host(owner), bytesOf(t, req))
		if e != nil {
			wantCode(t, e, "binding", "CAPACITY_LIMIT")
			after, w := current(t, s)
			if w != beforeW || number(object(object(after["snapshot"])["task"])["revision"]) != rev {
				t.Fatal("capacity rejection committed partial facts")
			}
			rejected = true
			break
		}
		rev++
	}
	if !rejected {
		t.Fatal("did not reach response capacity guard")
	}
}

func TestStrictCommandInputAndNumberEquivalentReplay(t *testing.T) {
	s, _, _ := testService(t)
	req := createRequest("numeric")
	object(object(req["command"])["body"])["extensions"] = Object{"org.example.test": Object{"rating": json.Number("4.80000000000000001")}}
	raw := bytesOf(t, req)
	mustCommand(t, s, host(owner), req)
	equivalent := strings.Replace(string(raw), "\"expected_revision\":0", "\"expected_revision\":0.0", 1)
	r, e := s.Execute(context.Background(), host(owner), []byte(equivalent))
	if e != nil {
		t.Fatal(e)
	}
	if object(r["result"])["replayed"] != true {
		t.Fatal("numeric equivalence not replayed")
	}
	changed := strings.Replace(string(raw), "4.80000000000000001", "4.8", 1)
	_, e = s.Execute(context.Background(), host(owner), []byte(changed))
	wantCode(t, e, "core", "IDEMPOTENCY_CONFLICT")
	for _, bad := range []string{`{"binding_version":"ledger-http-draft-1","binding_version":"ledger-http-draft-1"}`, `{"x":"\ud800"}`, `{"x":1e309}`} {
		_, e = s.Execute(context.Background(), host(owner), []byte(bad))
		wantCode(t, e, "binding", "INVALID_MESSAGE")
	}
}
