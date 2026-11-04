package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func TestReadStatusFakeContract(t *testing.T) {
	for _, mode := range []engine.Mode{engine.ModeReadOnly, engine.ModeReadWrite} {
		t.Run(string(mode), func(t *testing.T) {
			ctrl, err := fake.New(engine.Config{Mode: mode})
			if err != nil {
				t.Fatal(err)
			}
			s := New(Config{Engine: ctrl, BaseURL: "http://127.0.0.1:9999"})
			result := toolCall(t, s, "telegram_status", nil)
			if result["isError"] != false {
				t.Fatal(result)
			}
			report := toolBody(t, result)
			for key, want := range map[string]any{
				"version": Version, "endpoint": "http://127.0.0.1:9999/mcp", "mode": string(mode),
				"ready": true, "logged_out": false, "error": false, "paused": false,
				"secret_chats": "unsupported", "index_verified": false,
			} {
				if report[key] != want {
					t.Errorf("%s = %#v, want %#v", key, report[key], want)
				}
			}
			gateway := report["gateway"].(map[string]any)
			if gateway["version"] != report["version"] || gateway["endpoint"] != report["endpoint"] {
				t.Fatalf("gateway diverges: %v", report)
			}
			for _, key := range []string{"sync", "engine", "account", "transcription", "webhooks", "problems", "index_note"} {
				if _, ok := report[key]; !ok {
					t.Errorf("missing existing field %s", key)
				}
			}
			s.SetBaseURL("http://127.0.0.1:10000")
			if got := toolBody(t, toolCall(t, s, "telegram_status", nil))["endpoint"]; got != "http://127.0.0.1:10000/mcp" {
				t.Fatalf("stale endpoint: %v", got)
			}
			if got := ctrl.(*fake.Controller).Writes(); len(got) != 0 {
				t.Fatalf("status wrote to Telegram: %v", got)
			}
		})
	}
}

// Override only status/diagnostics: all other behavior remains the real fake.
type readStatusEngine struct {
	engine.Engine
	snapshot       engine.Status
	diagnostics    engine.Diagnostics
	diagnosticsErr error
}

func (e readStatusEngine) Status() engine.Status { return e.snapshot }
func (e readStatusEngine) Diagnostics(context.Context) (engine.Diagnostics, error) {
	return e.diagnostics, e.diagnosticsErr
}

func TestReadStatusLifecycleAndHealth(t *testing.T) {
	ctx := context.Background()
	for _, state := range []engine.ConnState{engine.StateConnected, engine.StatePaused, engine.StateLoggedOut} {
		t.Run(string(state), func(t *testing.T) {
			ctrl, err := fake.New(engine.Config{})
			if err != nil {
				t.Fatal(err)
			}
			switch state {
			case engine.StatePaused:
				err = ctrl.Disconnect(ctx, "user request")
			case engine.StateLoggedOut:
				err = ctrl.LogOut(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			s := New(Config{Engine: ctrl})
			report := toolBody(t, toolCall(t, s, "telegram_status", nil))
			for key, want := range map[string]bool{"ready": state == engine.StateConnected, "paused": state == engine.StatePaused, "logged_out": state == engine.StateLoggedOut, "error": false} {
				if report[key] != want {
					t.Errorf("%s = %v, want %v", key, report[key], want)
				}
			}
			health := toolBody(t, toolCall(t, s, "health", map[string]any{"max_silence_hours": 0.5}))
			want := "warn"
			if state == engine.StateLoggedOut {
				want = "fail"
			}
			if health["status"] != want {
				t.Errorf("health = %v, want %s", health, want)
			}
			for _, key := range []string{"status", "summary", "checks", "activity", "checked_at"} {
				if _, ok := health[key]; !ok {
					t.Errorf("health missing %s", key)
				}
			}
			if ctrl.Status().State != state {
				t.Fatal("read changed engine lifecycle")
			}
			if len(ctrl.(*fake.Controller).Writes()) != 0 {
				t.Fatal("read performed a write")
			}
		})
	}
}

func TestReadStatusConservativeDiagnostics(t *testing.T) {
	ctrl, err := fake.New(engine.Config{})
	if err != nil {
		t.Fatal(err)
	}
	diag, err := ctrl.Diagnostics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                                       string
		state                                      engine.ConnState
		err                                        error
		held                                       bool
		diagnosticHeld, disconnected, unauthorized bool
		gaps, diagnosticGaps                       int
		ready, loggedOut, failed, paused           bool
	}{
		{name: "connected", state: engine.StateConnected, ready: true},
		{name: "revoked", state: engine.StateRevoked, loggedOut: true, failed: true},
		{name: "not authorized", state: engine.StateNotAuthorized, loggedOut: true},
		{name: "error", state: engine.StateError, failed: true},
		{name: "stopped", state: engine.StateStopped, failed: true},
		{name: "connecting", state: engine.StateConnecting},
		{name: "reconnecting", state: engine.StateReconnecting},
		{name: "resyncing", state: engine.StateResyncing},
		{name: "held", state: engine.StateConnected, held: true},
		{name: "diagnostic held", state: engine.StateConnected, diagnosticHeld: true},
		{name: "gaps", state: engine.StateConnected, gaps: 2},
		{name: "diagnostic gaps", state: engine.StateConnected, diagnosticGaps: 2},
		{name: "disconnected diagnostics", state: engine.StateConnected, disconnected: true},
		{name: "unauthorized snapshot", state: engine.StateConnected, unauthorized: true},
		{name: "unknown state", state: "unexpected", failed: true},
		{name: "revoked diagnostics", state: engine.StateConnected, err: engine.ErrRevoked, loggedOut: true, failed: true},
		{name: "unauthorized diagnostics", state: engine.StateConnected, err: engine.ErrNotAuthorized, loggedOut: true},
		{name: "paused diagnostics", state: engine.StateConnected, err: engine.ErrPaused, paused: true},
		{name: "private diagnostics", state: engine.StateConnected, err: errors.New("secret-backend-detail"), failed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := ctrl.Status()
			snapshot.State = tc.state
			snapshot.Updates.Held = tc.held
			snapshot.Updates.OpenGaps = tc.gaps
			diagnostics := diag
			diagnostics.LocalUpdates.Held = tc.diagnosticHeld
			diagnostics.LocalUpdates.OpenGaps = tc.diagnosticGaps
			diagnostics.Connected = !tc.disconnected
			diagnostics.Authorized = !tc.unauthorized
			s := New(Config{Engine: readStatusEngine{Engine: ctrl, snapshot: snapshot, diagnostics: diagnostics, diagnosticsErr: tc.err}})
			report := toolBody(t, toolCall(t, s, "telegram_status", nil))
			for key, want := range map[string]bool{"ready": tc.ready, "logged_out": tc.loggedOut, "error": tc.failed, "paused": tc.paused} {
				if report[key] != want {
					t.Errorf("%s=%v, want %v", key, report[key], want)
				}
			}
			b, _ := json.Marshal(report)
			if strings.Contains(string(b), "secret-backend-detail") {
				t.Fatal("diagnostics leaked backend detail")
			}
			health := s.Health(context.Background(), time.Hour)
			if tc.failed && health.Status != "fail" {
				t.Errorf("health did not fail: %+v", health)
			}
		})
	}
}
