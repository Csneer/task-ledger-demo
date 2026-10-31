package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	human   = ActorRef{Kind: "human", ID: "owner"}
	agent   = ActorRef{Kind: "agent", ID: "worker"}
	other   = ActorRef{Kind: "agent", ID: "other"}
	taskRef = TaskRef{AuthorityID: "local", TaskID: "task"}
)

type journey struct {
	t     *testing.T
	state *Snapshot
	n     int
}

func (j *journey) command(op string, body Object) Command {
	j.n++
	var revision int64
	if j.state != nil {
		revision, _ = integer(j.state.Task["revision"])
	}
	return Command{ProtocolVersion: Version, CommandID: fmt.Sprintf("command-%d", j.n), Task: taskRef, ExpectedRevision: revision, Operation: op, Body: body}
}
func (j *journey) apply(actor ActorRef, op string, body Object) Result {
	j.t.Helper()
	cmd := j.command(op, body)
	next, result, err := Transition(j.state, cmd, actor, fmt.Sprintf("event-%d", j.n))
	if err != nil {
		j.t.Fatalf("%s: %v", op, err)
	}
	j.state = &next
	return result
}
func newJourney(t *testing.T) *journey {
	j := &journey{t: t}
	j.apply(human, "task.create", Object{"goal": "Review", "expected_result": "Review notes"})
	return j
}
func (j *journey) attempt() Object {
	j.t.Helper()
	return Object{"assignment_id": j.state.Task["current_assignment_id"], "generation": j.state.Task["generation"]}
}
func (j *journey) running() Object {
	body := j.attempt()
	body["run_id"] = j.state.Task["current_run_id"]
	return body
}
func (j *journey) start(actor ActorRef) {
	j.apply(actor, "task.claim", Object{})
	body := j.attempt()
	body["basis_version"] = j.state.Task["expectation_version"]
	j.apply(actor, "run.start", body)
}
func (j *journey) submit(actor ActorRef) Result {
	body := j.running()
	run, _ := record(*j.state, "Run", text(body["run_id"]))
	body["basis_version"], body["summary"] = run["basis_version"], "Actual review"
	return j.apply(actor, "run.submit", body)
}
func assertKind(t *testing.T, err error, kind string) {
	t.Helper()
	var actual *Error
	if !errors.As(err, &actual) || actual.Kind != kind {
		t.Fatalf("got %v, want %s", err, kind)
	}
}
func (j *journey) rejects(actor ActorRef, op string, body Object, kind string) {
	j.t.Helper()
	before, _ := copySnapshot(*j.state)
	_, _, err := Transition(j.state, j.command(op, body), actor, "rejected-event")
	assertKind(j.t, err, kind)
	if !reflect.DeepEqual(before, *j.state) {
		j.t.Fatal("rejected operation changed input")
	}
}
func stateIs(t *testing.T, s Snapshot, want string) {
	t.Helper()
	if s.Task["state"] != want {
		t.Fatalf("state %v, want %s", s.Task["state"], want)
	}
}

func TestSubmissionRequiresIssuerDecision(t *testing.T) {
	j := newJourney(t)
	j.start(agent)
	report := j.running()
	report["message"] = "Progress only"
	j.apply(agent, "run.report", report)
	stateIs(t, *j.state, "active")
	result := j.submit(agent)
	stateIs(t, *j.state, "submitted")
	if j.state.Task["current_assignment_id"] != nil || j.state.Task["current_run_id"] != nil {
		t.Fatal("submission kept execution pointers")
	}
	if len(result.Records) != 3 || result.Records[0].RecordType != "Assignment" || result.Records[1].RecordType != "Run" || result.Records[2].RecordType != "Submission" {
		t.Fatal("wrong submission records/order")
	}
	body := Object{"submission_id": j.state.Task["pending_submission_id"], "outcome": "accepted", "reason": "Reviewed actual result"}
	j.rejects(agent, "task.decide", body, "ROLE_MISMATCH")
	decision := j.apply(human, "task.decide", body)
	stateIs(t, *j.state, "completed")
	if len(decision.Records) != 1 || decision.Records[0].RecordType != "Decision" {
		t.Fatal("decision rewrote execution facts")
	}
	run, _ := record(*j.state, "Run", text(result.Records[1].Record["run_id"]))
	if run["state"] != "submitted" {
		t.Fatal("acceptance rewrote submitted Run")
	}
	j.rejects(human, "task.revise", Object{"goal": "Later", "expected_result": "Changed", "reason": "Update"}, "INVALID_STATE")
	j.rejects(human, "task.cancel", Object{"reason": "Cancel"}, "INVALID_STATE")
}

func TestHumanExecutionAndAgentIssuer(t *testing.T) {
	j := &journey{t: t}
	j.apply(agent, "task.create", Object{"goal": "Human review", "expected_result": "Opinion"})
	j.start(human)
	j.submit(human)
	body := Object{"submission_id": j.state.Task["pending_submission_id"], "outcome": "accepted", "reason": "Useful opinion"}
	j.rejects(human, "task.decide", body, "ROLE_MISMATCH")
	j.apply(agent, "task.decide", body)
	stateIs(t, *j.state, "completed")
}

func TestRevisionsPreserveOriginalExecutionBasis(t *testing.T) {
	j := newJourney(t)
	j.start(agent)
	j.apply(human, "task.revise", Object{"goal": "Review", "expected_result": "More detail", "reason": "Clarify"})
	body := j.running()
	body["basis_version"], body["summary"] = int64(2), "Cannot silently rebase"
	j.rejects(agent, "run.submit", body, "INVALID_BASIS")
	j.submit(agent)
	j.apply(human, "task.revise", Object{"goal": "Review", "expected_result": "Final preference", "reason": "After delivery"})
	result := j.apply(human, "task.decide", Object{"submission_id": j.state.Task["pending_submission_id"], "outcome": "accepted", "reason": "Old-basis delivery is sufficient"})
	d := result.Records[0].Record
	if d["basis_version"] != int64(1) || d["evaluated_version"] != int64(3) {
		t.Fatalf("wrong decision basis: %#v", d)
	}
}

func TestRevisionReplacesOptionalExpectationWithoutChangingHistory(t *testing.T) {
	j := &journey{t: t}
	j.apply(human, "task.create", Object{"goal": "Review", "expected_result": "Notes", "constraints": []any{"Read only"}})
	j.apply(human, "task.revise", Object{"goal": "Review", "expected_result": "Notes", "reason": "No extra constraint"})
	history := j.state.Task["expectations"].([]any)
	if _, ok := history[0].(Object)["constraints"]; !ok {
		t.Fatal("old constraint erased")
	}
	if _, ok := history[1].(Object)["constraints"]; ok {
		t.Fatal("omitted new constraint silently copied")
	}
	j.rejects(agent, "task.revise", Object{"goal": "Other", "expected_result": "Other", "reason": "Change"}, "ROLE_MISMATCH")
}

func TestNewRunMustUseCurrentBasis(t *testing.T) {
	j := newJourney(t)
	j.apply(agent, "task.claim", Object{})
	j.apply(human, "task.revise", Object{"goal": "Review", "expected_result": "New", "reason": "Clarify"})
	body := j.attempt()
	body["basis_version"] = int64(1)
	j.rejects(agent, "run.start", body, "INVALID_BASIS")
	body["basis_version"] = int64(2)
	j.apply(agent, "run.start", body)
}

func TestReleaseAndLateExecution(t *testing.T) {
	j := newJourney(t)
	j.start(agent)
	old := j.running()
	release := j.attempt()
	release["reason"] = "Hand over"
	j.rejects(other, "assignment.release", release, "ROLE_MISMATCH")
	j.apply(human, "assignment.release", release)
	stateIs(t, *j.state, "offered")
	j.start(other)
	old["basis_version"], old["summary"] = int64(1), "Late result"
	j.rejects(agent, "run.submit", old, "STALE_ASSIGNMENT")
	j.rejects(other, "assignment.release", release, "STALE_ASSIGNMENT")
	generation, _ := integer(j.state.Task["generation"])
	if generation != 2 {
		t.Fatal("generation not advanced")
	}
	current := j.attempt()
	current["reason"] = "Worker leaves"
	j.apply(other, "assignment.release", current)
}

func TestReleaseBeforeStartCreatesNoRun(t *testing.T) {
	j := newJourney(t)
	j.apply(agent, "task.claim", Object{})
	body := j.attempt()
	body["reason"] = "Not starting"
	result := j.apply(agent, "assignment.release", body)
	if len(j.state.Records) != 1 || len(result.Records) != 1 {
		t.Fatal("release manufactured Run")
	}
}

func TestFailureAndRejectionAreDistinctAndReopenPreservesFacts(t *testing.T) {
	for _, mode := range []string{"failure", "rejection"} {
		t.Run(mode, func(t *testing.T) {
			j := newJourney(t)
			j.start(agent)
			var oldSubmission any
			if mode == "failure" {
				body := j.running()
				body["reason"] = "Cannot execute"
				j.apply(agent, "run.fail", body)
				stateIs(t, *j.state, "failed")
			} else {
				j.submit(agent)
				oldSubmission = j.state.Task["pending_submission_id"]
				j.apply(human, "task.decide", Object{"submission_id": oldSubmission, "outcome": "rejected", "reason": "Incomplete"})
				stateIs(t, *j.state, "rejected")
			}
			before, _ := copyRecords(j.state.Records)
			result := j.apply(human, "task.reopen", Object{"reason": "Try again"})
			stateIs(t, *j.state, "offered")
			if result.Event["reason"] != "Try again" || !reflect.DeepEqual(before, j.state.Records) {
				t.Fatal("reopen lost reason or rewrote facts")
			}
			j.start(other)
			j.submit(other)
			if oldSubmission != nil {
				j.rejects(human, "task.decide", Object{"submission_id": oldSubmission, "outcome": "accepted", "reason": "Old"}, "INVALID_STATE")
			}
		})
	}
}

func TestCancellationKeepsSubmittedFactsAndEndsActiveFacts(t *testing.T) {
	for _, phase := range []string{"offered", "claimed", "active", "submitted"} {
		t.Run(phase, func(t *testing.T) {
			j := newJourney(t)
			if phase == "claimed" {
				j.apply(agent, "task.claim", Object{})
			}
			if phase == "active" || phase == "submitted" {
				j.start(agent)
			}
			if phase == "submitted" {
				j.submit(agent)
			}
			before, _ := copyRecords(j.state.Records)
			r := j.apply(human, "task.cancel", Object{"reason": "Stop request"})
			stateIs(t, *j.state, "cancelled")
			if r.Event["reason"] != "Stop request" || j.state.Task["pending_submission_id"] != nil {
				t.Fatal("cancellation lost reason/pending cleanup")
			}
			if phase == "submitted" && !reflect.DeepEqual(before, j.state.Records) {
				t.Fatal("cancel rewrote delivered facts")
			}
			if phase == "claimed" || phase == "active" {
				for _, record := range r.Records {
					if record.Record["state"] != "cancelled" {
						t.Fatal("active record not cancelled")
					}
				}
			}
			j.rejects(human, "task.reopen", Object{"reason": "Try"}, "INVALID_STATE")
		})
	}
}

func TestClaimExclusionAndGuardPriority(t *testing.T) {
	j := newJourney(t)
	stale := j.command("task.claim", Object{})
	j.apply(agent, "task.claim", Object{})
	_, _, err := Transition(j.state, stale, other, "stale")
	assertKind(t, err, "STALE_REVISION")
	j.rejects(other, "task.claim", Object{}, "INVALID_STATE")
	j.rejects(other, "run.start", Object{"assignment_id": "old", "generation": int64(99), "basis_version": int64(1)}, "STALE_ASSIGNMENT")
	body := j.attempt()
	body["basis_version"] = int64(1)
	j.rejects(other, "run.start", body, "ROLE_MISMATCH")
	j.rejects(other, "task.decide", Object{"submission_id": "none", "outcome": "accepted", "reason": "Wrong role and state"}, "ROLE_MISMATCH")
}

func TestActorKindAndTaskAuthorityAreDistinct(t *testing.T) {
	j := newJourney(t)
	j.rejects(ActorRef{Kind: "agent", ID: human.ID}, "task.cancel", Object{"reason": "Same text ID"}, "ROLE_MISMATCH")
	cmd := j.command("task.cancel", Object{"reason": "Other authority"})
	cmd.Task.AuthorityID = "other"
	_, _, err := Transition(j.state, cmd, human, "event")
	assertKind(t, err, "TASK_NOT_FOUND")
}

func TestRelationsDoNotAdvanceParent(t *testing.T) {
	parent := newJourney(t)
	before, _ := copySnapshot(*parent.state)
	child := &journey{t: t}
	cmd := Command{ProtocolVersion: Version, CommandID: "child-create", Task: TaskRef{AuthorityID: "local", TaskID: "child"}, Operation: "task.create", Body: Object{"goal": "Child", "expected_result": "Result", "relations": []any{Object{"kind": "parent", "target": taskObject(taskRef)}}}}
	s, _, err := Transition(nil, cmd, agent, "event-child")
	if err != nil {
		t.Fatal(err)
	}
	child.state = &s
	// A child transition cannot mutate a separately held parent or confer its issuer role.
	cmd.Task = TaskRef{AuthorityID: "local", TaskID: "child"}
	cmd.ExpectedRevision = 1
	cmd.Operation = "task.cancel"
	cmd.Body = Object{"reason": "Child no longer needed"}
	_, _, err = Transition(child.state, cmd, agent, "event-child-cancel")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, *parent.state) {
		t.Fatal("child changed parent")
	}
}

func TestOutputAndInputMutationIsolation(t *testing.T) {
	j := newJourney(t)
	before, _ := copySnapshot(*j.state)
	cmd := j.command("task.revise", Object{"goal": "New", "expected_result": "Notes", "reason": "Change", "extensions": Object{"org.example": Object{"items": []any{"original"}}}})
	state, result, err := Transition(j.state, cmd, human, "event-change")
	if err != nil {
		t.Fatal(err)
	}
	cmd.Body["extensions"].(Object)["org.example"].(Object)["items"].([]any)[0] = "input mutation"
	state.Task["issuer"].(Object)["id"] = "state mutation"
	result.Task["state"] = "result mutation"
	result.Event["task_snapshot"].(Object)["state"] = "event mutation"
	if !reflect.DeepEqual(before, *j.state) {
		t.Fatal("transition mutated prior")
	}
	if result.Task["issuer"].(Object)["id"] != human.ID {
		t.Fatal("state aliases result")
	}
	if state.Task["state"] != "offered" {
		t.Fatal("result aliases state")
	}
	for _, task := range []Object{state.Task, result.Task, result.Event["task_snapshot"].(Object)} {
		history := task["expectations"].([]any)
		ext := history[1].(Object)["extensions"].(Object)
		if ext["org.example"].(Object)["items"].([]any)[0] != "original" {
			t.Fatal("input aliases output")
		}
	}
	j.start(agent)
	body := j.running()
	body["basis_version"], body["summary"] = int64(1), "Notes"
	s, r, err := Transition(j.state, j.command("run.submit", body), agent, "submit")
	if err != nil {
		t.Fatal(err)
	}
	r.Records[0].Record["state"] = "mutated"
	if s.Records[0].Record["state"] != "completed" {
		t.Fatal("changed records alias snapshot")
	}
	eventRecord := r.Event["records"].([]any)[0].(Object)["record"].(Object)
	if eventRecord["state"] != "completed" {
		t.Fatal("changed records alias event")
	}
}

func TestCounterBounds(t *testing.T) {
	for _, counter := range []string{"revision", "generation", "expectation_version"} {
		t.Run(counter, func(t *testing.T) {
			j := newJourney(t)
			j.state.Task[counter] = MaxInteger
			op, body := "task.claim", Object{}
			if counter == "expectation_version" {
				op = "task.revise"
				body = Object{"goal": "New", "expected_result": "New", "reason": "New"}
			}
			j.rejects(human, op, body, "INVALID_STATE")
		})
	}
}

func TestDecodeExactIntegerSpellings(t *testing.T) {
	for _, value := range []string{"0", "-0", "-0.000e9999999999999", "1", "1.0", "1e0", "10e-1", "9007199254740991", "90071992547409910e-1"} {
		input := Object{"protocol_version": Version, "command_id": "id", "task": taskObject(taskRef), "expected_revision": json.Number(value), "operation": "task.claim", "body": Object{}}
		if _, err := DecodeCommand(input); err != nil {
			t.Errorf("valid %s: %v", value, err)
		}
	}
	for _, value := range []string{"-1", "1.1", "9007199254740992", "1e999999999999", "1e-999999999999", "true", "1/2", "\"1\""} {
		if _, ok := integer(json.Number(value)); ok {
			t.Errorf("invalid integer accepted: %s", value)
		}
	}
}

func TestMalformedDirectInputReturnsErrorWithoutPanic(t *testing.T) {
	for _, value := range []any{nil, Object{}, Object{"task": Object{}, "body": Object{}, "expected_revision": true}} {
		if _, err := DecodeCommand(value); err == nil {
			t.Errorf("accepted malformed %#v", value)
		}
	}
	j := newJourney(t)
	cmd := j.command("run.start", Object{})
	_, _, err := Transition(j.state, cmd, agent, "event")
	assertKind(t, err, "STALE_ASSIGNMENT")
	cmd.Operation = "unknown"
	_, _, err = Transition(j.state, cmd, agent, "event")
	assertKind(t, err, "UNSUPPORTED_OPERATION")
	cmd.ProtocolVersion = "other"
	_, _, err = Transition(j.state, cmd, agent, "event")
	assertKind(t, err, "UNSUPPORTED_VERSION")
	cmd.ProtocolVersion = Version
	cmd.Operation = "task.create"
	_, _, err = Transition(j.state, cmd, agent, "event")
	assertKind(t, err, "TASK_EXISTS")
	_, _, err = Transition(nil, j.command("task.claim", Object{}), agent, "event")
	assertKind(t, err, "TASK_NOT_FOUND")
	cycle := Object{}
	cycle["cycle"] = cycle
	cmd.Body = cycle
	_, _, err = Transition(nil, cmd, agent, "event")
	assertKind(t, err, "INVALID_MESSAGE")
}

func TestSuccessfulOutputsMatchPinnedSchema(t *testing.T) {
	data, err := os.ReadFile("../../protocol/upstream/schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("urn:agent-task-bridge:task-core-draft-1", schema); err != nil {
		t.Fatal(err)
	}
	snapshotSchema, err := compiler.Compile("urn:agent-task-bridge:task-core-draft-1#/$defs/Snapshot")
	if err != nil {
		t.Fatal(err)
	}
	resultSchema, err := compiler.Compile("urn:agent-task-bridge:task-core-draft-1#/$defs/Result")
	if err != nil {
		t.Fatal(err)
	}
	j := newJourney(t)
	validate := func(result Result) {
		t.Helper()
		for _, entry := range []struct {
			value  any
			schema *jsonschema.Schema
		}{{j.state, snapshotSchema}, {result, resultSchema}} {
			encoded, err := json.Marshal(entry.value)
			if err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(bytes.NewReader(encoded))
			decoder.UseNumber()
			var decoded any
			if err := decoder.Decode(&decoded); err != nil {
				t.Fatal(err)
			}
			if err := entry.schema.Validate(decoded); err != nil {
				t.Fatalf("invalid output: %v", err)
			}
		}
	}
	validate(j.apply(agent, "task.claim", Object{}))
	body := j.attempt()
	body["basis_version"] = json.Number("1.0")
	validate(j.apply(agent, "run.start", body))
	body = j.running()
	body["message"] = "Progress"
	body["extensions"] = Object{"org.example": Object{"score": json.Number("4.8000000000000000001")}}
	validate(j.apply(agent, "run.report", body))
	validate(j.submit(agent))
	validate(j.apply(human, "task.decide", Object{"submission_id": j.state.Task["pending_submission_id"], "outcome": "rejected", "reason": "More detail"}))
	validate(j.apply(human, "task.reopen", Object{"reason": "Retry"}))
	validate(j.apply(human, "task.cancel", Object{"reason": "Stop"}))
	if !strings.Contains(string(data), Version) {
		t.Fatal("wrong schema")
	}
}
