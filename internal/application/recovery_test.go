package application

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Csneer/task-ledger-demo/internal/storage"
)

// Only this test executable reads these variables. Production has no crash hook.
func TestApplicationRecoveryProcessHelper(t *testing.T) {
	if os.Getenv("LEDGER_APPLICATION_RECOVERY_CHILD") != "claim" {
		return
	}
	db, err := storage.Open(os.Getenv("LEDGER_APPLICATION_RECOVERY_DB"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(db, "personal")
	h := sessionHost(t, s, agentA, "crash-session", 7)
	mustCommand(t, s, h, commandRequest("task.claim", "crash-claim", 1, Object{}, Object{"session_id": h.SessionID}))
	// Terminate after Execute commits, without Store.Close or deferred cleanup.
	os.Exit(76)
}

func TestApplicationCommittedClaimSurvivesProcessExit(t *testing.T) {
	s, db, path := testService(t)
	create(t, s)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestApplicationRecoveryProcessHelper$")
	child.Env = append(os.Environ(), "LEDGER_APPLICATION_RECOVERY_CHILD=claim", "LEDGER_APPLICATION_RECOVERY_DB="+path)
	output, err := child.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 76 {
		t.Fatalf("child did not reach committed exit: %v; %s", err, output)
	}
	reopened, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	s = New(reopened, "personal")
	before, watermark := current(t, s)
	binding := object(object(before["application"])["execution_binding"])
	task := object(object(before["snapshot"])["task"])
	if task["state"] != "claimed" || binding["session_id"] != "crash-session" || binding["assignment_id"] != task["current_assignment_id"] {
		t.Fatalf("lost task or binding: %v", before)
	}
	req := commandRequest("task.claim", "crash-claim", 1, Object{}, Object{"session_id": "crash-session"})
	replay := mustCommand(t, s, host(agentA), req)
	if object(replay["result"])["replayed"] != true || resultTask(replay)["current_assignment_id"] != task["current_assignment_id"] {
		t.Fatal("durable receipt did not replay original assignment")
	}
	after, afterWatermark := current(t, s)
	if !equal(before, after) || watermark != afterWatermark {
		t.Fatal("replay mutated recovered state")
	}
	assertRecoveryClaimEvents(t, s, "crash-claim")
}

func TestApplicationReceiptFailureRollsBackClaimFacts(t *testing.T) {
	s, db, path := testService(t)
	h := sessionHost(t, s, agentA, "rollback-session", 8)
	create(t, s)
	before, watermark := current(t, s)
	// Fail the final receipt insert after normal Execute has written its candidate
	// task, binding, occupancy and events. This is a test-only database fixture.
	fixture, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	_, err = fixture.Exec(`CREATE TRIGGER reject_command_receipt BEFORE INSERT ON objects
 WHEN NEW.bucket = 'command-receipts'
 BEGIN SELECT RAISE(ABORT, 'test receipt failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	req := commandRequest("task.claim", "rollback-claim", 1, Object{}, Object{"session_id": h.SessionID})
	_, err = s.Execute(context.Background(), h, bytesOf(t, req))
	if err == nil || !strings.Contains(err.Error(), "test receipt failure") {
		t.Fatalf("did not reach receipt failure: %v", err)
	}
	after, afterWatermark := current(t, s)
	if !equal(before, after) || watermark != afterWatermark {
		t.Fatal("receipt failure left task, metadata or events")
	}
	err = db.View(context.Background(), func(tx *storage.Tx) error {
		for _, bucket := range []string{"bindings", "session-occupancy"} {
			rows, err := tx.List(bucket)
			if err != nil {
				return err
			}
			if len(rows) != 0 {
				t.Errorf("receipt failure left %s rows: %v", bucket, rows)
			}
		}
		_, err := tx.Get("command-receipts", key("personal", agentA.Kind, agentA.ID, "rollback-claim"))
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("receipt persisted or unexpected error: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.Exec("DROP TRIGGER reject_command_receipt"); err != nil {
		t.Fatal(err)
	}
	retry := mustCommand(t, s, h, req)
	if object(retry["result"])["replayed"] != false {
		t.Fatal("failed command occupied receipt")
	}
	assertRecoveryClaimEvents(t, s, "rollback-claim")
}

func assertRecoveryClaimEvents(t *testing.T, s *Service, commandID string) {
	t.Helper()
	rows, err := s.Events(context.Background(), host(owner), 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	coreCount, bindingCount := 0, 0
	for _, row := range rows {
		envelope, err := parseObject(row.Body)
		if err != nil {
			t.Fatal(err)
		}
		event := object(envelope["event"])
		if envelope["source"] == "core" && event["command_id"] == commandID {
			coreCount++
		}
		if envelope["source"] == "application" && event["operation"] == "task.claim" && event["entity_type"] == "binding" {
			bindingCount++
		}
	}
	if coreCount != 1 || bindingCount != 1 {
		t.Fatalf("claim events core=%d binding=%d; want exactly one each", coreCount, bindingCount)
	}
}
