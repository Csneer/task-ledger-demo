package transport

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Csneer/task-ledger-demo/internal/application"
	"github.com/Csneer/task-ledger-demo/internal/contract"
	"github.com/Csneer/task-ledger-demo/internal/core"
	"github.com/Csneer/task-ledger-demo/internal/jsonvalue"
)

// API accepts authenticated host context. Implementations must validate input
// and enforce response capacity before committing commands or actions.
type API interface {
	Execute(context.Context, application.Host, []byte) (application.Object, error)
	Action(context.Context, application.Host, []byte) (application.Object, error)
	Query(context.Context, application.Host, []byte) (application.Object, error)
}

// Credential contains only a SHA-256 digest of the 32 decoded random bytes in a
// base64url Bearer token. It never stores the token itself. Capabilities are
// independent; admin alone does not grant write or read access.
type Credential struct {
	Digest string        `json:"digest"`
	Actor  core.ActorRef `json:"actor"`
	Read   bool          `json:"read"`
	Write  bool          `json:"write"`
	Admin  bool          `json:"admin"`
}

type handler struct {
	api         API
	credentials map[[32]byte]application.Host
}

// New copies a fixed credential roster. Replacing the handler (normally by
// restarting the daemon) applies revocation; existing handlers do not reload it.
// Only Actor credentials are supported; dispatch runners belong to S4.
func New(api API, credentials []Credential) (http.Handler, error) {
	if api == nil {
		return nil, errors.New("transport: application API required")
	}
	h := &handler{api: api, credentials: make(map[[32]byte]application.Host, len(credentials))}
	for i, c := range credentials {
		digest, err := hex.DecodeString(c.Digest)
		if err != nil || len(digest) != sha256.Size || c.Digest != strings.ToLower(c.Digest) {
			return nil, fmt.Errorf("transport: credential %d requires a lowercase SHA-256 digest", i)
		}
		if err := contract.ValidateCore("ActorRef", map[string]any{"kind": c.Actor.Kind, "id": c.Actor.ID}); err != nil {
			return nil, fmt.Errorf("transport: credential %d has invalid actor", i)
		}
		var key [32]byte
		copy(key[:], digest)
		if _, exists := h.credentials[key]; exists {
			return nil, fmt.Errorf("transport: duplicate credential digest at index %d", i)
		}
		h.credentials[key] = application.Host{Actor: c.Actor, Read: c.Read, Write: c.Write, Admin: c.Admin}
	}
	return h, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var call func(context.Context, application.Host, []byte) (application.Object, error)
	switch r.URL.Path {
	case "/v1/commands":
		call = h.api.Execute
	case "/v1/actions":
		call = h.api.Action
	case "/v1/queries":
		call = h.api.Query
	default:
		writeError(w, http.StatusNotFound, "binding", "NOT_FOUND")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "binding", "INVALID_MESSAGE")
		return
	}
	host, ok := h.authenticate(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeError(w, http.StatusUnauthorized, "binding", "UNAUTHENTICATED")
		return
	}
	for name, dest := range map[string]*string{
		"X-Ledger-Session":              &host.SessionID,
		"X-Ledger-Session-Proof":        &host.SessionProof,
		"X-Ledger-Target-Session-Proof": &host.TargetSessionProof,
	} {
		values := r.Header.Values(name)
		if len(values) > 1 {
			writeError(w, http.StatusBadRequest, "binding", "INVALID_MESSAGE")
			return
		}
		if len(values) == 1 {
			*dest = values[0]
		}
	}
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if len(r.Header.Values("Content-Type")) != 1 || err != nil || media != "application/json" ||
		(params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) || len(params) > 1 ||
		(len(params) == 1 && params["charset"] == "") {
		writeError(w, http.StatusUnsupportedMediaType, "binding", "INVALID_MESSAGE")
		return
	}
	if r.ContentLength > application.MaxRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "binding", "CAPACITY_LIMIT")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, application.MaxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "binding", "CAPACITY_LIMIT")
		} else {
			writeError(w, http.StatusBadRequest, "binding", "INVALID_MESSAGE")
		}
		return
	}
	result, err := call(r.Context(), host, body)
	if err != nil {
		writeApplicationError(w, err)
		return
	}
	encoded, err := jsonvalue.Marshal(result)
	if err != nil || len(encoded) > application.MaxResponseBytes {
		// The application guarantees this bound before commit. A violated API
		// invariant is an unknown-result 500, not a false not-committed 507.
		writeError(w, http.StatusInternalServerError, "binding", "INTERNAL_ERROR")
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func (h *handler) authenticate(r *http.Request) (application.Host, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 {
		return application.Host{}, false
	}
	scheme, token, ok := strings.Cut(values[0], " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") || len(token) != 43 {
		return application.Host{}, false
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(secret) != 32 {
		return application.Host{}, false
	}
	host, ok := h.credentials[sha256.Sum256(secret)]
	return host, ok
}

var bindingStatuses = map[string]int{
	"INVALID_MESSAGE": 400, "UNSUPPORTED_BINDING": 400, "UNSUPPORTED_OPERATION": 400,
	"UNAUTHENTICATED": 401, "FORBIDDEN": 403, "AUTHORITY_MISMATCH": 400,
	"NOT_FOUND": 404, "ALREADY_EXISTS": 409, "STALE_REVISION": 409,
	"INVALID_STATE": 409, "IDEMPOTENCY_CONFLICT": 409, "SESSION_PROOF_REQUIRED": 403,
	"SESSION_ENDED": 409, "SESSION_BUSY": 409, "SESSION_MISMATCH": 403,
	"PROFILE_UNAVAILABLE": 409, "WAKE_IN_PROGRESS": 409, "INVALID_CURSOR": 400,
	"CURSOR_RESET_REQUIRED": 409, "CAPACITY_LIMIT": 507, "OVERLOADED": 503, "INTERNAL_ERROR": 500,
}
var coreStatuses = map[string]int{
	"INVALID_MESSAGE": 400, "UNSUPPORTED_VERSION": 400, "UNSUPPORTED_OPERATION": 400,
	"NOT_ADMITTED": 403, "TASK_EXISTS": 409, "TASK_NOT_FOUND": 404,
	"STALE_REVISION": 409, "STALE_ASSIGNMENT": 409, "ROLE_MISMATCH": 403,
	"INVALID_STATE": 409, "INVALID_BASIS": 409, "IDEMPOTENCY_CONFLICT": 409,
}

func writeApplicationError(w http.ResponseWriter, err error) {
	var e *application.Error
	if errors.As(err, &e) {
		statuses := bindingStatuses
		if e.Scope == "core" {
			statuses = coreStatuses
		} else if e.Scope != "binding" {
			writeError(w, 500, "binding", "INTERNAL_ERROR")
			return
		}
		if status, ok := statuses[e.Code]; ok {
			writeError(w, status, e.Scope, e.Code)
			return
		}
	}
	writeError(w, http.StatusInternalServerError, "binding", "INTERNAL_ERROR")
}

func writeError(w http.ResponseWriter, status int, scope, code string) {
	body := map[string]any{"scope": scope, "code": code}
	if scope == "core" {
		body = map[string]any{"scope": "core", "error": map[string]any{"protocol_version": core.Version, "kind": code}}
	}
	encoded, _ := json.Marshal(map[string]any{"binding_version": application.BindingVersion, "error": body})
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}
