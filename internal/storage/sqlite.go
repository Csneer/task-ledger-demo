package storage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	ErrNotFound = errors.New("storage: record not found")
	ErrExists   = errors.New("storage: record already exists")
	ErrReadOnly = errors.New("storage: read-only transaction")
	ErrTxClosed = errors.New("storage: transaction is closed")
	ErrCapacity = errors.New("storage: read byte budget exceeded")
)

// Store is a file-backed SQLite database. All connections use WAL, FULL
// synchronization, foreign keys, and a five-second SQLite busy timeout.
type Store struct{ db *sql.DB }

// Tx is valid only during its Update or View callback. Callbacks must not retain
// the Tx, use it concurrently, or recursively start a transaction on the Store.
type Tx struct {
	conn     *sql.Conn
	ctx      context.Context
	writable bool
	active   bool
}

type Entry struct {
	Key   string
	Value []byte
}

type Event struct {
	Position int64
	ID       string
	Body     []byte
}

// Open opens a filesystem path, not a SQLite URI. The parent directory must
// exist. Memory databases and implicit temporary databases are not supported.
func Open(path string) (*Store, error) {
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file:") {
		return nil, errors.New("storage: a filesystem database path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("storage path: %w", err)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs)}
	q := url.Values{}
	for _, pragma := range []string{"busy_timeout(5000)", "journal_mode(WAL)", "synchronous(FULL)", "foreign_keys(1)"} {
		q.Add("_pragma", pragma)
	}
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open storage: %w", err)
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	s := &Store{db: db}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.initialize(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Update reserves SQLite's writer slot before any callback reads, serializing
// read-check-write operations across connections, Stores, and processes.
func (s *Store) Update(ctx context.Context, fn func(*Tx) error) error {
	return s.transact(ctx, true, fn)
}

// View gives all callback reads one SQLite snapshot. No write methods are
// available in this transaction. The snapshot is fixed by its first read.
func (s *Store) View(ctx context.Context, fn func(*Tx) error) error {
	return s.transact(ctx, false, fn)
}

func (s *Store) transact(ctx context.Context, writable bool, fn func(*Tx) error) (err error) {
	if fn == nil {
		return errors.New("storage: nil transaction callback")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, conn.Close()) }()
	begin := "BEGIN"
	if writable {
		begin = "BEGIN IMMEDIATE"
	}
	if _, err = conn.ExecContext(ctx, begin); err != nil {
		return err
	}
	tx := &Tx{conn: conn, ctx: ctx, writable: writable, active: true}
	committed := false
	defer func() {
		tx.active = false
		if committed {
			return
		}
		// The caller's context may already be cancelled. Rollback must still run,
		// including when the callback panics, before this connection is reused.
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, rollbackErr := conn.ExecContext(cleanup, "ROLLBACK"); rollbackErr != nil {
			err = errors.Join(err, fmt.Errorf("rollback storage transaction: %w", rollbackErr))
			// Never return a connection with an uncertain transaction to the pool.
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	if err = fn(tx); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}

// initialize retries only startup: concurrent connection PRAGMAs (notably WAL)
// can return SQLITE_BUSY without waiting for busy_timeout. Migration is atomic
// and version-checked, so retrying it is safe; application callbacks never retry.
func (s *Store) initialize(ctx context.Context) error {
	for {
		err := s.migrate(ctx)
		if err == nil {
			return nil
		}
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_BUSY {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return errors.Join(err, ctx.Err())
		}
	}
}

func (s *Store) migrate(ctx context.Context) error {
	return s.Update(ctx, func(tx *Tx) error {
		var version int
		if err := tx.conn.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		if version == 1 {
			return nil
		}
		if version != 0 {
			return fmt.Errorf("storage: unsupported schema version %d", version)
		}
		var tables int
		if err := tx.conn.QueryRowContext(ctx,
			"SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'").Scan(&tables); err != nil {
			return err
		}
		if tables != 0 {
			return errors.New("storage: refusing to initialize a nonempty unversioned database")
		}
		for _, statement := range []string{
			`CREATE TABLE objects (
				bucket TEXT NOT NULL,
				key TEXT NOT NULL,
				value BLOB NOT NULL,
				PRIMARY KEY (bucket, key)
			) WITHOUT ROWID`,
			`CREATE TABLE events (
				position INTEGER PRIMARY KEY AUTOINCREMENT,
				event_id TEXT NOT NULL UNIQUE,
				body BLOB NOT NULL
			)`,
			"PRAGMA user_version = 1",
		} {
			if _, err := tx.conn.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
		return nil
	})
}

func (tx *Tx) check(write bool) error {
	if !tx.active {
		return ErrTxClosed
	}
	if write && !tx.writable {
		return ErrReadOnly
	}
	return nil
}

func (tx *Tx) Get(bucket, key string) ([]byte, error) {
	if err := tx.check(false); err != nil {
		return nil, err
	}
	var value []byte
	err := tx.conn.QueryRowContext(tx.ctx, "SELECT value FROM objects WHERE bucket = ? AND key = ?", bucket, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return value, err
}

func (tx *Tx) Put(bucket, key string, value []byte) error {
	if err := tx.check(true); err != nil {
		return err
	}
	_, err := tx.conn.ExecContext(tx.ctx, `INSERT INTO objects(bucket,key,value) VALUES(?,?,?)
		ON CONFLICT(bucket,key) DO UPDATE SET value=excluded.value`, bucket, key, blob(value))
	return err
}

func (tx *Tx) Insert(bucket, key string, value []byte) error {
	if err := tx.check(true); err != nil {
		return err
	}
	_, err := tx.conn.ExecContext(tx.ctx, "INSERT INTO objects(bucket,key,value) VALUES(?,?,?)", bucket, key, blob(value))
	return uniqueError(err)
}

// Delete is idempotent. It cannot delete events or successful receipt keys
// unless the application deliberately chooses to delete its own object record.
func (tx *Tx) Delete(bucket, key string) error {
	if err := tx.check(true); err != nil {
		return err
	}
	_, err := tx.conn.ExecContext(tx.ctx, "DELETE FROM objects WHERE bucket = ? AND key = ?", bucket, key)
	return err
}

func (tx *Tx) List(bucket string) ([]Entry, error) {
	return tx.list(bucket, -1)
}

// ListLimit reads at most limit objects in key order. Snapshot callers can
// fetch one extra row to reject an over-capacity collection without loading it.
func (tx *Tx) ListLimit(bucket string, limit int) ([]Entry, error) {
	if limit <= 0 {
		return nil, errors.New("storage: invalid object limit")
	}
	return tx.list(bucket, limit)
}

func (tx *Tx) list(bucket string, limit int) ([]Entry, error) {
	if err := tx.check(false); err != nil {
		return nil, err
	}
	rows, err := tx.conn.QueryContext(tx.ctx, "SELECT key,value FROM objects WHERE bucket = ? ORDER BY key LIMIT ?", bucket, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []Entry{}
	for rows.Next() {
		var item Entry
		if err := rows.Scan(&item.Key, &item.Value); err != nil {
			return nil, err
		}
		entries = append(entries, item)
	}
	return entries, rows.Err()
}

func (tx *Tx) AppendEvent(eventID string, body []byte) (int64, error) {
	if err := tx.check(true); err != nil {
		return 0, err
	}
	if eventID == "" {
		return 0, errors.New("storage: event ID is empty")
	}
	result, err := tx.conn.ExecContext(tx.ctx, "INSERT INTO events(event_id,body) VALUES(?,?)", eventID, blob(body))
	if err != nil {
		return 0, uniqueError(err)
	}
	return result.LastInsertId()
}

// Events returns at most limit entries after a committed position. Call it in
// the same View as Watermark when the caller needs a stable upper bound.
func (tx *Tx) Events(after int64, limit int) ([]Event, error) {
	if err := tx.check(false); err != nil {
		return nil, err
	}
	if after < 0 || limit <= 0 {
		return nil, errors.New("storage: invalid event range")
	}
	rows, err := tx.conn.QueryContext(tx.ctx, "SELECT position,event_id,body FROM events WHERE position > ? ORDER BY position LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var item Event
		if err := rows.Scan(&item.Position, &item.ID, &item.Body); err != nil {
			return nil, err
		}
		events = append(events, item)
	}
	return events, rows.Err()
}

func (tx *Tx) Watermark() (int64, error) {
	if err := tx.check(false); err != nil {
		return 0, err
	}
	var position int64
	err := tx.conn.QueryRowContext(tx.ctx, "SELECT coalesce(max(position),0) FROM events").Scan(&position)
	return position, err
}

func blob(value []byte) []byte {
	if value == nil {
		return []byte{}
	}
	return value
}

func uniqueError(err error) error {
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && (sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY || sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE) {
		return errors.Join(ErrExists, err)
	}
	return err
}

// GetBounded checks the BLOB length before loading it. maxBytes counts stored
// value bytes; callers separately enforce encoded response overhead.
func (tx *Tx) GetBounded(bucket, key string, maxBytes int) ([]byte, error) {
	if err := tx.check(false); err != nil {
		return nil, err
	}
	if maxBytes < 0 {
		return nil, errors.New("storage: invalid byte budget")
	}
	var size int64
	err := tx.conn.QueryRowContext(tx.ctx, "SELECT length(value) FROM objects WHERE bucket = ? AND key = ?", bucket, key).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if size > int64(maxBytes) {
		return nil, ErrCapacity
	}
	return tx.Get(bucket, key)
}

// ListBounded reads at most limit entries, refusing the entire result before
// loading BLOBs if their combined size exceeds maxBytes. The transaction keeps
// the metadata check and subsequent reads on the same snapshot.
func (tx *Tx) ListBounded(bucket string, limit, maxBytes int) ([]Entry, error) {
	if err := tx.check(false); err != nil {
		return nil, err
	}
	if limit <= 0 || maxBytes < 0 {
		return nil, errors.New("storage: invalid bounded range")
	}
	rows, err := tx.conn.QueryContext(tx.ctx, "SELECT key,length(value) FROM objects WHERE bucket = ? ORDER BY key LIMIT ?", bucket, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []Entry{}
	remaining := int64(maxBytes)
	for rows.Next() {
		var entry Entry
		var size int64
		if err := rows.Scan(&entry.Key, &size); err != nil {
			return nil, err
		}
		if size > remaining {
			return nil, ErrCapacity
		}
		remaining -= size
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range entries {
		entries[i].Value, err = tx.Get(bucket, entries[i].Key)
		if err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// EventsBounded applies a combined body-byte budget before loading event BLOBs.
// Event selection and order match Events; metadata and JSON overhead are not
// included in this storage budget.
func (tx *Tx) EventsBounded(after int64, limit, maxBytes int) ([]Event, error) {
	if err := tx.check(false); err != nil {
		return nil, err
	}
	if after < 0 || limit <= 0 || maxBytes < 0 {
		return nil, errors.New("storage: invalid bounded event range")
	}
	rows, err := tx.conn.QueryContext(tx.ctx, "SELECT position,event_id,length(body) FROM events WHERE position > ? ORDER BY position LIMIT ?", after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	remaining := int64(maxBytes)
	for rows.Next() {
		var event Event
		var size int64
		if err := rows.Scan(&event.Position, &event.ID, &size); err != nil {
			return nil, err
		}
		if size > remaining {
			return nil, ErrCapacity
		}
		remaining -= size
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range events {
		if err := tx.conn.QueryRowContext(tx.ctx, "SELECT body FROM events WHERE position = ?", events[i].Position).Scan(&events[i].Body); err != nil {
			return nil, err
		}
	}
	return events, nil
}
