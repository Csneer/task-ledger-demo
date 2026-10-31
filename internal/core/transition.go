package core

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// DecodeCommand converts a schema-validated JSON object without rounding its
// revision. Schema validation and strict raw JSON parsing belong to the caller.
func DecodeCommand(value any) (Command, error) {
	v, ok := value.(map[string]any)
	if !ok {
		return Command{}, reject("INVALID_MESSAGE")
	}
	task, ok := v["task"].(map[string]any)
	if !ok {
		return Command{}, reject("INVALID_MESSAGE")
	}
	body, ok := v["body"].(map[string]any)
	if !ok {
		return Command{}, reject("INVALID_MESSAGE")
	}
	revision, ok := integer(v["expected_revision"])
	if !ok {
		return Command{}, reject("INVALID_MESSAGE")
	}
	copied, err := copyObject(body)
	if err != nil {
		return Command{}, reject("INVALID_MESSAGE")
	}
	cmd := Command{ProtocolVersion: text(v["protocol_version"]), CommandID: text(v["command_id"]),
		Task:             TaskRef{AuthorityID: text(task["authority_id"]), TaskID: text(task["task_id"])},
		ExpectedRevision: revision, Operation: text(v["operation"]), Body: copied}
	if cmd.ProtocolVersion == "" || cmd.CommandID == "" || cmd.Task.AuthorityID == "" || cmd.Task.TaskID == "" || cmd.Operation == "" {
		return Command{}, reject("INVALID_MESSAGE")
	}
	return cmd, nil
}

// Transition computes one candidate commit. The caller owns schema validation,
// current host admission, historical receipts, event ID uniqueness and atomic
// persistence. Rejected operations never modify prior or cmd.Body.
func Transition(prior *Snapshot, cmd Command, actor ActorRef, eventID string) (Snapshot, Result, error) {
	fail := func(kind string) (Snapshot, Result, error) { return Snapshot{}, Result{}, reject(kind) }
	if cmd.ProtocolVersion != Version {
		return fail("UNSUPPORTED_VERSION")
	}
	if !operationKnown(cmd.Operation) {
		return fail("UNSUPPORTED_OPERATION")
	}
	if cmd.CommandID == "" || cmd.Task.AuthorityID == "" || cmd.Task.TaskID == "" || eventID == "" ||
		(actor.Kind != "human" && actor.Kind != "agent") || actor.ID == "" || cmd.Body == nil ||
		cmd.ExpectedRevision < 0 || cmd.ExpectedRevision > MaxInteger {
		return fail("INVALID_MESSAGE")
	}
	body, err := copyObject(cmd.Body)
	if err != nil {
		return fail("INVALID_MESSAGE")
	}
	ref, who := taskObject(cmd.Task), actorObject(actor)
	var state Snapshot
	if cmd.Operation == "task.create" {
		if prior != nil {
			return fail("TASK_EXISTS")
		}
		if cmd.ExpectedRevision != 0 {
			return fail("STALE_REVISION")
		}
		if text(body["goal"]) == "" || text(body["expected_result"]) == "" {
			return fail("INVALID_MESSAGE")
		}
		relations := body["relations"]
		if relations == nil {
			relations = []any{}
		}
		state = Snapshot{Task: Object{"authority_id": cmd.Task.AuthorityID, "task_id": cmd.Task.TaskID,
			"issuer": who, "state": "offered", "revision": int64(0), "expectation_version": int64(1),
			"expectations": []any{expectation(body, 1, who, "created")}, "generation": int64(0),
			"current_assignment_id": nil, "current_run_id": nil, "pending_submission_id": nil,
			"relations": relations}, Records: []Record{}}
	} else {
		if prior == nil {
			return fail("TASK_NOT_FOUND")
		}
		state, err = copySnapshot(*prior)
		if err != nil {
			return fail("INVALID_STATE")
		}
		if text(state.Task["authority_id"]) != cmd.Task.AuthorityID || text(state.Task["task_id"]) != cmd.Task.TaskID {
			return fail("TASK_NOT_FOUND")
		}
		revision, valid := integer(state.Task["revision"])
		if !valid || revision < 1 {
			return fail("INVALID_STATE")
		}
		if cmd.ExpectedRevision != revision {
			return fail("STALE_REVISION")
		}
	}
	task := state.Task
	revision, ok := integer(task["revision"])
	if !ok || revision >= MaxInteger {
		return fail("INVALID_STATE")
	}
	next := revision + 1
	id := func(kind string) string { return fmt.Sprintf("%s-%d", kind, next) }
	changed := []Record{}
	put := func(kind string, value Object) error { return putRecord(&state, &changed, kind, value) }
	issuer := func() bool { return actorEqual(task["issuer"], actor) }
	clear := func() {
		task["current_assignment_id"], task["current_run_id"], task["pending_submission_id"] = nil, nil, nil
	}
	switch cmd.Operation {
	case "task.create":
	case "task.revise":
		if !issuer() {
			return fail("ROLE_MISMATCH")
		}
		version, valid := integer(task["expectation_version"])
		history, historyOK := task["expectations"].([]any)
		if terminal(text(task["state"])) || !valid || version < 1 || version >= MaxInteger || !historyOK {
			return fail("INVALID_STATE")
		}
		if text(body["goal"]) == "" || text(body["expected_result"]) == "" || text(body["reason"]) == "" {
			return fail("INVALID_MESSAGE")
		}
		task["expectation_version"] = version + 1
		task["expectations"] = append(history, expectation(body, version+1, who, body["reason"]))
	case "task.claim":
		if text(task["state"]) != "offered" || task["current_assignment_id"] != nil {
			return fail("INVALID_STATE")
		}
		generation, valid := integer(task["generation"])
		if !valid || generation >= MaxInteger {
			return fail("INVALID_STATE")
		}
		task["generation"], task["state"], task["current_assignment_id"] = generation+1, "claimed", id("assignment")
		err = put("Assignment", Object{"task": ref, "assignment_id": id("assignment"), "assignee": who,
			"generation": generation + 1, "state": "claimed"})
	case "run.start":
		assignment, e := currentAssignment(state, body, actor, false)
		if e != nil {
			return Snapshot{}, Result{}, e
		}
		if text(task["state"]) != "claimed" {
			return fail("INVALID_STATE")
		}
		basis, valid := integer(body["basis_version"])
		version, versionOK := integer(task["expectation_version"])
		if !valid || !versionOK || basis != version {
			return fail("INVALID_BASIS")
		}
		assignment["state"] = "active"
		if err = put("Assignment", assignment); err == nil {
			err = put("Run", Object{"task": ref, "run_id": id("run"), "assignment_id": assignment["assignment_id"],
				"assignee": who, "basis_version": basis, "state": "running"})
		}
		task["state"], task["current_run_id"] = "active", id("run")
	case "run.report", "run.submit", "run.fail":
		assignment, run, e := currentRun(state, body, actor)
		if e != nil {
			return Snapshot{}, Result{}, e
		}
		switch cmd.Operation {
		case "run.report":
			if text(body["message"]) == "" {
				return fail("INVALID_MESSAGE")
			}
			err = put("Report", Object{"task": ref, "report_id": id("report"), "run_id": run["run_id"],
				"reported_by": who, "message": body["message"], "extensions": optional(body, "extensions", Object{})})
		case "run.submit":
			basis, valid := integer(body["basis_version"])
			runBasis, basisOK := integer(run["basis_version"])
			if !valid || !basisOK || basis != runBasis {
				return fail("INVALID_BASIS")
			}
			if text(body["summary"]) == "" {
				return fail("INVALID_MESSAGE")
			}
			assignment["state"], run["state"] = "completed", "submitted"
			if err = put("Assignment", assignment); err == nil {
				err = put("Run", run)
			}
			if err == nil {
				err = put("Submission", Object{"task": ref, "submission_id": id("submission"),
					"assignment_id": assignment["assignment_id"], "run_id": run["run_id"], "basis_version": basis,
					"submitted_by": who, "summary": body["summary"], "artifacts": optional(body, "artifacts", []any{}),
					"extensions": optional(body, "extensions", Object{})})
			}
			clear()
			task["state"], task["pending_submission_id"] = "submitted", id("submission")
		case "run.fail":
			err = endCurrent(&state, &changed, body["reason"], "failed")
			clear()
			task["state"] = "failed"
		}
	case "task.decide":
		if !issuer() {
			return fail("ROLE_MISMATCH")
		}
		if text(task["state"]) != "submitted" || text(task["pending_submission_id"]) == "" ||
			text(task["pending_submission_id"]) != text(body["submission_id"]) {
			return fail("INVALID_STATE")
		}
		submission, e := record(state, "Submission", text(body["submission_id"]))
		if e != nil {
			return Snapshot{}, Result{}, e
		}
		outcome := text(body["outcome"])
		if (outcome != "accepted" && outcome != "rejected") || text(body["reason"]) == "" {
			return fail("INVALID_MESSAGE")
		}
		err = put("Decision", Object{"task": ref, "decision_id": id("decision"), "submission_id": submission["submission_id"],
			"decided_by": who, "outcome": outcome, "reason": body["reason"], "basis_version": submission["basis_version"],
			"evaluated_version": task["expectation_version"]})
		task["state"], task["pending_submission_id"] = "rejected", nil
		if outcome == "accepted" {
			task["state"] = "completed"
		}
	case "assignment.release":
		if _, e := currentAssignment(state, body, actor, true); e != nil {
			return Snapshot{}, Result{}, e
		}
		if text(task["state"]) != "claimed" && text(task["state"]) != "active" {
			return fail("INVALID_STATE")
		}
		err = endCurrent(&state, &changed, body["reason"], "released")
		clear()
		task["state"] = "offered"
	case "task.reopen":
		if !issuer() {
			return fail("ROLE_MISMATCH")
		}
		if text(task["state"]) != "failed" && text(task["state"]) != "rejected" {
			return fail("INVALID_STATE")
		}
		if text(body["reason"]) == "" {
			return fail("INVALID_MESSAGE")
		}
		clear()
		task["state"] = "offered"
	case "task.cancel":
		if !issuer() {
			return fail("ROLE_MISMATCH")
		}
		if terminal(text(task["state"])) {
			return fail("INVALID_STATE")
		}
		err = endCurrent(&state, &changed, body["reason"], "cancelled")
		clear()
		task["state"] = "cancelled"
	}
	if err != nil {
		return Snapshot{}, Result{}, err
	}
	task["revision"] = next
	// Independent copies avoid aliasing across state, result and event facts.
	resultTask, err := copyObject(task)
	if err != nil {
		return fail("INVALID_STATE")
	}
	eventTask, _ := copyObject(task)
	resultRecords, _ := copyRecords(changed)
	eventRecords := make([]any, 0, len(changed))
	for _, item := range changed {
		value, _ := copyObject(item.Record)
		eventRecords = append(eventRecords, Object{"record_type": item.RecordType, "record": value})
	}
	event := Object{"protocol_version": Version, "event_id": eventID, "task": taskObject(cmd.Task), "revision": next,
		"actor": actorObject(actor), "operation": cmd.Operation, "command_id": cmd.CommandID,
		"task_snapshot": eventTask, "records": eventRecords}
	if reason, exists := body["reason"]; exists {
		event["reason"] = reason
	}
	state, err = copySnapshot(state)
	if err != nil {
		return fail("INVALID_STATE")
	}
	return state, Result{ProtocolVersion: Version, CommandID: cmd.CommandID, Task: resultTask, Records: resultRecords, Event: event}, nil
}

func reject(kind string) error      { return &Error{Kind: kind} }
func text(value any) string         { s, _ := value.(string); return s }
func terminal(state string) bool    { return state == "completed" || state == "cancelled" }
func actorObject(a ActorRef) Object { return Object{"kind": a.Kind, "id": a.ID} }
func taskObject(t TaskRef) Object   { return Object{"authority_id": t.AuthorityID, "task_id": t.TaskID} }
func actorEqual(value any, actor ActorRef) bool {
	v, ok := value.(map[string]any)
	return ok && text(v["kind"]) == actor.Kind && text(v["id"]) == actor.ID
}
func optional(body Object, key string, fallback any) any {
	if value, exists := body[key]; exists {
		return value
	}
	return fallback
}
func operationKnown(op string) bool {
	switch op {
	case "task.create", "task.revise", "task.claim", "run.start", "run.report", "run.submit", "run.fail", "task.decide", "assignment.release", "task.reopen", "task.cancel":
		return true
	default:
		return false
	}
}
func expectation(body Object, version int64, actor Object, reason any) Object {
	out := Object{"version": version, "changed_by": actor, "reason": reason}
	for _, key := range []string{"goal", "expected_result", "delivery_requirements", "constraints", "inputs", "context", "extensions"} {
		if value, exists := body[key]; exists {
			out[key] = value
		}
	}
	return out
}
func record(state Snapshot, kind, identity string) (Object, error) {
	field := strings.ToLower(kind) + "_id"
	for _, item := range state.Records {
		if item.RecordType == kind && text(item.Record[field]) == identity && identity != "" {
			return copyObject(item.Record)
		}
	}
	return nil, reject("INVALID_STATE")
}
func putRecord(state *Snapshot, changed *[]Record, kind string, value Object) error {
	field := strings.ToLower(kind) + "_id"
	copied, err := copyObject(value)
	if err != nil || text(value[field]) == "" {
		return reject("INVALID_STATE")
	}
	found := false
	for i, old := range state.Records {
		if old.RecordType == kind && text(old.Record[field]) == text(value[field]) {
			state.Records[i] = Record{RecordType: kind, Record: copied}
			found = true
			break
		}
	}
	if !found {
		state.Records = append(state.Records, Record{RecordType: kind, Record: copied})
	}
	other, _ := copyObject(value)
	*changed = append(*changed, Record{RecordType: kind, Record: other})
	return nil
}
func currentAssignment(state Snapshot, body Object, actor ActorRef, allowIssuer bool) (Object, error) {
	generation, valid := integer(body["generation"])
	current, currentOK := integer(state.Task["generation"])
	if text(body["assignment_id"]) == "" || text(state.Task["current_assignment_id"]) != text(body["assignment_id"]) || !valid || !currentOK || generation != current {
		return nil, reject("STALE_ASSIGNMENT")
	}
	a, err := record(state, "Assignment", text(body["assignment_id"]))
	if err != nil {
		return nil, err
	}
	if text(a["state"]) != "claimed" && text(a["state"]) != "active" {
		return nil, reject("STALE_ASSIGNMENT")
	}
	if !actorEqual(a["assignee"], actor) && !(allowIssuer && actorEqual(state.Task["issuer"], actor)) {
		return nil, reject("ROLE_MISMATCH")
	}
	return a, nil
}
func currentRun(state Snapshot, body Object, actor ActorRef) (Object, Object, error) {
	a, err := currentAssignment(state, body, actor, false)
	if err != nil {
		return nil, nil, err
	}
	if text(body["run_id"]) == "" || text(state.Task["current_run_id"]) != text(body["run_id"]) {
		return nil, nil, reject("STALE_ASSIGNMENT")
	}
	r, err := record(state, "Run", text(body["run_id"]))
	if err != nil {
		return nil, nil, err
	}
	if text(state.Task["state"]) != "active" || text(a["state"]) != "active" || text(r["state"]) != "running" {
		return nil, nil, reject("INVALID_STATE")
	}
	return a, r, nil
}
func endCurrent(state *Snapshot, changed *[]Record, reason any, status string) error {
	if text(reason) == "" {
		return reject("INVALID_MESSAGE")
	}
	for _, kind := range []string{"Assignment", "Run"} {
		pointer := "current_" + strings.ToLower(kind) + "_id"
		if state.Task[pointer] == nil {
			continue
		}
		item, err := record(*state, kind, text(state.Task[pointer]))
		if err != nil {
			return err
		}
		item["state"], item["reason"] = status, reason
		if err := putRecord(state, changed, kind, item); err != nil {
			return err
		}
	}
	return nil
}

// integer accepts exact JSON integer spellings without expanding huge exponents.
func integer(value any) (int64, bool) {
	switch n := value.(type) {
	case int:
		return integer(int64(n))
	case int64:
		return n, n >= 0 && n <= MaxInteger
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > float64(MaxInteger) || math.Trunc(n) != n {
			return 0, false
		}
		return int64(n), true
	case json.Number:
		s := string(n)
		if !json.Valid([]byte(s)) || s == "" {
			return 0, false
		}
		negative := s[0] == '-'
		if negative {
			s = s[1:]
		}
		if s == "" || s[0] < '0' || s[0] > '9' {
			return 0, false
		}
		parts := strings.FieldsFunc(s, func(r rune) bool { return r == 'e' || r == 'E' })
		if len(parts) < 1 || len(parts) > 2 {
			return 0, false
		}
		digits := parts[0]
		scale := 0
		if dot := strings.IndexByte(digits, '.'); dot >= 0 {
			scale = len(digits) - dot - 1
			digits = digits[:dot] + digits[dot+1:]
		}
		digits = strings.TrimLeft(digits, "0")
		if digits == "" {
			return 0, true
		}
		if negative {
			return 0, false
		}
		var exponent int64
		if len(parts) == 2 {
			var err error
			exponent, err = strconv.ParseInt(parts[1], 10, 32)
			if err != nil {
				return 0, false
			}
		}
		exponent -= int64(scale)
		trimmed := strings.TrimRight(digits, "0")
		exponent += int64(len(digits) - len(trimmed))
		digits = trimmed
		if exponent < 0 || exponent > 16 || int64(len(digits))+exponent > 16 {
			return 0, false
		}
		whole, err := strconv.ParseInt(digits+strings.Repeat("0", int(exponent)), 10, 64)
		return whole, err == nil && whole <= MaxInteger
	default:
		return 0, false
	}
}

func copyObject(value Object) (Object, error) {
	copied, err := copyJSON(value, 0)
	if err != nil {
		return nil, err
	}
	out, ok := copied.(map[string]any)
	if !ok {
		return nil, reject("INVALID_STATE")
	}
	return out, nil
}
func copyJSON(value any, depth int) (any, error) {
	if depth > 256 {
		return nil, reject("INVALID_MESSAGE")
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(Object, len(v))
		for k, item := range v {
			copied, err := copyJSON(item, depth+1)
			if err != nil {
				return nil, err
			}
			out[k] = copied
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			copied, err := copyJSON(item, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = copied
		}
		return out, nil
	case []string:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = item
		}
		return out, nil
	case []Object:
		out := make([]any, len(v))
		for i, item := range v {
			copied, err := copyJSON(item, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = copied
		}
		return out, nil
	case nil, string, bool, json.Number, int, int64, float64:
		return v, nil
	default:
		return nil, reject("INVALID_MESSAGE")
	}
}
func copyRecords(records []Record) ([]Record, error) {
	out := make([]Record, len(records))
	for i, item := range records {
		value, err := copyObject(item.Record)
		if err != nil {
			return nil, err
		}
		out[i] = Record{RecordType: item.RecordType, Record: value}
	}
	return out, nil
}
func copySnapshot(snapshot Snapshot) (Snapshot, error) {
	task, err := copyObject(snapshot.Task)
	if err != nil {
		return Snapshot{}, err
	}
	records, err := copyRecords(snapshot.Records)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Task: task, Records: records}, nil
}
