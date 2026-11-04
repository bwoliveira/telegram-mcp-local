package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

func TestStatusAndHealthUseRealStoreTotals(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	const acc engine.AccountID = "status-mcp"
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"), acc, store.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := fake.New(engine.Config{Account: acc})
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately stale engine counters: the local store is authoritative.
	e := readStatusEngine{Engine: f, snapshot: engine.Status{State: engine.StateConnected, Mode: engine.ModeReadOnly, Since: now}, diagnostics: engine.Diagnostics{Authorized: true, Connected: true}}
	s := New(Config{Engine: e, ReadStore: db, Clock: func() time.Time { return now }})
	user := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	channel := engine.PeerRef{Type: engine.PeerChannel, ID: 1}
	oldest := now.Add(-90 * time.Minute)
	for _, peer := range []engine.PeerRef{user, channel} {
		batch := engine.Batch{Account: acc, Source: engine.SourceBackfill,
			Cursor:   &engine.Cursor{Peer: peer, State: "idle", Complete: true, OldestID: 1, OldestDate: &oldest},
			Messages: []engine.Message{{Key: engine.MessageKey{Account: acc, Peer: peer, ID: 1}, Date: now.Add(-10 * time.Minute), Text: "synthetic recent history"}},
		}
		if peer == user {
			batch.Dialogs = []engine.Dialog{{Peer: peer}}
			batch.Messages = append(batch.Messages, engine.Message{Key: engine.MessageKey{Account: acc, Peer: peer, ID: 2}, Date: oldest, Out: true, Text: "synthetic older outgoing"})
		}
		if _, err := db.Apply(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	report := toolBody(t, toolCall(t, s, "telegram_status", nil))
	index := report["index"].(map[string]any)
	if index["messages"] != float64(3) || index["chats"] != float64(2) || index["coverage_complete"] != true {
		t.Fatalf("real-store totals/coverage lost: %v", index)
	}
	history := report["sync"].(map[string]any)["history"].(map[string]any)
	if history["messages_synced"] != float64(3) || history["conversations"] != float64(2) || history["last_at"] != now.Format(time.RFC3339) {
		t.Fatalf("stale engine history leaked: %v", history)
	}
	rows, ok := report["conversation_coverage"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("per-conversation coverage missing: %v", report)
	}
	for _, raw := range rows {
		row := raw.(map[string]any)
		want := float64(1)
		if row["chat_id"] == "user:1" {
			want = 2
		}
		if row["messages"] != want || row["coverage_complete"] != true || row["history_since"] != oldest.Format(time.RFC3339) {
			t.Fatalf("wrong conversation coverage: %v", row)
		}
	}
	health := toolBody(t, toolCall(t, s, "health", nil))
	hh, ok := health["history"].(map[string]any)
	if !ok || hh["messages_synced"] != float64(3) || hh["conversations"] != float64(2) || hh["last_at"] != now.Format(time.RFC3339) {
		t.Fatalf("health totals missing: %v", health)
	}
	activity := health["activity"].(map[string]any)
	if activity["messages_last_hour"] != float64(2) || activity["messages_last_24h"] != float64(3) {
		t.Fatalf("health activity lost: %v", activity)
	}
	for _, which := range []string{"memory_held", "diagnostic_held", "diagnostic_error"} {
		t.Run(which, func(t *testing.T) {
			guard := e
			switch which {
			case "memory_held":
				guard.snapshot.Updates.Held = true
			case "diagnostic_held":
				guard.diagnostics.LocalUpdates.Held = true
			case "diagnostic_error":
				guard.diagnosticsErr = errors.New("synthetic unavailable diagnostics")
			}
			guarded := New(Config{Engine: guard, ReadStore: db, Clock: func() time.Time { return now }})
			body := toolBody(t, toolCall(t, guarded, "telegram_status", nil))
			for _, raw := range body["conversation_coverage"].([]any) {
				if raw.(map[string]any)["coverage_complete"] != false {
					t.Fatalf("unsafe conversation proof: %v", body)
				}
			}
		})
	}
	if _, err := db.Apply(ctx, engine.Batch{Account: acc, Gaps: []engine.GapMark{{Reason: engine.GapStateLost, Detected: now}}}); err != nil {
		t.Fatal(err)
	}
	report = toolBody(t, toolCall(t, s, "telegram_status", nil))
	if report["ready"] != true || report["index"].(map[string]any)["coverage_complete"] != false || report["sync"].(map[string]any)["updates"].(map[string]any)["terminal_gaps"] != float64(1) {
		t.Fatalf("durable gap hidden by stale engine status: %v", report)
	}
	if h := s.Health(ctx, 0); h.Status != "ok" {
		t.Fatalf("health ignored durable gap: %+v", h)
	}
	if writes := f.(*fake.Controller).Writes(); len(writes) != 0 {
		t.Fatalf("status/health wrote Telegram: %v", writes)
	}
}

type unavailableStatusStore struct{ ReadStore }

func (unavailableStatusStore) ReadStatus(context.Context, engine.AccountID) (engine.HistoryProgress, engine.UpdatesStatus, error) {
	return engine.HistoryProgress{}, engine.UpdatesStatus{}, errors.New("synthetic private database detail")
}

func (unavailableStatusStore) ReadCoverage(context.Context, engine.AccountID, *engine.PeerRef) (bool, *time.Time, error) {
	return true, nil, nil
}

func TestStatusAndHealthHideStoreStatusErrors(t *testing.T) {
	f, err := fake.New(engine.Config{})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Engine: f, ReadStore: unavailableStatusStore{ReadStore: f.(*fake.Controller)}, Clock: fake.Epoch})
	r := toolBody(t, toolCall(t, s, "telegram_status", nil))
	if r["ready"] != false || r["error"] != true || r["sync_state"] != "error" {
		t.Fatalf("unreadable store reported ready: %v", r)
	}
	for _, name := range []string{"telegram_status", "health"} {
		body := toolBody(t, toolCall(t, s, name, nil))
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(encoded)), "synthetic private database detail") {
			t.Fatalf("%s leaked private store error", name)
		}
	}
	if h := s.Health(context.Background(), 0); h.Status != "warn" {
		t.Fatalf("health ignored unreadable store: %+v", h)
	}
}
