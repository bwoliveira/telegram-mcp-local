package gotdengine

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

type forbiddenPanelStatusStore struct{ engine.Persister }

func (forbiddenPanelStatusStore) ReadStatus(context.Context, engine.AccountID) (engine.HistoryProgress, engine.UpdatesStatus, error) {
	panic("LightStatus must not read the store")
}

func TestDiagnosticsDoesNotCountMessagesToReadConnection(t *testing.T) {
	e := &Engine{
		cfg:     engine.Config{Store: forbiddenPanelStatusStore{}, Session: &memorySession{}, Clock: time.Now},
		status:  engine.Status{State: engine.StateConnected},
		account: engine.Account{Authenticated: true},
	}
	diagnostics, err := e.Diagnostics(context.Background())
	if err != nil || !diagnostics.Connected || !diagnostics.Authorized {
		t.Fatalf("diagnostics: %+v %v", diagnostics, err)
	}
}

func TestLightStatusUsesOnlyInMemoryLifecycle(t *testing.T) {
	want := engine.Status{
		State: engine.StateConnected, Mode: engine.ModeReadOnly,
		Since: time.Unix(123, 0), LastError: "lifecycle error", Restarts: 2,
		PausedFor: "test", Updates: engine.UpdatesStatus{Pts: 7},
		History: engine.HistoryProgress{Messages: 42},
	}
	// Unimplemented persistence methods and nil transport/session dependencies
	// fail immediately if this read touches anything beyond lifecycle memory.
	e := &Engine{cfg: engine.Config{Store: forbiddenPanelStatusStore{}}, status: want}
	reader, ok := any(e).(interface{ LightStatus() engine.Status })
	if !ok {
		t.Fatal("engine does not expose optional LightStatus")
	}
	for _, flags := range []struct{ held, recovering bool }{{}, {true, false}, {false, true}, {true, true}} {
		e.held.Store(flags.held)
		e.recovering.Store(flags.recovering)
		expected := want
		expected.Updates.Held = flags.held || flags.recovering
		if got := reader.LightStatus(); !reflect.DeepEqual(got, expected) {
			t.Fatalf("LightStatus() = %+v, want %+v", got, expected)
		}
	}
	e.status.Updates.Held = true
	e.held.Store(false)
	e.recovering.Store(false)
	if !reader.LightStatus().Updates.Held {
		t.Fatal("in-memory checkpoint hold was lost")
	}
}
