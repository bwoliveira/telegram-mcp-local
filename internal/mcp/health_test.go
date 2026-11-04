package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func healthy(now time.Time) healthInputs {
	last := now.Add(-20 * time.Minute)
	return healthInputs{
		Now:         now,
		Uptime:      3 * time.Hour,
		Diagnostics: engine.Diagnostics{Library: "fakeengine", Authorized: true, Connected: true},
		Sync:        engine.Status{State: engine.StateConnected, Since: now.Add(-3 * time.Hour), Mode: engine.ModeReadOnly},
		Activity:    index.Activity{NewestIncoming: &last, NewestAny: &last, LastHour: 12, LastDay: 140},
	}
}

func check(t *testing.T, h Health, name string) Check {
	t.Helper()
	for _, c := range h.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no %q check in %+v", name, h.Checks)
	return Check{}
}

// Only these engine methods may be used by health/status. The nil embedded
// interface deliberately makes any unexpected network read or write fail.
type healthTestEngine struct {
	engine.Engine
	status         engine.Status
	diagnostics    engine.Diagnostics
	diagnosticsErr error
	statusCalls    int
	diagCalls      int
	diagContext    context.Context
}

func (e *healthTestEngine) Status() engine.Status {
	e.statusCalls++
	return e.status
}

func (e *healthTestEngine) Diagnostics(ctx context.Context) (engine.Diagnostics, error) {
	e.diagCalls++
	e.diagContext = ctx
	return e.diagnostics, e.diagnosticsErr
}

func TestHealthUsesEngine(t *testing.T) {
	e := &healthTestEngine{
		status:      engine.Status{State: engine.StateConnected, Mode: engine.ModeReadOnly},
		diagnostics: engine.Diagnostics{Library: "fakeengine", Authorized: true, Connected: true},
	}
	s := &Server{eng: e, started: time.Now()}
	h := s.Health(context.Background(), 0)
	if h.Status != "warn" || check(t, h, "paired").Status != "ok" || check(t, h, "sync").Status != "ok" {
		t.Fatalf("health without inherited index: %+v", h)
	}
	if e.statusCalls != 1 || e.diagCalls != 1 {
		t.Fatalf("engine calls: status=%d diagnostics=%d", e.statusCalls, e.diagCalls)
	}
	if _, ok := e.diagContext.Deadline(); !ok {
		t.Fatal("diagnostics has no bounded context")
	}
	if !errors.Is(e.diagContext.Err(), context.Canceled) {
		t.Fatal("health did not release diagnostics context")
	}
}

func TestHealthWithoutEngine(t *testing.T) {
	h := (&Server{started: time.Now()}).Health(context.Background(), 0)
	if h.Status != "fail" || check(t, h, "engine").Status != "fail" {
		t.Fatalf("missing engine: %+v", h)
	}
}

func TestStatusUsesEngineAndLabelsInheritedIndex(t *testing.T) {
	e := &healthTestEngine{
		status:      engine.Status{State: engine.StateConnected, Mode: engine.ModeReadOnly},
		diagnostics: engine.Diagnostics{Library: "fakeengine", Authorized: true, Connected: true},
	}
	report := statusReport(t, &Server{eng: e}, context.Background())
	if report["sync"].(map[string]any)["state"] != "connected" || report["engine"].(map[string]any)["library"] != "fakeengine" {
		t.Fatalf("missing engine snapshots: %+v", report)
	}
	if report["index_verified"] != false || !strings.Contains(report["index_note"].(string), "inherited") {
		t.Fatalf("index reported as Telegram verified: %+v", report)
	}
	if e.statusCalls != 1 || e.diagCalls != 1 {
		t.Fatalf("engine calls: status=%d diagnostics=%d", e.statusCalls, e.diagCalls)
	}
}

func TestStatusEngineProblems(t *testing.T) {
	for _, st := range []engine.ConnState{engine.StateNotAuthorized, engine.StateLoggedOut, engine.StateRevoked, engine.StateError, engine.StateStopped, engine.StatePaused} {
		t.Run(string(st), func(t *testing.T) {
			e := &healthTestEngine{status: engine.Status{State: st}, diagnosticsErr: errors.New("private diagnostics failure")}
			report := statusReport(t, &Server{eng: e}, context.Background())
			if len(report["problems"].([]any)) == 0 {
				t.Fatalf("missing engine problem: %+v", report)
			}
			if st == engine.StatePaused && !strings.Contains(report["note"].(string), "reconnect") {
				t.Fatalf("paused must not promise automatic restart: %+v", report)
			}
		})
	}
}

func TestStatusReportsTypedDiagnosticsFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "revoked", err: engine.ErrRevoked, want: "revoked"},
		{name: "not authorized", err: engine.ErrNotAuthorized, want: "not authorized"},
		{name: "paused", err: engine.ErrPaused, want: "reconnect"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &healthTestEngine{status: engine.Status{State: engine.StateConnected}, diagnosticsErr: tc.err}
			report := statusReport(t, &Server{eng: e}, context.Background())
			body, err := json.Marshal(report["problems"])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), tc.want) {
				t.Fatalf("missing actionable engine failure %q: %s", tc.want, body)
			}
		})
	}
}

func TestStatusWithoutEngine(t *testing.T) {
	report := statusReport(t, &Server{}, context.Background())
	if len(report["problems"].([]any)) == 0 {
		t.Fatalf("missing engine is not reported: %+v", report)
	}
}

func TestHealthAndStatusReadInheritedIndexWithoutVerifyingTelegram(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inherited.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE messages (chat_jid TEXT, ts INTEGER, from_me INTEGER, deleted_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, ts := range []time.Time{now.Add(-48 * time.Hour), now.Add(-20 * time.Minute)} {
		if _, err := db.Exec(`INSERT INTO messages (chat_jid, ts, from_me) VALUES ('user:123', ?, 0)`, ts.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	x, err := index.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = x.Close() })
	e := &healthTestEngine{
		status:      engine.Status{State: engine.StateConnected, Since: now.Add(-time.Hour), Mode: engine.ModeReadOnly},
		diagnostics: engine.Diagnostics{Library: "fakeengine", Authorized: true, Connected: true},
	}
	s := &Server{eng: e, index: x, started: now}
	h := s.Health(context.Background(), 0)
	if h.Status != "warn" || h.Activity.LastHour != 1 || h.Activity.LastDay != 1 {
		t.Fatalf("inherited activity was lost or treated as verified: %+v", h)
	}
	if check(t, h, "receiving").Status != "warn" || check(t, h, "coverage").Status != "warn" {
		t.Fatalf("inherited index verified Telegram: %+v", h)
	}
	report := statusReport(t, s, context.Background())
	if report["index"].(map[string]any)["messages"] != float64(2) || report["index_verified"] != false {
		t.Fatalf("inherited coverage was lost or verified: %+v", report)
	}
	if len(report["unknown_windows"].([]any)) != 1 || !strings.Contains(report["unknown_windows_note"].(string), "not confirmed Telegram gaps") {
		t.Fatalf("silent window was not labelled honestly: %+v", report)
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	if h := s.Health(context.Background(), 0); check(t, h, "receiving").Status != "warn" {
		t.Fatalf("index failure misreported as engine failure: %+v", h)
	}
	report = statusReport(t, s, context.Background())
	if len(report["problems"].([]any)) == 0 {
		t.Fatalf("index failure was silently ignored: %+v", report)
	}
}

func TestHealthAndStatusPropagateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, method := range []string{"health", "status"} {
		t.Run(method, func(t *testing.T) {
			e := &healthTestEngine{
				status:         engine.Status{State: engine.StateConnected},
				diagnosticsErr: context.Canceled,
			}
			s := &Server{eng: e, started: time.Now()}
			if method == "health" {
				if h := s.Health(ctx, 0); h.Status != "fail" {
					t.Fatalf("canceled diagnostics reported healthy: %+v", h)
				}
			} else {
				if report := statusReport(t, s, ctx); len(report["problems"].([]any)) == 0 {
					t.Fatalf("canceled diagnostics omitted: %+v", report)
				}
			}
			if !errors.Is(e.diagContext.Err(), context.Canceled) {
				t.Fatal("caller cancellation did not reach engine diagnostics")
			}
		})
	}
}
