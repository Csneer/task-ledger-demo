package application

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Csneer/task-ledger-demo/internal/core"
	"github.com/Csneer/task-ledger-demo/internal/storage"
)

func queryRequest(op string, body Object) Object {
	return Object{"binding_version": BindingVersion, "operation": op, "body": body}
}
func mustQuery(t *testing.T, s *Service, h Host, op string, body Object) Object {
	t.Helper()
	r, e := s.Query(context.Background(), h, bytesOf(t, queryRequest(op, body)))
	if e != nil {
		t.Fatal(e)
	}
	return object(r["result"])
}
func queryError(t *testing.T, s *Service, h Host, op string, body Object, scope, code string) {
	t.Helper()
	_, e := s.Query(context.Background(), h, bytesOf(t, queryRequest(op, body)))
	wantCode(t, e, scope, code)
}
func TestQuerySnapshotAndSessionViewsDoNotPublishSecrets(t *testing.T) {
	s, _, _ := testService(t)
	h := sessionHost(t, s, agentA, "session", 42)
	create(t, s)
	claim := mustCommand(t, s, h, commandRequest("task.claim", "claim", 1, Object{}, Object{"session_id": "session"}))
	ws := mustQuery(t, s, host(owner), "workspace.snapshot", Object{})
	if len(ws["tasks"].([]any)) != 1 || len(ws["participants"].([]any)) != 3 || len(ws["sessions"].([]any)) != 1 {
		t.Fatal("incomplete workspace")
	}
	sess := mustQuery(t, s, host(owner), "session.get", Object{"session_id": "session"})
	if !equal(object(sess["current_binding"])["assignment_id"], resultTask(claim)["current_assignment_id"]) {
		t.Fatal("missing session binding")
	}
	snapshot := mustQuery(t, s, h, "task.snapshot", Object{"task": taskObject(task1)})
	if !equal(object(object(snapshot["bundle"])["snapshot"])["task"], resultTask(claim)) {
		t.Fatal("snapshot differs")
	}
	for _, v := range []Object{ws, sess, snapshot} {
		raw := string(bytesOf(t, v))
		if strings.Contains(raw, "proof_digest") || strings.Contains(raw, "digest") || strings.Contains(raw, h.SessionProof) {
			t.Fatal("private proof leaked")
		}
	}
	info := mustQuery(t, s, host(owner), "service.info", Object{})
	if info["authority_id"] != "personal" || len(info["core_operations"].([]string)) != 11 {
		t.Fatal("incorrect service info")
	}
	profiles := mustQuery(t, s, h, "profiles.list", Object{})
	if len(profiles["profiles"].([]any)) != 0 {
		t.Fatal("invented profile")
	}
	queryError(t, s, h, "wake.get", Object{"wake_id": "absent"}, "binding", "UNSUPPORTED_OPERATION")
}
func TestQueryFixedBatchFollowAndReopen(t *testing.T) {
	s, db, path := testService(t)
	create(t, s)
	opened := mustQuery(t, s, host(owner), "events.open", Object{})
	cursor := str(opened["cursor"])
	first := mustQuery(t, s, host(owner), "events.read", Object{"cursor": cursor, "limit": 1})
	if first["done"] != false || first["next_cursor"] == nil || first["follow_cursor"] != nil {
		t.Fatal("first page state")
	}
	// A later write cannot extend an already opened fixed batch.
	mustAction(t, s, host(owner), actionRequest("project.create", "late-project", 0, Object{"project_id": "late", "title": "Late", "description": "After batch"}))
	again := mustQuery(t, s, host(owner), "events.read", Object{"cursor": cursor, "limit": 1})
	if !equal(first, again) {
		t.Fatal("fixed cursor changed")
	}
	seen := map[string]bool{}
	page := first
	for {
		for _, item := range page["events"].([]any) {
			event := object(object(item)["event"])
			eid := str(event["event_id"])
			if seen[eid] {
				t.Fatal("repeated event")
			}
			seen[eid] = true
			if str(event["entity_id"]) == "late" {
				t.Fatal("late write entered fixed batch")
			}
		}
		if page["done"] == true {
			break
		}
		page = mustQuery(t, s, host(owner), "events.read", Object{"cursor": page["next_cursor"], "limit": 2})
	}
	follow := str(page["follow_cursor"])
	if e := db.Close(); e != nil {
		t.Fatal(e)
	}
	reopened, e := storage.Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	s = New(reopened, "personal")
	next := mustQuery(t, s, Host{Actor: owner, Read: true}, "events.read", Object{"cursor": follow, "limit": 200})
	events := next["events"].([]any)
	if len(events) != 1 || object(object(events[0])["event"])["entity_id"] != "late" || next["done"] != true {
		t.Fatal("follow did not resume durable batch")
	}
	empty := mustQuery(t, s, host(owner), "events.read", Object{"cursor": next["follow_cursor"], "limit": 1})
	if len(empty["events"].([]any)) != 0 || empty["done"] != true {
		t.Fatal("empty batch not advanced")
	}
}
func TestQueryCursorIntegrityIdentityAndScope(t *testing.T) {
	s, db, _ := testService(t)
	create(t, s)
	workspace := mustQuery(t, s, host(owner), "workspace.snapshot", Object{})
	snapshot := mustQuery(t, s, host(owner), "task.snapshot", Object{"task": taskObject(task1)})
	token := str(workspace["event_cursor"])
	raw := []byte(token)
	if raw[20] == 'A' {
		raw[20] = 'B'
	} else {
		raw[20] = 'A'
	}
	queryError(t, s, host(owner), "events.read", Object{"cursor": string(raw), "limit": 1}, "binding", "INVALID_CURSOR")
	queryError(t, s, host(agentA), "events.read", Object{"cursor": token, "limit": 1}, "binding", "FORBIDDEN")
	queryError(t, s, Host{Actor: owner, Write: true, Admin: true}, "events.read", Object{"cursor": token, "limit": 1}, "binding", "FORBIDDEN")
	queryError(t, s, host(owner), "events.open", Object{"after": snapshot["watermark"]}, "binding", "INVALID_CURSOR")
	queryError(t, s, host(owner), "events.open", Object{"after": workspace["watermark"], "task": taskObject(task1)}, "binding", "INVALID_CURSOR")
	queryError(t, s, host(owner), "events.open", Object{"after": token}, "binding", "INVALID_CURSOR")
	queryError(t, s, host(owner), "events.read", Object{"cursor": workspace["watermark"], "limit": 1}, "binding", "INVALID_CURSOR")
	// Watermarks may be shared by currently authorized actors in the same scope.
	mustQuery(t, s, host(agentA), "events.open", Object{"after": workspace["watermark"]})
	// A database-history reset changes incarnation while retaining verification
	// key, so old signed tokens have a distinct reset response rather than replay.
	if e := db.Update(context.Background(), func(tx *storage.Tx) error {
		b, e := tx.Get("settings", querySettings)
		if e != nil {
			return e
		}
		b[47] ^= 1
		return tx.Put("settings", querySettings, b)
	}); e != nil {
		t.Fatal(e)
	}
	queryError(t, s, host(owner), "events.read", Object{"cursor": token, "limit": 1}, "binding", "CURSOR_RESET_REQUIRED")
}
func TestQueryTaskFilteringAndSnapshotContinuation(t *testing.T) {
	s, _, _ := testService(t)
	create(t, s)
	before := mustQuery(t, s, host(owner), "task.snapshot", Object{"task": taskObject(task1)})
	mustAction(t, s, host(owner), actionRequest("project.update", "unrelated", 1, Object{"project_id": "p", "title": "Updated", "description": "Unrelated fact"}))
	mustCommand(t, s, host(owner), commandRequest("task.revise", "revise", 1, Object{"goal": "Updated", "expected_result": "Result", "reason": "Revision"}, Object{}))
	opened := mustQuery(t, s, host(owner), "events.open", Object{"after": before["watermark"], "task": taskObject(task1)})
	read := mustQuery(t, s, host(owner), "events.read", Object{"cursor": opened["cursor"], "limit": 1})
	events := read["events"].([]any)
	if len(events) != 1 || object(object(events[0])["event"])["operation"] != "task.revise" || read["done"] != true {
		t.Fatal("task filtering failed")
	}
	// The snapshot follow cursor has the same task filter and complete boundary.
	follow := mustQuery(t, s, host(owner), "events.read", Object{"cursor": before["event_cursor"], "limit": 200})
	if !equal(follow["events"], read["events"]) {
		t.Fatal("snapshot cursor missed or included wrong facts")
	}
	// A task stream includes its application policy event as well as core facts.
	all := mustQuery(t, s, host(owner), "events.open", Object{"task": taskObject(task1)})
	page := mustQuery(t, s, host(owner), "events.read", Object{"cursor": all["cursor"], "limit": 200})
	if len(page["events"].([]any)) != 3 {
		t.Fatal("task stream did not include initial policy and core events")
	}
}
func TestQueryLongIDsRemainWithinTokenContract(t *testing.T) {
	s, _, _ := testService(t)
	longActor := core.ActorRef{Kind: "agent", ID: strings.Repeat("a", 256)}
	mustAction(t, s, host(owner), actionRequest("participant.create", "long-register", 0, Object{"actor": actorObject(longActor), "display_name": "Long", "state": "enabled", "profile": Object{}}))
	req := createRequest("long-create")
	ref := core.TaskRef{AuthorityID: "personal", TaskID: strings.Repeat("t", 256)}
	object(req["command"])["task"] = taskObject(ref)
	mustCommand(t, s, host(owner), req)
	view := mustQuery(t, s, host(longActor), "task.snapshot", Object{"task": taskObject(ref)})
	if len(str(view["event_cursor"])) > 256 || len(str(view["watermark"])) > 256 {
		t.Fatal("token exceeds schema")
	}
	mustQuery(t, s, host(longActor), "events.read", Object{"cursor": view["event_cursor"], "limit": 1})
}
func TestQueryValidationAndNoReadSideEffect(t *testing.T) {
	s, _, _ := testService(t)
	events, e := s.Events(context.Background(), host(owner), 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	for _, op := range []string{"service.info", "workspace.snapshot", "profiles.list"} {
		mustQuery(t, s, host(owner), op, Object{})
	}
	after, e := s.Events(context.Background(), host(owner), 0, 100)
	if e != nil || len(events) != len(after) {
		t.Fatal("queries appended events")
	}
	queryError(t, s, host(owner), "task.snapshot", Object{"task": Object{"authority_id": "other", "task_id": "absent"}}, "binding", "AUTHORITY_MISMATCH")
	queryError(t, s, host(owner), "task.snapshot", Object{"task": taskObject(task1)}, "core", "TASK_NOT_FOUND")
	queryError(t, s, host(owner), "session.get", Object{"session_id": "absent"}, "binding", "NOT_FOUND")
	queryError(t, s, host(owner), "service.info", Object{"unexpected": true}, "binding", "INVALID_MESSAGE")
	queryError(t, s, host(owner), "events.read", Object{"cursor": "invalid", "limit": 0}, "binding", "INVALID_MESSAGE")
	_, e = s.Query(context.Background(), host(owner), []byte(`{"binding_version":"ledger-http-draft-1","binding_version":"ledger-http-draft-1"}`))
	wantCode(t, e, "binding", "INVALID_MESSAGE")
	_, e = s.Query(context.Background(), host(owner), []byte(strings.Repeat(" ", MaxRequestBytes+1)))
	wantCode(t, e, "binding", "CAPACITY_LIMIT")
}
func TestQuerySnapshotCollectionCapacity(t *testing.T) {
	s, db, _ := testService(t)
	// Synthetic stored fixtures isolate the read boundary; command behavior has
	// separate tests. Do not spend 1001 writes on unrelated directory semantics.
	if e := db.Update(context.Background(), func(tx *storage.Tx) error {
		for i := 0; i < 1000; i++ {
			if e := put(tx, "projects", fmt.Sprint("extra-", i), Object{"project_id": fmt.Sprint("extra-", i), "title": "Test", "description": "Capacity", "created_by": actorObject(owner), "revision": 1}); e != nil {
				return e
			}
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	queryError(t, s, host(owner), "workspace.snapshot", Object{}, "binding", "CAPACITY_LIMIT")
	// Narrow reads remain available even if full workspace is above its bound.
	mustQuery(t, s, host(owner), "service.info", Object{})
}

func TestQueryWorkspaceResponseCapacity(t *testing.T) {
	s, _, _ := testService(t)
	before := mustQuery(t, s, host(owner), "workspace.snapshot", Object{})
	// Legal sub-1-MiB commands build a task and event history above the public
	// response budget without allocating a huge synthetic database in the test.
	for i := 0; i < 19; i++ {
		pid := fmt.Sprint("large-", i)
		req := createRequest(pid)
		object(req["command"])["task"] = taskObject(core.TaskRef{AuthorityID: "personal", TaskID: pid})
		object(object(req["command"])["body"])["expected_result"] = strings.Repeat("x", 900000)
		mustCommand(t, s, host(owner), req)
	}
	queryError(t, s, host(owner), "workspace.snapshot", Object{}, "binding", "CAPACITY_LIMIT")
	opened := mustQuery(t, s, host(owner), "events.open", Object{"after": before["watermark"]})
	// A one-event page must remain available even when the following BLOBs
	// exceed the response bound together; do not prefetch them before checking.
	first := mustQuery(t, s, host(owner), "events.read", Object{"cursor": opened["cursor"], "limit": 1})
	if len(first["events"].([]any)) != 1 || first["done"] != false {
		t.Fatal("bounded first page was not preserved")
	}
	queryError(t, s, host(owner), "events.read", Object{"cursor": opened["cursor"], "limit": 200}, "binding", "CAPACITY_LIMIT")
	// The rejected large page must not consume the smaller successful page.
	repeated := mustQuery(t, s, host(owner), "events.read", Object{"cursor": opened["cursor"], "limit": 1})
	if !equal(first, repeated) {
		t.Fatal("capacity failure changed the fixed batch")
	}
}
