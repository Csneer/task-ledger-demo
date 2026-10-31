package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	return s
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestPersistObjectsEventsAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks ?&.sqlite")
	s := testStore(t, path)
	ctx := testContext(t)
	if err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.Insert("tasks", "b", []byte("first")); err != nil {
			return err
		}
		if err := tx.Put("tasks", "b", []byte("updated")); err != nil {
			return err
		}
		if err := tx.Insert("tasks", "a", nil); err != nil {
			return err
		}
		if err := tx.Put("tasks", "discard", []byte("gone")); err != nil {
			return err
		}
		if err := tx.Delete("tasks", "discard"); err != nil {
			return err
		}
		for i := 1; i <= 2; i++ {
			pos, err := tx.AppendEvent(fmt.Sprintf("event-%d", i), []byte(strconv.Itoa(i)))
			if err != nil {
				return err
			}
			if pos != int64(i) {
				return fmt.Errorf("position = %d", pos)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := testStore(t, path)
	if err := reopened.View(ctx, func(tx *Tx) error {
		entries, err := tx.List("tasks")
		if err != nil {
			return err
		}
		if len(entries) != 2 || entries[0].Key != "a" || len(entries[0].Value) != 0 || entries[1].Key != "b" || string(entries[1].Value) != "updated" {
			return fmt.Errorf("unexpected entries: %+v", entries)
		}
		if _, err := tx.Get("tasks", "discard"); !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("deleted object: %v", err)
		}
		watermark, err := tx.Watermark()
		if err != nil || watermark != 2 {
			return fmt.Errorf("watermark %d: %w", watermark, err)
		}
		events, err := tx.Events(1, 1)
		if err != nil || len(events) != 1 || events[0].Position != 2 || events[0].ID != "event-2" || string(events[0].Body) != "2" {
			return fmt.Errorf("unexpected events %+v: %w", events, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRollbackOnCallbackFailureAndUniqueEventFailure(t *testing.T) {
	s := testStore(t, filepath.Join(t.TempDir(), "tasks.sqlite"))
	ctx := testContext(t)
	if err := s.Update(ctx, func(tx *Tx) error {
		_, err := tx.AppendEvent("existing", []byte("existing"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	want := errors.New("abort transaction")
	for _, cause := range []string{"callback", "event uniqueness"} {
		t.Run(cause, func(t *testing.T) {
			err := s.Update(ctx, func(tx *Tx) error {
				for _, bucket := range []string{"tasks", "receipts", "policies"} {
					if err := tx.Put(bucket, "new", []byte("partial")); err != nil {
						return err
					}
				}
				if _, err := tx.AppendEvent("new", []byte("partial")); err != nil {
					return err
				}
				if cause == "callback" {
					return want
				}
				_, err := tx.AppendEvent("existing", []byte("duplicate"))
				return err
			})
			if cause == "callback" && !errors.Is(err, want) || cause != "callback" && !errors.Is(err, ErrExists) {
				t.Fatalf("wrong error: %v", err)
			}
			if err := s.View(ctx, func(tx *Tx) error {
				for _, bucket := range []string{"tasks", "receipts", "policies"} {
					if _, err := tx.Get(bucket, "new"); !errors.Is(err, ErrNotFound) {
						return fmt.Errorf("partial %s survived: %v", bucket, err)
					}
				}
				watermark, err := tx.Watermark()
				if err != nil || watermark != 1 {
					return fmt.Errorf("partial event survived: watermark=%d error=%v", watermark, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReadOnlyClosedAndPanicRollback(t *testing.T) {
	s := testStore(t, filepath.Join(t.TempDir(), "tasks.sqlite"))
	ctx := testContext(t)
	var retained *Tx
	if err := s.View(ctx, func(tx *Tx) error {
		retained = tx
		for _, mutate := range []func() error{
			func() error { return tx.Put("b", "k", nil) },
			func() error { return tx.Insert("b", "k", nil) },
			func() error { return tx.Delete("b", "k") },
			func() error { _, err := tx.AppendEvent("e", nil); return err },
		} {
			if err := mutate(); !errors.Is(err, ErrReadOnly) {
				return fmt.Errorf("read-only write returned %v", err)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := retained.Get("b", "k"); !errors.Is(err, ErrTxClosed) {
		t.Fatalf("retained transaction: %v", err)
	}
	func() {
		defer func() {
			if recovered := recover(); recovered != "stop" {
				t.Fatalf("panic did not propagate: %v", recovered)
			}
		}()
		_ = s.Update(ctx, func(tx *Tx) error {
			if err := tx.Put("b", "k", []byte("partial")); err != nil {
				t.Fatal(err)
			}
			panic("stop")
		})
	}()
	if err := s.Update(ctx, func(tx *Tx) error {
		if _, err := tx.Get("b", "k"); !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("panic write survived: %v", err)
		}
		return tx.Insert("b", "k", []byte("after rollback"))
	}); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentStoresSerializeCompetingWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.sqlite")
	stores := []*Store{testStore(t, path), testStore(t, path)}
	ctx := testContext(t)
	start := make(chan struct{})
	results := make(chan error, 16)
	var workers sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			<-start
			results <- stores[i%2].Update(ctx, func(tx *Tx) error {
				if err := tx.Insert("claim", "task-1", []byte(strconv.Itoa(i))); err != nil {
					return err
				}
				_, err := tx.AppendEvent(fmt.Sprintf("claim-%d", i), []byte("winner"))
				return err
			})
		}(i)
	}
	close(start)
	workers.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, ErrExists) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
	if err := stores[0].View(ctx, func(tx *Tx) error {
		watermark, err := tx.Watermark()
		if err != nil || watermark != 1 {
			return fmt.Errorf("event count=%d error=%v", watermark, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestViewHasConsistentObjectsAndWatermarkAcrossConcurrentCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.sqlite")
	reader, writer := testStore(t, path), testStore(t, path)
	ctx := testContext(t)
	write := func(value string) error {
		return writer.Update(ctx, func(tx *Tx) error {
			if err := tx.Put("task", "1", []byte(value)); err != nil {
				return err
			}
			_, err := tx.AppendEvent("event-"+value, []byte(value))
			return err
		})
	}
	if err := write("before"); err != nil {
		t.Fatal(err)
	}
	if err := reader.View(ctx, func(tx *Tx) error {
		before, err := tx.Get("task", "1")
		if err != nil {
			return err
		}
		if err := write("after"); err != nil {
			return err
		}
		after, err := tx.Get("task", "1")
		if err != nil || string(before) != "before" || string(after) != "before" {
			return fmt.Errorf("inconsistent snapshot %q/%q: %v", before, after, err)
		}
		watermark, err := tx.Watermark()
		if err != nil || watermark != 1 {
			return fmt.Errorf("snapshot watermark=%d error=%v", watermark, err)
		}
		events, err := tx.Events(0, 10)
		if err != nil || len(events) != 1 || string(events[0].Body) != "before" {
			return fmt.Errorf("snapshot events=%+v error=%v", events, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := reader.View(ctx, func(tx *Tx) error {
		value, err := tx.Get("task", "1")
		if err != nil || string(value) != "after" {
			return fmt.Errorf("new snapshot=%q error=%v", value, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPragmasOnEveryPooledConnection(t *testing.T) {
	s := testStore(t, filepath.Join(t.TempDir(), "tasks.sqlite"))
	ctx := testContext(t)
	var conns []*sql.Conn
	defer func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	}()
	for i := 0; i < 4; i++ {
		conn, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		for pragma, want := range map[string]string{"journal_mode": "wal", "synchronous": "2", "foreign_keys": "1", "busy_timeout": "5000", "user_version": "1"} {
			var got string
			if err := conn.QueryRowContext(ctx, "PRAGMA "+pragma).Scan(&got); err != nil || got != want {
				t.Fatalf("connection %d %s=%q, want=%q, error=%v", i, pragma, got, want, err)
			}
		}
	}
}

func TestRejectUnsafeInitialization(t *testing.T) {
	for _, path := range []string{"", ":memory:", "file:test?mode=memory"} {
		if s, err := Open(path); err == nil {
			_ = s.Close()
			t.Fatalf("accepted %q", path)
		}
	}
	for _, setup := range []string{"PRAGMA user_version=99", "CREATE TABLE unrelated(value TEXT)"} {
		t.Run(setup, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "existing.sqlite")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(setup); err != nil {
				t.Fatal(err)
			}
			_ = db.Close()
			if s, err := Open(path); err == nil {
				_ = s.Close()
				t.Fatal("accepted unsupported database")
			}
			db, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var count int
			if err := db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name IN ('objects','events')").Scan(&count); err != nil || count != 0 {
				t.Fatalf("rejected database modified: count=%d err=%v", count, err)
			}
		})
	}
}

func TestCancelledContextRollsBackAndConnectionRemainsUsable(t *testing.T) {
	s := testStore(t, filepath.Join(t.TempDir(), "tasks.sqlite"))
	ctx, cancel := context.WithCancel(testContext(t))
	err := s.Update(ctx, func(tx *Tx) error {
		if err := tx.Put("tasks", "cancelled", []byte("partial")); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("commit after cancellation: %v", err)
	}
	if err := s.Update(testContext(t), func(tx *Tx) error {
		if _, err := tx.Get("tasks", "cancelled"); !errors.Is(err, ErrNotFound) {
			return fmt.Errorf("cancelled write survived: %v", err)
		}
		return tx.Put("tasks", "success", []byte("ok"))
	}); err != nil {
		t.Fatal(err)
	}
}

// These child-only exits are deliberately inside test code. Production storage
// contains no environment switch, crash hook, or fault bypass.
func TestStorageProcessHelper(t *testing.T) {
	mode := os.Getenv("LEDGER_STORAGE_TEST_CHILD")
	if mode == "" {
		return
	}
	s, err := Open(os.Getenv("LEDGER_STORAGE_TEST_DB"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := testContext(t)
	if mode == "compete" {
		start := os.Getenv("LEDGER_STORAGE_TEST_START")
		for {
			if _, err := os.Stat(start); err == nil {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			case <-time.After(time.Millisecond):
			}
		}
	}
	err = s.Update(ctx, func(tx *Tx) error {
		if err := tx.Insert("tasks", "claimed", []byte("actor")); err != nil {
			return err
		}
		if err := tx.Insert("receipts", "command", []byte("result")); err != nil {
			return err
		}
		if _, err := tx.AppendEvent("committed", []byte("event")); err != nil {
			return err
		}
		if mode == "before-commit" {
			os.Exit(73)
		}
		return nil
	})
	if mode == "compete" && errors.Is(err, ErrExists) {
		os.Exit(11)
	}
	if err != nil {
		t.Fatal(err)
	}
	if mode == "compete" {
		os.Exit(10)
	}
	// No Close and no deferred cleanup run: the commit has returned, but no
	// synthetic caller response has been delivered before the process exits.
	os.Exit(74)
}

func childCommand(ctx context.Context, path, mode, start string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStorageProcessHelper$", "-test.count=1")
	cmd.Env = append(os.Environ(), "LEDGER_STORAGE_TEST_CHILD="+mode, "LEDGER_STORAGE_TEST_DB="+path, "LEDGER_STORAGE_TEST_START="+start)
	return cmd
}

func TestSubprocessCrashBeforeAndAfterCommit(t *testing.T) {
	for _, mode := range []string{"before-commit", "after-commit"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tasks.sqlite")
			output, err := childCommand(testContext(t), path, mode, "").CombinedOutput()
			var exit *exec.ExitError
			wantExit := 73
			if mode == "after-commit" {
				wantExit = 74
			}
			if !errors.As(err, &exit) || exit.ExitCode() != wantExit {
				t.Fatalf("child exit %v: %s", err, output)
			}
			s := testStore(t, path)
			if err := s.View(testContext(t), func(tx *Tx) error {
				want := 0
				if mode == "after-commit" {
					want = 1
				}
				for _, bucket := range []string{"tasks", "receipts"} {
					items, err := tx.List(bucket)
					if err != nil || len(items) != want {
						return fmt.Errorf("%s records=%d want=%d error=%v", bucket, len(items), want, err)
					}
				}
				events, err := tx.Events(0, 10)
				if err != nil || len(events) != want {
					return fmt.Errorf("events=%d want=%d error=%v", len(events), want, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSubprocessCompetingWriters(t *testing.T) {
	dir := t.TempDir()
	path, start := filepath.Join(dir, "tasks.sqlite"), filepath.Join(dir, "start")
	ctx := testContext(t)
	cmds := []*exec.Cmd{childCommand(ctx, path, "compete", start), childCommand(ctx, path, "compete", start)}
	output := make([]strings.Builder, len(cmds))
	for i, cmd := range cmds {
		cmd.Stdout, cmd.Stderr = &output[i], &output[i]
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(start, []byte("start"), 0600); err != nil {
		t.Fatal(err)
	}
	winners, conflicts := 0, 0
	for i, cmd := range cmds {
		var exit *exec.ExitError
		if err := cmd.Wait(); !errors.As(err, &exit) {
			t.Fatalf("child %d: %v %s", i, err, output[i].String())
		}
		switch exit.ExitCode() {
		case 10:
			winners++
		case 11:
			conflicts++
		default:
			t.Fatalf("child %d exit %d: %s", i, exit.ExitCode(), output[i].String())
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("winners=%d conflicts=%d", winners, conflicts)
	}
	s := testStore(t, path)
	if err := s.View(ctx, func(tx *Tx) error {
		events, err := tx.Events(0, 10)
		if err != nil || len(events) != 1 {
			return fmt.Errorf("committed events=%d error=%v", len(events), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
