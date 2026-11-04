package pocstore

import (
	"database/sql/driver"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"modernc.org/sqlite"
)

// The helper is run in a separate process. os.Exit(137) deliberately bypasses
// Close/Rollback to model abrupt process death without contacting Telegram.
func TestCrashProcessHelper(t *testing.T) {
	stage := os.Getenv("POCSTORE_CRASH_STAGE")
	if stage == "" {
		return
	}
	if stage == "during" {
		if err := sqlite.RegisterScalarFunction("poc_crash", 0, func(*sqlite.FunctionContext, []driver.Value) (driver.Value, error) { os.Exit(137); return nil, nil }); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(os.Getenv("POCSTORE_CRASH_PATH"))
	if err != nil {
		t.Fatal(err)
	}
	if stage == "during" {
		if _, err = s.db.Exec(`CREATE TEMP TRIGGER crash_in_cursor BEFORE INSERT ON main.poc_records WHEN new.kind='cursor' BEGIN SELECT poc_crash(); END`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{message(42, "durable data")}, Cursor: &engine.Cursor{Peer: peer, OldestID: 42, NewestID: 42}}); err != nil {
		t.Fatal(err)
	}
	os.Exit(137) // after Apply commit, before a checkpoint write
}

func TestCrashBeforeAndAfterApplyCommit(t *testing.T) {
	for _, stage := range []string{"during", "after"} {
		t.Run(stage, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "crash.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = s.SaveUpdatesState(ctx, "primary", engine.UpdatesState{Pts: 10}); err != nil {
				t.Fatal(err)
			}
			s.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCrashProcessHelper$")
			cmd.Env = append(os.Environ(), "POCSTORE_CRASH_STAGE="+stage, "POCSTORE_CRASH_PATH="+path)
			output, err := cmd.CombinedOutput()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 137 {
				t.Fatalf("child did not crash at expected stage: %v %s", err, output)
			}
			s, err = Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, exists, err := s.Message(ctx, message(42, "").Key)
			if err != nil || exists != (stage == "after") {
				t.Fatalf("crash atomicity: exists=%v error=%v", exists, err)
			}
			_, cursorExists, err := s.Cursor(ctx, "primary", peer)
			if err != nil || cursorExists != exists {
				t.Fatalf("page/cursor torn: %v %v", cursorExists, err)
			}
			st, ok, err := s.LoadUpdatesState(ctx, "primary")
			if err != nil || !ok || st.Pts != 10 {
				t.Fatalf("checkpoint ahead of data: %+v %v %v", st, ok, err)
			}
			result := apply(t, s, engine.Batch{Messages: []engine.Message{message(42, "durable data")}, Cursor: &engine.Cursor{Peer: peer, OldestID: 42, NewestID: 42}})
			if stage == "after" {
				noEvents(t, result)
			} else if len(result.Inserted) != 1 {
				t.Fatalf("missing replay: %+v", result)
			}
			noEvents(t, apply(t, s, engine.Batch{Messages: []engine.Message{message(42, "durable data")}}))
			if err = s.SaveUpdatesState(ctx, "primary", engine.UpdatesState{Pts: 11}); err != nil {
				t.Fatal(err)
			}
			st, _, err = s.LoadUpdatesState(ctx, "primary")
			if err != nil || st.Pts != 11 {
				t.Fatalf("final checkpoint: %+v %v", st, err)
			}
		})
	}
}
