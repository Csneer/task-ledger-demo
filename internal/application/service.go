// Package application coordinates trusted-host commands with durable protocol
// facts. This S2 entry point is not an HTTP authentication boundary.
package application

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/Csneer/task-ledger-demo/internal/contract"
	"github.com/Csneer/task-ledger-demo/internal/core"
	"github.com/Csneer/task-ledger-demo/internal/jsonvalue"
	"github.com/Csneer/task-ledger-demo/internal/storage"
)

const BindingVersion = "ledger-http-draft-1"
const MaxRequestBytes = 1 << 20
const MaxResponseBytes = 16 << 20

type Object = map[string]any

// Host is supplied by a trusted adapter after credential authentication. Its
// booleans are credential capabilities; registry state is rechecked in the DB.
type Host struct {
	Actor                                       core.ActorRef
	Read, Write, Admin                          bool
	SessionID, SessionProof, TargetSessionProof string
}

type Error struct{ Scope, Code string }

func (e *Error) Error() string       { return e.Scope + ":" + e.Code }
func bindingError(code string) error { return &Error{Scope: "binding", Code: code} }
func coreError(code string) error    { return &Error{Scope: "core", Code: code} }
func translate(err error) error {
	var e *core.Error
	if errors.As(err, &e) {
		return coreError(e.Kind)
	}
	return err
}

type Service struct {
	db        *storage.Store
	authority string
}

func New(db *storage.Store, authority string) *Service { return &Service{db: db, authority: authority} }

// Initialize is a one-time trusted installer operation, not participant API.
// It establishes the authority and operator identities before any user traffic.
func (s *Service) Initialize(ctx context.Context, operators []core.ActorRef) error {
	if s.authority == "" || len(operators) == 0 {
		return errors.New("authority and operator identities required")
	}
	for _, a := range operators {
		if err := contract.ValidateCore("ActorRef", actorObject(a)); err != nil {
			return err
		}
	}
	return s.db.Update(ctx, func(tx *storage.Tx) error {
		if _, err := tx.Get("settings", "authority"); err == nil {
			return bindingError("ALREADY_EXISTS")
		} else if !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		if err := put(tx, "settings", "authority", s.authority); err != nil {
			return err
		}
		for _, a := range operators {
			p := Object{"actor": actorObject(a), "display_name": a.ID, "state": "enabled", "revision": int64(1), "profile": Object{}}
			if err := insert(tx, "participants", actorKey(a), p); err != nil {
				return err
			}
			if err := put(tx, "operators", actorKey(a), true); err != nil {
				return err
			}
			if err := appendApplication(tx, "bootstrap", "participant", actorKey(a), 1, actorObject(a), p, nil); err != nil {
				return err
			}
		}
		return nil
	})
}

func actorObject(a core.ActorRef) Object { return Object{"kind": a.Kind, "id": a.ID} }
func taskObject(t core.TaskRef) Object {
	return Object{"authority_id": t.AuthorityID, "task_id": t.TaskID}
}
func key(parts ...string) string      { b, _ := json.Marshal(parts); return string(b) }
func actorKey(a core.ActorRef) string { return key(a.Kind, a.ID) }
func taskKey(t core.TaskRef) string   { return key(t.AuthorityID, t.TaskID) }
func recordKey(t core.TaskRef, assignment string) string {
	return key(t.AuthorityID, t.TaskID, assignment)
}
func str(v any) string    { s, _ := v.(string); return s }
func object(v any) Object { o, _ := v.(map[string]any); return o }
func number(v any) int64  { n, _ := jsonvalue.Integer(v); return n }
func equal(a, b any) bool { return jsonvalue.Equal(a, b) }
func stamp() string       { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }
func id() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return hex.EncodeToString(b[:]), nil
}
func parseObject(raw []byte) (Object, error) {
	v, e := jsonvalue.Parse(raw)
	if e != nil {
		return nil, e
	}
	o, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("expected object")
	}
	return o, nil
}
func get(tx *storage.Tx, bucket, k string) (Object, error) {
	raw, e := tx.Get(bucket, k)
	if e != nil {
		return nil, e
	}
	return parseObject(raw)
}
func put(tx *storage.Tx, bucket, k string, v any) error {
	raw, e := jsonvalue.Marshal(v)
	if e != nil {
		return e
	}
	return tx.Put(bucket, k, raw)
}
func insert(tx *storage.Tx, bucket, k string, v any) error {
	raw, e := jsonvalue.Marshal(v)
	if e != nil {
		return e
	}
	return tx.Insert(bucket, k, raw)
}
func tree(v any) (any, error) {
	b, e := jsonvalue.Marshal(v)
	if e != nil {
		return nil, e
	}
	return jsonvalue.Parse(b)
}
func checkSchema(name string, v any) error {
	t, e := tree(v)
	if e != nil {
		return e
	}
	return contract.ValidateBinding(name, t)
}
func decodeSnapshot(raw []byte) (core.Snapshot, error) {
	var s core.Snapshot
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	e := d.Decode(&s)
	return s, e
}
func (s *Service) checkAuthority(tx *storage.Tx) error {
	raw, e := tx.Get("settings", "authority")
	if e != nil {
		return e
	}
	var a string
	if e = json.Unmarshal(raw, &a); e != nil {
		return e
	}
	if a != s.authority {
		return bindingError("AUTHORITY_MISMATCH")
	}
	return nil
}
func (s *Service) participant(tx *storage.Tx, h Host, write bool) error {
	if e := s.checkAuthority(tx); e != nil {
		return e
	}
	p, e := get(tx, "participants", actorKey(h.Actor))
	if errors.Is(e, storage.ErrNotFound) {
		return bindingError("FORBIDDEN")
	}
	if e != nil {
		return e
	}
	if write && (!h.Write || str(p["state"]) != "enabled") {
		return bindingError("FORBIDDEN")
	}
	if !write && !h.Read {
		return bindingError("FORBIDDEN")
	}
	return nil
}
func required(tx *storage.Tx, bucket, k string) (Object, error) {
	o, e := get(tx, bucket, k)
	if errors.Is(e, storage.ErrNotFound) {
		return nil, bindingError("NOT_FOUND")
	}
	return o, e
}
func absent(tx *storage.Tx, bucket, k string) error {
	_, e := tx.Get(bucket, k)
	if e == nil {
		return bindingError("ALREADY_EXISTS")
	}
	if errors.Is(e, storage.ErrNotFound) {
		return nil
	}
	return e
}
func revision(o Object, want int64) error {
	if number(o["revision"]) != want {
		return bindingError("STALE_REVISION")
	}
	if want >= core.MaxInteger {
		return bindingError("INVALID_STATE")
	}
	return nil
}
func taskRef(o Object) core.TaskRef {
	return core.TaskRef{AuthorityID: str(o["authority_id"]), TaskID: str(o["task_id"])}
}
func proof(tx *storage.Tx, session Object, token string) error {
	b, e := base64.RawURLEncoding.DecodeString(token)
	if e != nil || len(b) != 32 {
		return bindingError("SESSION_PROOF_REQUIRED")
	}
	secret, e := get(tx, "session-secrets", str(session["session_id"]))
	if e != nil {
		return e
	}
	digest := sha256.Sum256(b)
	expected, e := hex.DecodeString(str(secret["digest"]))
	if e != nil {
		return fmt.Errorf("stored proof digest: %w", e)
	}
	if subtle.ConstantTimeCompare(expected, digest[:]) != 1 {
		return bindingError("SESSION_PROOF_REQUIRED")
	}
	return nil
}
func session(tx *storage.Tx, h Host, sid, project string, active bool) (Object, error) {
	if sid == "" || sid != h.SessionID {
		return nil, bindingError("SESSION_PROOF_REQUIRED")
	}
	sess, e := required(tx, "sessions", sid)
	if e != nil {
		return nil, e
	}
	if !equal(sess["actor"], actorObject(h.Actor)) || str(sess["project_id"]) != project {
		return nil, bindingError("SESSION_MISMATCH")
	}
	if e = proof(tx, sess, h.SessionProof); e != nil {
		return nil, e
	}
	if active && str(sess["state"]) != "active" {
		return nil, bindingError("SESSION_ENDED")
	}
	return sess, nil
}
func appendApplication(tx *storage.Tx, op, kind, entity string, rev int64, actor Object, after any, task *core.TaskRef) error {
	eid, e := id()
	if e != nil {
		return e
	}
	// Composite database keys can exceed the public Identifier limit even when
	// each component is valid. Keep the public key stable and bounded; consumers
	// use the separately carried ActorRef/TaskRef and after-image for identity.
	if utf8.RuneCountInString(entity) > 256 {
		digest := sha256.Sum256([]byte(entity))
		entity = "sha256:" + hex.EncodeToString(digest[:])
	}
	event := Object{"event_id": eid, "operation": op, "entity_type": kind, "entity_id": entity, "revision": rev, "actor": actor, "after": after, "removed": false}
	if task != nil {
		event["task"] = taskObject(*task)
	}
	if e = checkSchema("ApplicationEvent", event); e != nil {
		return fmt.Errorf("application event schema: %w", e)
	}
	raw, e := jsonvalue.Marshal(Object{"recorded_at": stamp(), "source": "application", "event": event})
	if e != nil {
		return e
	}
	_, e = tx.AppendEvent(eid, raw)
	return e
}

// Snapshot returns one trusted-host task view at a committed watermark. HTTP
// signed cursors and workspace query APIs are deliberately not implemented here.
func (s *Service) Snapshot(ctx context.Context, h Host, t core.TaskRef) (Object, int64, error) {
	var result Object
	var w int64
	err := s.db.View(ctx, func(tx *storage.Tx) error {
		if e := s.participant(tx, h, false); e != nil {
			return e
		}
		if t.AuthorityID != s.authority {
			return bindingError("AUTHORITY_MISMATCH")
		}
		snap, e := get(tx, "tasks", taskKey(t))
		if errors.Is(e, storage.ErrNotFound) {
			return coreError("TASK_NOT_FOUND")
		}
		if e != nil {
			return e
		}
		meta, e := get(tx, "metadata", taskKey(t))
		if e != nil {
			return e
		}
		result = Object{"snapshot": snap, "application": meta}
		raw, e := jsonvalue.Marshal(result)
		if e != nil {
			return e
		}
		if len(raw) > MaxResponseBytes {
			return bindingError("CAPACITY_LIMIT")
		}
		w, e = tx.Watermark()
		return e
	})
	return result, w, err
}

// Events is a trusted-host read helper for S2 recovery verification, not the S1
// public cursor binding. Each row is committed with its task and receipt.
func (s *Service) Events(ctx context.Context, h Host, after int64, limit int) ([]storage.Event, error) {
	var out []storage.Event
	e := s.db.View(ctx, func(tx *storage.Tx) error {
		if e := s.participant(tx, h, false); e != nil {
			return e
		}
		var e error
		out, e = tx.Events(after, limit)
		return e
	})
	return out, e
}
