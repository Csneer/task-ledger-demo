package application

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Csneer/task-ledger-demo/internal/contract"
	"github.com/Csneer/task-ledger-demo/internal/core"
	"github.com/Csneer/task-ledger-demo/internal/jsonvalue"
	"github.com/Csneer/task-ledger-demo/internal/storage"
)

const querySettings = "query-signing-v1"
const tokenPayloadSize = 130

type querySigning struct {
	key         [32]byte
	incarnation [16]byte
}
type queryToken struct {
	kind         byte
	scope, actor [32]byte
	after, upper int64
}

// Query implements the S3 read binding. Every result comes from one read
// transaction, including its watermark and current authorization checks.
func (s *Service) Query(ctx context.Context, h Host, raw []byte) (Object, error) {
	if len(raw) > MaxRequestBytes {
		return nil, bindingError("CAPACITY_LIMIT")
	}
	req, e := parseObject(raw)
	if e != nil {
		return nil, bindingError("INVALID_MESSAGE")
	}
	if str(req["binding_version"]) != BindingVersion {
		return nil, bindingError("UNSUPPORTED_BINDING")
	}
	op := str(req["operation"])
	switch op {
	case "service.info", "task.snapshot", "workspace.snapshot", "events.open", "events.read", "session.get", "profiles.list":
	default:
		return nil, bindingError("UNSUPPORTED_OPERATION")
	}
	if e = contract.ValidateBinding("QueryRequest", req); e != nil {
		return nil, bindingError("INVALID_MESSAGE")
	}
	if e = contract.ValidateCore("ActorRef", actorObject(h.Actor)); e != nil {
		return nil, bindingError("FORBIDDEN")
	}
	if e = s.ensureQuerySigning(ctx, h); e != nil {
		return nil, e
	}
	var result Object
	e = s.db.View(ctx, func(tx *storage.Tx) error {
		if e := s.participant(tx, h, false); e != nil {
			return e
		}
		signing, e := readSigning(tx)
		if e != nil {
			return e
		}
		body := object(req["body"])
		var view Object
		switch op {
		case "service.info":
			view = Object{"authority_id": s.authority, "binding_version": BindingVersion, "protocol_version": core.Version, "upstream_commit": "e6dae9a6127537af94f0f5127e8d590a1e6a3f1a", "core_operations": []string{"task.create", "task.revise", "task.claim", "run.start", "run.report", "run.submit", "run.fail", "task.decide", "assignment.release", "task.reopen", "task.cancel"}, "limits": Object{"request_bytes": MaxRequestBytes, "response_bytes": MaxResponseBytes, "snapshot_tasks": 1000, "page_events": 200}}
		case "task.snapshot":
			ref := taskRef(object(body["task"]))
			if ref.AuthorityID != s.authority {
				return bindingError("AUTHORITY_MISMATCH")
			}
			bundle, e := queryTask(tx, ref)
			if e != nil {
				return e
			}
			w, e := tx.Watermark()
			if e != nil {
				return e
			}
			scope := taskScope(ref)
			view = Object{"bundle": bundle, "watermark": s.signToken(signing, queryToken{kind: 'w', scope: scope, after: w}), "event_cursor": s.signToken(signing, queryToken{kind: 'f', scope: scope, actor: actorScope(h.Actor), after: w})}
		case "workspace.snapshot":
			view, e = s.queryWorkspace(tx, h, signing)
		case "session.get":
			view, e = querySession(tx, str(body["session_id"]))
		case "profiles.list":
			view = Object{"profiles": []any{}}
		case "events.open":
			view, e = s.queryOpen(tx, h, signing, body)
		case "events.read":
			view, e = s.queryEvents(tx, h, signing, body)
		}
		if e != nil {
			return e
		}
		result = Object{"binding_version": BindingVersion, "result": view}
		encoded, e := jsonvalue.Marshal(result)
		if e != nil {
			return e
		}
		if len(encoded) > MaxResponseBytes {
			return bindingError("CAPACITY_LIMIT")
		}
		if e = checkSchema("View_"+strings.ReplaceAll(op, ".", "_"), result); e != nil {
			return fmt.Errorf("query output schema: %w", e)
		}
		return nil
	})
	if errors.Is(e, storage.ErrCapacity) {
		return nil, bindingError("CAPACITY_LIMIT")
	}
	if e != nil {
		return nil, e
	}
	return result, nil
}

// Only the first valid read creates signing metadata. Later reads remain read
// transactions. The second check under the write lock handles concurrent starts.
func (s *Service) ensureQuerySigning(ctx context.Context, h Host) error {
	e := s.db.View(ctx, func(tx *storage.Tx) error {
		if e := s.participant(tx, h, false); e != nil {
			return e
		}
		_, e := readSigning(tx)
		return e
	})
	if !errors.Is(e, storage.ErrNotFound) {
		return e
	}
	return s.db.Update(ctx, func(tx *storage.Tx) error {
		if e := s.participant(tx, h, false); e != nil {
			return e
		}
		if _, e := readSigning(tx); !errors.Is(e, storage.ErrNotFound) {
			return e
		}
		raw := make([]byte, 48)
		if _, e := rand.Read(raw); e != nil {
			return e
		}
		return tx.Insert("settings", querySettings, raw)
	})
}
func readSigning(tx *storage.Tx) (querySigning, error) {
	var out querySigning
	raw, e := tx.Get("settings", querySettings)
	if e != nil {
		return out, e
	}
	if len(raw) != 48 {
		return out, errors.New("invalid query signing configuration")
	}
	copy(out.key[:], raw[:32])
	copy(out.incarnation[:], raw[32:])
	return out, nil
}
func actorScope(actor core.ActorRef) [32]byte { return sha256.Sum256([]byte(actorKey(actor))) }
func taskScope(task core.TaskRef) [32]byte    { return sha256.Sum256([]byte(taskKey(task))) }
func (s *Service) signToken(signing querySigning, t queryToken) string {
	raw := make([]byte, tokenPayloadSize)
	raw[0], raw[1] = 1, t.kind
	copy(raw[2:18], signing.incarnation[:])
	authority := sha256.Sum256([]byte(s.authority))
	copy(raw[18:50], authority[:])
	copy(raw[50:82], t.scope[:])
	copy(raw[82:114], t.actor[:])
	binary.BigEndian.PutUint64(raw[114:122], uint64(t.after))
	binary.BigEndian.PutUint64(raw[122:130], uint64(t.upper))
	mac := hmac.New(sha256.New, signing.key[:])
	_, _ = mac.Write(raw)
	return base64.RawURLEncoding.EncodeToString(append(raw, mac.Sum(nil)...))
}
func (s *Service) openToken(signing querySigning, encoded string) (queryToken, error) {
	var out queryToken
	raw, e := base64.RawURLEncoding.DecodeString(encoded)
	if e != nil || len(raw) != tokenPayloadSize+32 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return out, bindingError("INVALID_CURSOR")
	}
	mac := hmac.New(sha256.New, signing.key[:])
	_, _ = mac.Write(raw[:tokenPayloadSize])
	if !hmac.Equal(mac.Sum(nil), raw[tokenPayloadSize:]) || raw[0] != 1 {
		return out, bindingError("INVALID_CURSOR")
	}
	authority := sha256.Sum256([]byte(s.authority))
	if !hmac.Equal(authority[:], raw[18:50]) {
		return out, bindingError("INVALID_CURSOR")
	}
	if !hmac.Equal(signing.incarnation[:], raw[2:18]) {
		return out, bindingError("CURSOR_RESET_REQUIRED")
	}
	out.kind = raw[1]
	copy(out.scope[:], raw[50:82])
	copy(out.actor[:], raw[82:114])
	out.after = int64(binary.BigEndian.Uint64(raw[114:122]))
	out.upper = int64(binary.BigEndian.Uint64(raw[122:130]))
	if out.after < 0 || out.upper < 0 {
		return out, bindingError("INVALID_CURSOR")
	}
	switch out.kind {
	case 'w':
		if out.actor != ([32]byte{}) || out.upper != 0 {
			return out, bindingError("INVALID_CURSOR")
		}
	case 'f':
		if out.upper != 0 {
			return out, bindingError("INVALID_CURSOR")
		}
	case 'c':
		if out.after > out.upper {
			return out, bindingError("INVALID_CURSOR")
		}
	default:
		return out, bindingError("INVALID_CURSOR")
	}
	return out, nil
}

// queryObject checks the byte budget in storage before loading or decoding a
// BLOB. The caller shares this budget across all public objects in a result.
func queryObject(tx *storage.Tx, bucket, k string, budget *int) (Object, error) {
	if *budget <= 0 {
		return nil, storage.ErrCapacity
	}
	raw, e := tx.GetBounded(bucket, k, *budget)
	if e != nil {
		return nil, e
	}
	*budget -= len(raw)
	return parseObject(raw)
}
func queryTask(tx *storage.Tx, ref core.TaskRef) (Object, error) {
	budget := MaxResponseBytes
	snap, e := queryObject(tx, "tasks", taskKey(ref), &budget)
	if errors.Is(e, storage.ErrNotFound) {
		return nil, coreError("TASK_NOT_FOUND")
	}
	if e != nil {
		return nil, e
	}
	meta, e := queryObject(tx, "metadata", taskKey(ref), &budget)
	if e != nil {
		return nil, e
	}
	return Object{"snapshot": snap, "application": meta}, nil
}
func querySession(tx *storage.Tx, sid string) (Object, error) {
	budget := MaxResponseBytes
	sess, e := queryObject(tx, "sessions", sid, &budget)
	if errors.Is(e, storage.ErrNotFound) {
		return nil, bindingError("NOT_FOUND")
	}
	if e != nil {
		return nil, e
	}
	return querySessionView(tx, sid, sess, &budget)
}
func querySessionView(tx *storage.Tx, sid string, sess Object, budget *int) (Object, error) {
	var binding any
	raw, e := tx.GetBounded("session-occupancy", sid, MaxResponseBytes)
	if e == nil {
		v, e := jsonvalue.Parse(raw)
		if e != nil {
			return nil, e
		}
		binding, e = queryObject(tx, "bindings", str(v), budget)
		if e != nil {
			return nil, e
		}
	} else if !errors.Is(e, storage.ErrNotFound) {
		return nil, e
	}
	return Object{"session": sess, "current_binding": binding}, nil
}
func (s *Service) queryWorkspace(tx *storage.Tx, h Host, signing querySigning) (Object, error) {
	view := Object{}
	budget := MaxResponseBytes
	for _, bucket := range []string{"tasks", "projects", "participants", "handoffs", "sessions", "wakes", "profiles"} {
		rows, e := tx.ListBounded(bucket, 1001, budget)
		if e != nil {
			return nil, e
		}
		if len(rows) > 1000 {
			return nil, bindingError("CAPACITY_LIMIT")
		}
		// Reserve all rows already loaded before fetching accompanying metadata.
		for _, row := range rows {
			budget -= len(row.Value)
		}
		values := []any{}
		for _, row := range rows {
			value, e := parseObject(row.Value)
			if e != nil {
				return nil, e
			}
			switch bucket {
			case "tasks":
				meta, e := queryObject(tx, "metadata", row.Key, &budget)
				if e != nil {
					return nil, e
				}
				value = Object{"snapshot": value, "application": meta}
			case "sessions":
				value, e = querySessionView(tx, row.Key, value, &budget)
				if e != nil {
					return nil, e
				}
			}
			values = append(values, value)
		}
		view[bucket] = values
	}
	w, e := tx.Watermark()
	if e != nil {
		return nil, e
	}
	view["watermark"] = s.signToken(signing, queryToken{kind: 'w', after: w})
	view["event_cursor"] = s.signToken(signing, queryToken{kind: 'f', actor: actorScope(h.Actor), after: w})
	return view, nil
}
func (s *Service) queryOpen(tx *storage.Tx, h Host, signing querySigning, b Object) (Object, error) {
	var scope [32]byte
	if refValue, ok := b["task"]; ok {
		ref := taskRef(object(refValue))
		if ref.AuthorityID != s.authority {
			return nil, bindingError("AUTHORITY_MISMATCH")
		}
		scope = taskScope(ref)
	}
	var after int64
	if encoded, ok := b["after"]; ok {
		token, e := s.openToken(signing, str(encoded))
		if e != nil {
			return nil, e
		}
		if token.kind != 'w' || token.scope != scope {
			return nil, bindingError("INVALID_CURSOR")
		}
		after = token.after
	}
	upper, e := tx.Watermark()
	if e != nil {
		return nil, e
	}
	if after > upper {
		return nil, bindingError("CURSOR_RESET_REQUIRED")
	}
	return Object{"cursor": s.signToken(signing, queryToken{kind: 'c', scope: scope, actor: actorScope(h.Actor), after: after, upper: upper}), "watermark": s.signToken(signing, queryToken{kind: 'w', scope: scope, after: upper})}, nil
}
func (s *Service) queryEvents(tx *storage.Tx, h Host, signing querySigning, b Object) (Object, error) {
	token, e := s.openToken(signing, str(b["cursor"]))
	if e != nil {
		return nil, e
	}
	if token.kind != 'c' && token.kind != 'f' {
		return nil, bindingError("INVALID_CURSOR")
	}
	if token.actor != actorScope(h.Actor) {
		return nil, bindingError("FORBIDDEN")
	}
	current, e := tx.Watermark()
	if e != nil {
		return nil, e
	}
	if token.after > current || token.upper > current {
		return nil, bindingError("CURSOR_RESET_REQUIRED")
	}
	if token.kind == 'f' {
		token.kind = 'c'
		token.upper = current
	}
	limit := int(number(b["limit"]))
	events := []any{}
	position := token.after
	lastEmitted := position
	total := 0
	more := false
	// Read one byte-bounded row at a time: even limit=1 must not preload
	// hundreds of large history events. A filtered stream may scan unrelated
	// rows; one extra matching event determines done without truncating a page.
	for position < token.upper && !more {
		rows, e := tx.EventsBounded(position, 1, MaxResponseBytes)
		if e != nil {
			return nil, e
		}
		if len(rows) == 0 {
			break
		}
		for _, row := range rows {
			if row.Position > token.upper {
				position = token.upper
				break
			}
			position = row.Position
			entry, e := parseObject(row.Body)
			if e != nil {
				return nil, e
			}
			if token.scope != ([32]byte{}) {
				ref := taskRef(object(object(entry["event"])["task"]))
				if taskScope(ref) != token.scope {
					continue
				}
			}
			if len(events) == limit {
				more = true
				break
			}
			total += len(row.Body)
			if total > MaxResponseBytes {
				return nil, bindingError("CAPACITY_LIMIT")
			}
			entry["position"] = strconv.FormatInt(row.Position, 10)
			events = append(events, entry)
			lastEmitted = row.Position
		}
	}
	result := Object{"events": events, "done": !more, "next_cursor": nil, "follow_cursor": nil}
	if more {
		token.after = lastEmitted
		result["next_cursor"] = s.signToken(signing, token)
	} else {
		token.kind = 'f'
		token.after = token.upper
		token.upper = 0
		result["follow_cursor"] = s.signToken(signing, token)
	}
	return result, nil
}
