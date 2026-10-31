package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/Csneer/task-ledger-demo/internal/contract"
	"github.com/Csneer/task-ledger-demo/internal/core"
	"github.com/Csneer/task-ledger-demo/internal/jsonvalue"
	"github.com/Csneer/task-ledger-demo/internal/storage"
)

// Action implements the registry, project, session and policy operations needed
// to enforce S2 atomic execution. Handoffs, wake dispatch and HTTP remain later.
func (s *Service) Action(ctx context.Context, h Host, raw []byte) (Object, error) {
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
	op := str(request["operation"])
	switch op {
	case "participant.create", "participant.update", "project.create", "project.update", "session.begin", "session.update", "session.rotate", "session.end", "session.transfer", "claim_policy.update":
	default:
		return nil, bindingError("UNSUPPORTED_OPERATION")
	}
	if e = contract.ValidateBinding("ActionRequest", request); e != nil {
		return nil, bindingError("INVALID_MESSAGE")
	}
	if e = contract.ValidateCore("ActorRef", actorObject(h.Actor)); e != nil {
		return nil, bindingError("FORBIDDEN")
	}
	rid := str(request["request_id"])
	receiptKey := key(s.authority, "actor", h.Actor.Kind, h.Actor.ID, rid)
	var result Object
	e = s.db.Update(ctx, func(tx *storage.Tx) error {
		if e := s.checkAuthority(tx); e != nil {
			return e
		}
		saved, e := get(tx, "action-receipts", receiptKey)
		if e == nil {
			if e = s.participant(tx, h, false); e != nil {
				return e
			}
			if !equal(saved["request"], request) {
				return bindingError("IDEMPOTENCY_CONFLICT")
			}
			result = object(saved["response"])
			result["replayed"] = true
			return nil
		}
		if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		if e = s.participant(tx, h, true); e != nil {
			return e
		}
		body := object(request["body"])
		rev := number(request["expected_revision"])
		var out Object
		switch op {
		case "participant.create", "participant.update":
			out, e = s.participantAction(tx, h, op, body, rev)
		case "project.create", "project.update":
			out, e = projectAction(tx, h, op, body, rev)
		case "session.begin", "session.update", "session.rotate", "session.end":
			out, e = sessionAction(tx, h, op, body, rev)
		case "session.transfer":
			out, e = s.transfer(tx, h, body, rev)
		case "claim_policy.update":
			out, e = s.policyAction(tx, h, body, rev)
		}
		if e != nil {
			return e
		}
		result = Object{"binding_version": BindingVersion, "request_id": rid, "replayed": false, "result": out}
		if e = checkSchema("ActionSuccess", result); e != nil {
			return fmt.Errorf("action output schema: %w", e)
		}
		encoded, e := jsonvalue.Marshal(result)
		if e != nil {
			return e
		}
		if len(encoded) > MaxResponseBytes {
			return bindingError("CAPACITY_LIMIT")
		}
		return insert(tx, "action-receipts", receiptKey, Object{"request": request, "response": result})
	})
	if e != nil {
		return nil, e
	}
	return result, nil
}
func requireAdmin(tx *storage.Tx, h Host) error {
	if !h.Admin {
		return bindingError("FORBIDDEN")
	}
	_, e := tx.Get("operators", actorKey(h.Actor))
	if errors.Is(e, storage.ErrNotFound) {
		return bindingError("FORBIDDEN")
	}
	return e
}
func (s *Service) participantAction(tx *storage.Tx, h Host, op string, b Object, rev int64) (Object, error) {
	if e := requireAdmin(tx, h); e != nil {
		return nil, e
	}
	a := object(b["actor"])
	k := key(str(a["kind"]), str(a["id"]))
	if op == "participant.create" {
		if e := absent(tx, "participants", k); e != nil {
			return nil, e
		}
	} else {
		old, e := required(tx, "participants", k)
		if e != nil {
			return nil, e
		}
		if e = revision(old, rev); e != nil {
			return nil, e
		}
		if str(b["state"]) == "read_only" {
			operators, e := tx.List("operators")
			if e != nil {
				return nil, e
			}
			enabled := 0
			for _, o := range operators {
				p, e := get(tx, "participants", o.Key)
				if e != nil {
					return nil, e
				}
				if o.Key != k && str(p["state"]) == "enabled" {
					enabled++
				}
			}
			if _, e := tx.Get("operators", k); e == nil && enabled == 0 {
				return nil, bindingError("INVALID_STATE")
			} else if e != nil && !errors.Is(e, storage.ErrNotFound) {
				return nil, e
			}
		}
	}
	p := Object{"actor": a, "display_name": b["display_name"], "state": b["state"], "profile": b["profile"], "revision": rev + 1}
	if e := put(tx, "participants", k, p); e != nil {
		return nil, e
	}
	return p, appendApplication(tx, op, "participant", k, rev+1, actorObject(h.Actor), p, nil)
}
func projectAction(tx *storage.Tx, h Host, op string, b Object, rev int64) (Object, error) {
	k := str(b["project_id"])
	creator := any(actorObject(h.Actor))
	if op == "project.create" {
		if e := absent(tx, "projects", k); e != nil {
			return nil, e
		}
	} else {
		p, e := required(tx, "projects", k)
		if e != nil {
			return nil, e
		}
		if e = revision(p, rev); e != nil {
			return nil, e
		}
		if !equal(p["created_by"], actorObject(h.Actor)) {
			if e = requireAdmin(tx, h); e != nil {
				return nil, e
			}
		}
		creator = p["created_by"]
	}
	p := Object{"project_id": k, "title": b["title"], "description": b["description"], "created_by": creator, "revision": rev + 1}
	if e := put(tx, "projects", k, p); e != nil {
		return nil, e
	}
	return p, appendApplication(tx, op, "project", k, rev+1, actorObject(h.Actor), p, nil)
}
func sessionAction(tx *storage.Tx, h Host, op string, b Object, rev int64) (Object, error) {
	sid := str(b["session_id"])
	var sess Object
	if op == "session.begin" {
		if e := absent(tx, "sessions", sid); e != nil {
			return nil, e
		}
		if _, e := required(tx, "projects", str(b["project_id"])); e != nil {
			return nil, e
		}
		now := stamp()
		sess = Object{"session_id": sid, "actor": actorObject(h.Actor), "project_id": b["project_id"], "goal": b["goal"], "state": "active", "phase": "preparing", "summary": b["goal"], "revision": int64(1), "proof_epoch": int64(1), "created_at": now, "last_seen_at": now}
		for _, f := range []string{"provider", "host_ref", "workspace_ref"} {
			if v, ok := b[f]; ok {
				sess[f] = v
			}
		}
		if e := insert(tx, "session-secrets", sid, Object{"digest": b["proof_digest"], "used": []any{b["proof_digest"]}}); e != nil {
			return nil, e
		}
	} else {
		var e error
		sess, e = required(tx, "sessions", sid)
		if e != nil {
			return nil, e
		}
		if e = revision(sess, rev); e != nil {
			return nil, e
		}
		if _, e = session(tx, h, sid, str(sess["project_id"]), true); e != nil {
			return nil, e
		}
		switch op {
		case "session.update":
			sess["phase"] = b["phase"]
			sess["summary"] = b["summary"]
		case "session.end":
			sess["state"] = "ended"
			sess["outcome"] = b["outcome"]
			sess["summary"] = b["summary"]
			sess["ended_at"] = stamp()
		case "session.rotate":
			secret, e := get(tx, "session-secrets", sid)
			if e != nil {
				return nil, e
			}
			used, ok := secret["used"].([]any)
			if !ok {
				return nil, errors.New("invalid stored proof history")
			}
			for _, d := range used {
				if equal(d, b["proof_digest"]) {
					return nil, bindingError("INVALID_STATE")
				}
			}
			if number(sess["proof_epoch"]) >= core.MaxInteger {
				return nil, bindingError("INVALID_STATE")
			}
			sess["proof_epoch"] = number(sess["proof_epoch"]) + 1
			secret["digest"] = b["proof_digest"]
			secret["used"] = append(used, b["proof_digest"])
			if e = put(tx, "session-secrets", sid, secret); e != nil {
				return nil, e
			}
		}
		sess["revision"] = rev + 1
		sess["last_seen_at"] = stamp()
	}
	if e := put(tx, "sessions", sid, sess); e != nil {
		return nil, e
	}
	return sess, appendApplication(tx, op, "session", sid, number(sess["revision"]), actorObject(h.Actor), sess, nil)
}
func (s *Service) policyAction(tx *storage.Tx, h Host, b Object, rev int64) (Object, error) {
	t := taskRef(object(b["task"]))
	if t.AuthorityID != s.authority {
		return nil, bindingError("AUTHORITY_MISMATCH")
	}
	meta, e := required(tx, "metadata", taskKey(t))
	if e != nil {
		return nil, e
	}
	if number(meta["policy_revision"]) != rev {
		return nil, bindingError("STALE_REVISION")
	}
	if rev >= core.MaxInteger {
		return nil, bindingError("INVALID_STATE")
	}
	snap, e := required(tx, "tasks", taskKey(t))
	if e != nil {
		return nil, e
	}
	task := object(snap["task"])
	if number(task["revision"]) != number(b["expected_task_revision"]) {
		return nil, bindingError("STALE_REVISION")
	}
	if !equal(task["issuer"], actorObject(h.Actor)) {
		return nil, bindingError("FORBIDDEN")
	}
	if str(task["state"]) != "offered" {
		return nil, bindingError("INVALID_STATE")
	}
	if e = validatePolicy(tx, object(b["claim_policy"])); e != nil {
		return nil, e
	}
	meta["claim_policy"] = b["claim_policy"]
	meta["policy_revision"] = rev + 1
	if e = put(tx, "metadata", taskKey(t), meta); e != nil {
		return nil, e
	}
	return meta, appendApplication(tx, "claim_policy.update", "policy", taskKey(t), rev+1, actorObject(h.Actor), meta, &t)
}
func (s *Service) transfer(tx *storage.Tx, h Host, b Object, rev int64) (Object, error) {
	t := taskRef(object(b["task"]))
	if t.AuthorityID != s.authority {
		return nil, bindingError("AUTHORITY_MISMATCH")
	}
	meta, e := required(tx, "metadata", taskKey(t))
	if e != nil {
		return nil, e
	}
	binding := object(meta["execution_binding"])
	if binding == nil {
		return nil, bindingError("INVALID_STATE")
	}
	if e = revision(binding, rev); e != nil {
		return nil, e
	}
	if !equal(binding["assignment_id"], b["assignment_id"]) || !equal(binding["generation"], b["generation"]) || str(binding["session_id"]) != h.SessionID {
		return nil, bindingError("SESSION_MISMATCH")
	}
	if _, e = session(tx, h, h.SessionID, str(meta["project_id"]), false); e != nil {
		return nil, e
	}
	target := str(b["target_session_id"])
	if target == h.SessionID {
		return nil, bindingError("INVALID_STATE")
	}
	th := h
	th.SessionID = target
	th.SessionProof = h.TargetSessionProof
	if _, e = session(tx, th, target, str(meta["project_id"]), true); e != nil {
		return nil, e
	}
	if _, e = tx.Get("session-occupancy", target); e == nil {
		return nil, bindingError("SESSION_BUSY")
	} else if !errors.Is(e, storage.ErrNotFound) {
		return nil, e
	}
	snap, e := required(tx, "tasks", taskKey(t))
	if e != nil {
		return nil, e
	}
	task := object(snap["task"])
	if !equal(task["current_assignment_id"], b["assignment_id"]) || !equal(task["generation"], b["generation"]) {
		return nil, bindingError("SESSION_MISMATCH")
	}
	if e = tx.Delete("session-occupancy", h.SessionID); e != nil {
		return nil, e
	}
	k := recordKey(t, str(b["assignment_id"]))
	if e = insert(tx, "session-occupancy", target, k); e != nil {
		return nil, e
	}
	binding["session_id"] = target
	binding["revision"] = rev + 1
	if e = put(tx, "bindings", k, binding); e != nil {
		return nil, e
	}
	if e = put(tx, "metadata", taskKey(t), meta); e != nil {
		return nil, e
	}
	return binding, appendApplication(tx, "session.transfer", "binding", k, rev+1, actorObject(h.Actor), binding, &t)
}

func (s *Service) Session(ctx context.Context, h Host, sid string) (Object, error) {
	var result Object
	e := s.db.View(ctx, func(tx *storage.Tx) error {
		if e := s.participant(tx, h, false); e != nil {
			return e
		}
		sess, e := required(tx, "sessions", sid)
		if e != nil {
			return e
		}
		var binding any
		raw, e := tx.Get("session-occupancy", sid)
		if e == nil {
			v, e := jsonvalue.Parse(raw)
			if e != nil {
				return e
			}
			binding, e = get(tx, "bindings", str(v))
			if e != nil {
				return e
			}
		} else if !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		result = Object{"session": sess, "current_binding": binding}
		return checkSchema("SessionView", result)
	})
	return result, e
}
