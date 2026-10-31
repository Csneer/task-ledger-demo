package application_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	app "github.com/Csneer/task-ledger-demo/internal/application"
	"github.com/Csneer/task-ledger-demo/internal/core"
	"github.com/Csneer/task-ledger-demo/internal/storage"
)

type adversary struct {
	t           *testing.T
	s           *app.Service
	owner, a, b app.Host
}
type obj = map[string]any

func adversarialSetup(t *testing.T) *adversary {
	t.Helper()
	db, e := storage.Open(filepath.Join(t.TempDir(), "ledger.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := db.Close(); e != nil {
			t.Error(e)
		}
	})
	f := &adversary{t: t, s: app.New(db, "local"), owner: app.Host{Actor: core.ActorRef{Kind: "human", ID: "owner"}, Read: true, Write: true, Admin: true}}
	if e = f.s.Initialize(context.Background(), []core.ActorRef{f.owner.Actor}); e != nil {
		t.Fatal(e)
	}
	f.action(f.owner, "project", "project.create", 0, obj{"project_id": "p", "title": "Project", "description": "Test project"})
	for _, id := range []string{"a", "b"} {
		f.action(f.owner, "register-"+id, "participant.create", 0, obj{"actor": core.ActorRef{Kind: "agent", ID: id}, "display_name": id, "state": "enabled", "profile": obj{}})
	}
	f.a = f.begin("a", "sa")
	f.b = f.begin("b", "sb")
	return f
}
func bytesOf(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func actionBody(id, op string, revision int, body obj) obj {
	return obj{"binding_version": app.BindingVersion, "request_id": id, "operation": op, "expected_revision": revision, "body": body}
}
func commandBody(id, task, op string, revision int, body, application obj) obj {
	return obj{"binding_version": app.BindingVersion, "command": obj{"protocol_version": core.Version, "command_id": id, "task": core.TaskRef{AuthorityID: "local", TaskID: task}, "expected_revision": revision, "operation": op, "body": body}, "application": application}
}
func (f *adversary) action(h app.Host, id, op string, revision int, body obj) obj {
	f.t.Helper()
	v, e := f.s.Action(context.Background(), h, bytesOf(f.t, actionBody(id, op, revision, body)))
	if e != nil {
		f.t.Fatalf("%s: %v", op, e)
	}
	return v
}
func (f *adversary) execute(h app.Host, v obj) obj {
	f.t.Helper()
	r, e := f.s.Execute(context.Background(), h, bytesOf(f.t, v))
	if e != nil {
		f.t.Fatal(e)
	}
	return r
}
func (f *adversary) begin(actor, sid string) app.Host {
	f.t.Helper()
	h := app.Host{Actor: core.ActorRef{Kind: "agent", ID: actor}, Read: true, Write: true}
	token := sha256.Sum256([]byte(sid))
	digest := sha256.Sum256(token[:])
	f.action(h, "begin-"+sid, "session.begin", 0, obj{"session_id": sid, "project_id": "p", "goal": "Work", "proof_digest": hex.EncodeToString(digest[:])})
	h.SessionID = sid
	h.SessionProof = base64.RawURLEncoding.EncodeToString(token[:])
	return h
}
func (f *adversary) create(task string, policy obj) obj {
	v := commandBody("create-"+task, task, "task.create", 0, obj{"goal": "Work", "expected_result": "Result"}, obj{"project_id": "p", "claim_policy": policy})
	f.execute(f.owner, v)
	return v
}
func (f *adversary) count() int {
	f.t.Helper()
	v, e := f.s.Events(context.Background(), f.owner, 0, 1000)
	if e != nil {
		f.t.Fatal(e)
	}
	return len(v)
}
func wantError(t *testing.T, e error, scope, code string) {
	t.Helper()
	var got *app.Error
	if !errors.As(e, &got) || got.Scope != scope || got.Code != code {
		t.Fatalf("want %s:%s; got %v", scope, code, e)
	}
}
func TestAdversarialSessionProofAndErrorOrder(t *testing.T) {
	f := adversarialSetup(t)
	f.create("t", obj{"mode": "workspace"})
	before := f.count()
	claim := commandBody("claim", "t", "task.claim", 1, obj{}, obj{"session_id": "sa"})
	bad := f.a
	bad.SessionProof = f.b.SessionProof
	_, e := f.s.Execute(context.Background(), bad, bytesOf(t, claim))
	wantError(t, e, "binding", "SESSION_PROOF_REQUIRED")
	bad = f.a
	bad.SessionID = "sb"
	bad.SessionProof = f.b.SessionProof
	_, e = f.s.Execute(context.Background(), bad, bytesOf(t, claim))
	wantError(t, e, "binding", "SESSION_MISMATCH")
	if f.count() != before {
		t.Fatal("rejected claims emitted events")
	}
	f.execute(f.a, claim)
	start := commandBody("start", "t", "run.start", 2, obj{"assignment_id": "assignment-2", "generation": 1, "basis_version": 1}, obj{})
	other := f.begin("a", "sa-other")
	_, e = f.s.Execute(context.Background(), other, bytesOf(t, start))
	wantError(t, e, "binding", "SESSION_MISMATCH")
	bad = f.a
	bad.SessionProof = "invalid"
	_, e = f.s.Execute(context.Background(), bad, bytesOf(t, start))
	wantError(t, e, "binding", "SESSION_PROOF_REQUIRED")
	start["command"].(obj)["expected_revision"] = 1
	_, e = f.s.Execute(context.Background(), bad, bytesOf(t, start))
	wantError(t, e, "core", "STALE_REVISION")
	start["command"].(obj)["expected_revision"] = 2
	f.execute(f.a, start)
	// A historical replay needs read access, not a current session credential.
	replayHost := f.a
	replayHost.SessionProof = ""
	replayHost.Write = false
	result := f.execute(replayHost, start)
	if result["result"].(obj)["replayed"] != true {
		t.Fatal("missing replay flag")
	}
}
func TestAdversarialReadonlyRevocationAndReceipts(t *testing.T) {
	f := adversarialSetup(t)
	request := commandBody("agent-create", "t", "task.create", 0, obj{"goal": "Work", "expected_result": "Result"}, obj{"project_id": "p", "claim_policy": obj{"mode": "workspace"}})
	f.execute(f.a, request)
	begin := actionBody("begin-sa", "session.begin", 0, obj{"session_id": "sa", "project_id": "p", "goal": "Work"})
	// Replay the exact saved begin payload, including its proof digest.
	token := sha256.Sum256([]byte("sa"))
	digest := sha256.Sum256(token[:])
	begin["body"].(obj)["proof_digest"] = hex.EncodeToString(digest[:])
	f.action(f.owner, "disable-a", "participant.update", 1, obj{"actor": f.a.Actor, "display_name": "a", "state": "read_only", "profile": obj{}})
	before := f.count()
	f.execute(f.a, request)
	r, e := f.s.Action(context.Background(), f.a, bytesOf(t, begin))
	if e != nil || r["replayed"] != true {
		t.Fatalf("read-only action replay: %v %v", r, e)
	}
	fresh := commandBody("fresh", "t", "task.cancel", 1, obj{"reason": "Cancel"}, obj{})
	_, e = f.s.Execute(context.Background(), f.a, bytesOf(t, fresh))
	wantError(t, e, "binding", "FORBIDDEN")
	if f.count() != before {
		t.Fatal("replay or denial appended events")
	}
	noRead := f.a
	noRead.Read = false
	_, e = f.s.Execute(context.Background(), noRead, bytesOf(t, request))
	wantError(t, e, "binding", "FORBIDDEN")
}
func TestAdversarialCoreAndApplicationIdempotencyScopes(t *testing.T) {
	f := adversarialSetup(t)
	request := f.create("t", obj{"mode": "workspace"})
	before := f.count()
	request["command"].(obj)["body"].(obj)["goal"] = "Changed"
	_, e := f.s.Execute(context.Background(), f.owner, bytesOf(t, request))
	wantError(t, e, "core", "IDEMPOTENCY_CONFLICT")
	request["command"].(obj)["body"].(obj)["goal"] = "Work"
	request["application"].(obj)["project_id"] = "other"
	_, e = f.s.Execute(context.Background(), f.owner, bytesOf(t, request))
	wantError(t, e, "binding", "IDEMPOTENCY_CONFLICT")
	_, e = f.s.Action(context.Background(), f.owner, bytesOf(t, actionBody("project", "project.create", 0, obj{"project_id": "p", "title": "Changed", "description": "Test project"})))
	wantError(t, e, "binding", "IDEMPOTENCY_CONFLICT")
	if f.count() != before {
		t.Fatal("conflicting receipts appended events")
	}
}
func TestAdversarialTargetPolicyAndConcurrentClaims(t *testing.T) {
	f := adversarialSetup(t)
	f.create("target", obj{"mode": "target", "target": f.a.Actor})
	_, e := f.s.Execute(context.Background(), f.b, bytesOf(t, commandBody("b-claim", "target", "task.claim", 1, obj{}, obj{"session_id": "sb"})))
	wantError(t, e, "binding", "FORBIDDEN")
	// A policy change and an otherwise valid claim must serialize at the same task revision.
	policy := bytesOf(t, actionBody("retarget", "claim_policy.update", 1, obj{"task": core.TaskRef{AuthorityID: "local", TaskID: "target"}, "expected_task_revision": 1, "claim_policy": obj{"mode": "target", "target": f.b.Actor}}))
	claim := bytesOf(t, commandBody("a-claim", "target", "task.claim", 1, obj{}, obj{"session_id": "sa"}))
	var pe, ce error
	var wg sync.WaitGroup
	gate := make(chan struct{})
	wg.Add(2)
	go func() { defer wg.Done(); <-gate; _, pe = f.s.Action(context.Background(), f.owner, policy) }()
	go func() { defer wg.Done(); <-gate; _, ce = f.s.Execute(context.Background(), f.a, claim) }()
	close(gate)
	wg.Wait()
	if pe == nil && ce == nil {
		t.Fatal("policy change and old-target claim both committed")
	}
	if pe != nil && ce != nil {
		t.Fatalf("neither committed: policy=%v claim=%v", pe, ce)
	}
	if pe == nil {
		wantError(t, ce, "binding", "FORBIDDEN")
	} else {
		wantError(t, pe, "binding", "STALE_REVISION")
	}
	f.create("race", obj{"mode": "workspace"})
	h1 := f.begin("a", "race-a")
	h2 := f.begin("b", "race-b")
	errorsOut := make(chan error, 2)
	gate = make(chan struct{})
	wg.Add(2)
	for i, h := range []app.Host{h1, h2} {
		raw := bytesOf(t, commandBody([]string{"race-1", "race-2"}[i], "race", "task.claim", 1, obj{}, obj{"session_id": h.SessionID}))
		go func(h app.Host, raw []byte) {
			defer wg.Done()
			<-gate
			_, e := f.s.Execute(context.Background(), h, raw)
			errorsOut <- e
		}(h, raw)
	}
	close(gate)
	wg.Wait()
	close(errorsOut)
	success := 0
	for e := range errorsOut {
		if e == nil {
			success++
		} else {
			wantError(t, e, "core", "STALE_REVISION")
		}
	}
	if success != 1 {
		t.Fatalf("successful claims = %d", success)
	}
}
func TestAdversarialUnavailableWakeRollsBackCreate(t *testing.T) {
	f := adversarialSetup(t)
	before := f.count()
	request := commandBody("wake-create", "wake", "task.create", 0, obj{"goal": "Work", "expected_result": "Result"}, obj{"project_id": "p", "claim_policy": obj{"mode": "target", "target": f.a.Actor}, "wake": obj{"profile_id": "missing", "profile_revision": 1}})
	_, e := f.s.Execute(context.Background(), f.owner, bytesOf(t, request))
	wantError(t, e, "binding", "PROFILE_UNAVAILABLE")
	_, _, e = f.s.Snapshot(context.Background(), f.owner, core.TaskRef{AuthorityID: "local", TaskID: "wake"})
	wantError(t, e, "core", "TASK_NOT_FOUND")
	if f.count() != before {
		t.Fatal("failed wake left events")
	}
	delete(request["application"].(obj), "wake")
	f.execute(f.owner, request)
}

func TestAdversarialRotatedProofAndTransferCannotStealAssignment(t *testing.T) {
	f := adversarialSetup(t)
	f.create("t", obj{"mode": "workspace"})
	f.execute(f.a, commandBody("claim", "t", "task.claim", 1, obj{}, obj{"session_id": "sa"}))
	token := sha256.Sum256([]byte("rotated-token"))
	digest := sha256.Sum256(token[:])
	rotate := actionBody("rotate", "session.rotate", 1, obj{"session_id": "sa", "proof_digest": hex.EncodeToString(digest[:])})
	if _, e := f.s.Action(context.Background(), f.a, bytesOf(t, rotate)); e != nil {
		t.Fatal(e)
	}
	start := commandBody("start", "t", "run.start", 2, obj{"assignment_id": "assignment-2", "generation": 1, "basis_version": 1}, obj{})
	_, e := f.s.Execute(context.Background(), f.a, bytesOf(t, start))
	wantError(t, e, "binding", "SESSION_PROOF_REQUIRED")
	// Receipt replay after rotation cannot change current proof or state.
	before := f.count()
	r, e := f.s.Action(context.Background(), f.a, bytesOf(t, rotate))
	if e != nil || r["replayed"] != true {
		t.Fatalf("rotation replay: %v %v", r, e)
	}
	if f.count() != before {
		t.Fatal("rotation replay emitted an event")
	}
	f.a.SessionProof = base64.RawURLEncoding.EncodeToString(token[:])
	transfer := actionBody("transfer", "session.transfer", 1, obj{"task": core.TaskRef{AuthorityID: "local", TaskID: "t"}, "assignment_id": "assignment-2", "generation": 1, "target_session_id": "sb", "reason": "Resume execution"})
	h := f.a
	h.TargetSessionProof = f.b.SessionProof
	_, e = f.s.Action(context.Background(), h, bytesOf(t, transfer))
	wantError(t, e, "binding", "SESSION_MISMATCH")
	replacement := f.begin("a", "replacement")
	transfer["body"].(obj)["target_session_id"] = replacement.SessionID
	h.TargetSessionProof = replacement.SessionProof
	if _, e = f.s.Action(context.Background(), h, bytesOf(t, transfer)); e != nil {
		t.Fatal(e)
	}
	_, e = f.s.Execute(context.Background(), f.a, bytesOf(t, start))
	wantError(t, e, "binding", "SESSION_MISMATCH")
	f.execute(replacement, start)
	view, e := f.s.Session(context.Background(), f.owner, "sa")
	if e != nil {
		t.Fatal(e)
	}
	if view["current_binding"] != nil {
		t.Fatal("transfer left source occupied")
	}
}
