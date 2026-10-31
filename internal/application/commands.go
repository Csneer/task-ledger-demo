package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Csneer/task-ledger-demo/internal/contract"
	"github.com/Csneer/task-ledger-demo/internal/core"
	"github.com/Csneer/task-ledger-demo/internal/jsonvalue"
	"github.com/Csneer/task-ledger-demo/internal/storage"
)

// Execute handles a complete S1 command envelope through the S2 transaction
// boundary. Credential authentication belongs to the future transport adapter.
func (s *Service) Execute(ctx context.Context, h Host, raw []byte) (Object, error) {
	if len(raw) > MaxRequestBytes {
		return nil, bindingError("CAPACITY_LIMIT")
	}
	request, e := parseObject(raw)
	if e != nil {
		return nil, bindingError("INVALID_MESSAGE")
	}
	if str(request["binding_version"]) != BindingVersion {
		return nil, bindingError("UNSUPPORTED_BINDING")
	}
	commandValue := object(request["command"])
	if commandValue == nil {
		return nil, bindingError("INVALID_MESSAGE")
	}
	if str(commandValue["protocol_version"]) != core.Version {
		return nil, coreError("UNSUPPORTED_VERSION")
	}
	if !knownCore(str(commandValue["operation"])) {
		return nil, coreError("UNSUPPORTED_OPERATION")
	}
	if e = contract.ValidateCore("Command", commandValue); e != nil {
		return nil, coreError("INVALID_MESSAGE")
	}
	if e = contract.ValidateCore("ActorRef", actorObject(h.Actor)); e != nil {
		return nil, bindingError("FORBIDDEN")
	}
	if e = contract.ValidateBinding("CommandRequest", request); e != nil {
		return nil, bindingError("INVALID_MESSAGE")
	}
	cmd, e := core.DecodeCommand(commandValue)
	if e != nil {
		return nil, translate(e)
	}
	if cmd.Task.AuthorityID != s.authority {
		return nil, bindingError("AUTHORITY_MISMATCH")
	}
	receiptKey := key(s.authority, h.Actor.Kind, h.Actor.ID, cmd.CommandID)
	var result Object
	e = s.db.Update(ctx, func(tx *storage.Tx) error {
		if e := s.checkAuthority(tx); e != nil {
			return e
		}
		saved, e := get(tx, "command-receipts", receiptKey)
		if e == nil {
			if e = s.participant(tx, h, false); e != nil {
				return e
			}
			original := object(saved["request"])
			if !equal(original["command"], request["command"]) {
				return coreError("IDEMPOTENCY_CONFLICT")
			}
			if !equal(original, request) {
				return bindingError("IDEMPOTENCY_CONFLICT")
			}
			result = object(saved["response"])
			object(result["result"])["replayed"] = true
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		if e = s.participant(tx, h, true); e != nil {
			return e
		}
		var prior *core.Snapshot
		state, e := tx.Get("tasks", taskKey(cmd.Task))
		if e == nil {
			snap, e := decodeSnapshot(state)
			if e != nil {
				return e
			}
			prior = &snap
		} else if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		eventID, e := id()
		if e != nil {
			return e
		}
		next, coreResult, e := core.Transition(prior, cmd, h.Actor, eventID)
		if e != nil {
			return translate(e)
		}
		// The pure candidate does not write. Any host rejection rolls back all facts.
		app := object(request["application"])
		if cmd.Operation == "task.claim" && str(app["session_id"]) != h.SessionID {
			return bindingError("SESSION_MISMATCH")
		}
		var meta Object
		if cmd.Operation == "task.create" {
			if _, e = required(tx, "projects", str(app["project_id"])); e != nil {
				return e
			}
			if e = validatePolicy(tx, object(app["claim_policy"])); e != nil {
				return e
			}
			// There are no configured launch profiles in the S2 host. Do not silently
			// discard wake: S4 will supply the durable profile/outbox implementation.
			if _, present := app["wake"]; present {
				return bindingError("PROFILE_UNAVAILABLE")
			}
			meta = Object{"project_id": app["project_id"], "claim_policy": app["claim_policy"], "policy_revision": int64(1), "execution_binding": nil}
		} else {
			meta, e = get(tx, "metadata", taskKey(cmd.Task))
			if e != nil {
				return e
			}
		}
		if e = updateExecution(tx, h, cmd, meta, next.Task); e != nil {
			return e
		}
		if e = put(tx, "tasks", taskKey(cmd.Task), next); e != nil {
			return e
		}
		if e = put(tx, "metadata", taskKey(cmd.Task), meta); e != nil {
			return e
		}
		if cmd.Operation == "task.create" {
			if e = appendApplication(tx, cmd.Operation, "policy", taskKey(cmd.Task), 1, actorObject(h.Actor), meta, &cmd.Task); e != nil {
				return e
			}
		}
		eventRaw, e := jsonvalue.Marshal(Object{"recorded_at": stamp(), "source": "core", "event": coreResult.Event})
		if e != nil {
			return e
		}
		if _, e = tx.AppendEvent(eventID, eventRaw); e != nil {
			return e
		}
		if nextTree, e := tree(next); e != nil {
			return e
		} else if e = contract.ValidateCore("Snapshot", nextTree); e != nil {
			return fmt.Errorf("core snapshot output: %w", e)
		}
		out := Object{"binding_version": BindingVersion, "result": coreResult, "application": Object{"task_meta": meta, "wake": nil}}
		normalized, e := tree(out)
		if e != nil {
			return e
		}
		result = object(normalized)
		if e = contract.ValidateBinding("CommandSuccess", result); e != nil {
			return fmt.Errorf("command output schema: %w", e)
		}
		response, e := jsonvalue.Marshal(result)
		if e != nil {
			return e
		}
		if len(response) > MaxResponseBytes {
			return bindingError("CAPACITY_LIMIT")
		}
		return insert(tx, "command-receipts", receiptKey, Object{"request": request, "response": result})
	})
	if e != nil {
		return nil, e
	}
	return result, nil
}
func knownCore(op string) bool {
	switch op {
	case "task.create", "task.revise", "task.claim", "run.start", "run.report", "run.submit", "run.fail", "task.decide", "assignment.release", "task.reopen", "task.cancel":
		return true
	}
	return false
}
func validatePolicy(tx *storage.Tx, policy Object) error {
	if str(policy["mode"]) != "target" {
		return nil
	}
	a := object(policy["target"])
	p, e := required(tx, "participants", key(str(a["kind"]), str(a["id"])))
	if e != nil {
		return e
	}
	if str(p["state"]) != "enabled" {
		return bindingError("FORBIDDEN")
	}
	return nil
}
func updateExecution(tx *storage.Tx, h Host, cmd core.Command, meta, task Object) error {
	binding := object(meta["execution_binding"])
	project := str(meta["project_id"])
	if cmd.Operation == "task.claim" {
		policy := object(meta["claim_policy"])
		if str(policy["mode"]) == "target" && !equal(policy["target"], actorObject(h.Actor)) {
			return bindingError("FORBIDDEN")
		}
		// application.session_id was checked against the trusted session header by
		// Execute's claim helper; the actual session is checked inside this transaction.
		if _, e := session(tx, h, h.SessionID, project, true); e != nil {
			return e
		}
		if _, e := tx.Get("session-occupancy", h.SessionID); e == nil {
			return bindingError("SESSION_BUSY")
		} else if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		binding = Object{"task": taskObject(cmd.Task), "assignment_id": task["current_assignment_id"], "generation": task["generation"], "session_id": h.SessionID, "revision": int64(1), "state": "current", "run_id": nil}
		meta["execution_binding"] = binding
		if e := insert(tx, "session-occupancy", h.SessionID, recordKey(cmd.Task, str(binding["assignment_id"]))); e != nil {
			return e
		}
	} else {
		requires := strings.HasPrefix(cmd.Operation, "run.") || (cmd.Operation == "assignment.release" && !equal(task["issuer"], actorObject(h.Actor)))
		if requires {
			if binding == nil || str(binding["session_id"]) != h.SessionID {
				return bindingError("SESSION_MISMATCH")
			}
			if _, e := session(tx, h, h.SessionID, project, true); e != nil {
				return e
			}
		}
		if binding == nil {
			return nil
		}
		changed := false
		if cmd.Operation == "run.start" {
			binding["run_id"] = task["current_run_id"]
			changed = true
		}
		if task["current_assignment_id"] == nil {
			binding["state"] = "ended"
			meta["execution_binding"] = nil
			changed = true
			if e := tx.Delete("session-occupancy", str(binding["session_id"])); e != nil {
				return e
			}
		}
		if !changed {
			return nil
		}
		if number(binding["revision"]) >= core.MaxInteger {
			return bindingError("INVALID_STATE")
		}
		binding["revision"] = number(binding["revision"]) + 1
	}
	k := recordKey(cmd.Task, str(binding["assignment_id"]))
	if e := put(tx, "bindings", k, binding); e != nil {
		return e
	}
	return appendApplication(tx, cmd.Operation, "binding", k, number(binding["revision"]), actorObject(h.Actor), binding, &cmd.Task)
}
