package mcp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

func TestTerminalCoverageDoesNotBlockReadiness(t *testing.T) {
	ctx := context.Background()
	now := fake.Epoch()
	const account engine.AccountID = "readiness"
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"), account)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := fake.New(engine.Config{Account: account})
	if err != nil {
		t.Fatal(err)
	}
	e := readStatusEngine{Engine: f, snapshot: engine.Status{State: engine.StateConnected}, diagnostics: engine.Diagnostics{Authorized: true, Connected: true}}
	s := New(Config{Engine: e, ReadStore: db, Clock: func() time.Time { return now }})
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	_, err = db.Apply(ctx, engine.Batch{Account: account, Source: engine.SourceBackfill,
		Messages: []engine.Message{{Key: engine.MessageKey{Account: account, Peer: peer, ID: 1}, Date: now, Text: "synthetic"}},
		Gaps:     []engine.GapMark{{Reason: engine.GapStateLost, Detected: now}},
	})
	if err != nil {
		t.Fatal(err)
	}
	body := toolBody(t, toolCall(t, s, "telegram_status", nil))
	if body["ready"] != true || body["index"].(map[string]any)["coverage_complete"] != false {
		t.Fatalf("terminal evidence must preserve partial coverage without blocking readiness: %v", body)
	}
	if body["history_state"] != "partial" || body["sync_state"] != "ready" {
		t.Errorf("missing separate history/sync state: %v", body)
	}
	if h := s.Health(ctx, 0); h.Status != "ok" {
		t.Errorf("terminal history is informational: %+v", h)
	}
	// An explicit import is active even between RPC pages, when the durable
	// cursor is idle and cannot represent the in-memory job.
	s.history = &HistoryJob{StartedAt: now}
	importing := toolBody(t, toolCall(t, s, "telegram_status", nil))
	if importing["history_state"] != "syncing" {
		t.Errorf("active import reported idle: %v", importing)
	}
	s.history = nil
	// A memory-only actionable gap must still block readiness even when the
	// durable snapshot contains only terminal evidence.
	e.snapshot.Updates.OpenGaps = 1
	s = New(Config{Engine: e, ReadStore: db, Clock: func() time.Time { return now }})
	body = toolBody(t, toolCall(t, s, "telegram_status", nil))
	if body["ready"] != false || body["sync_state"] != "syncing" {
		t.Fatalf("recoverable gap lost: %v", body)
	}
	if len(f.(*fake.Controller).Writes()) != 0 {
		t.Fatal("status must never write Telegram")
	}
}

func TestTerminalPeerGapDoesNotInvalidateUnrelatedCoverage(t *testing.T) {
	ctx := context.Background()
	const account engine.AccountID = "peer-scope"
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"), account)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := fake.New(engine.Config{Account: account})
	if err != nil {
		t.Fatal(err)
	}
	e := readStatusEngine{Engine: f, snapshot: engine.Status{State: engine.StateConnected}, diagnostics: engine.Diagnostics{Authorized: true, Connected: true}}
	for _, id := range []int64{1, 2} {
		peer := engine.PeerRef{Type: engine.PeerUser, ID: id}
		_, err = db.Apply(ctx, engine.Batch{Account: account, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Access: engine.AccessOK}}}, Cursor: &engine.Cursor{Peer: peer, State: "idle", Complete: true}, Messages: []engine.Message{{Key: engine.MessageKey{Account: account, Peer: peer, ID: 1}, Date: fake.Epoch()}}})
		if err != nil {
			t.Fatal(err)
		}
	}
	affected := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	_, err = db.Apply(ctx, engine.Batch{Account: account, Gaps: []engine.GapMark{{Peer: &affected, Reason: engine.GapAccessLost, Detected: fake.Epoch()}}})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Engine: e, ReadStore: db, Clock: fake.Epoch})
	body := toolBody(t, toolCall(t, s, "telegram_status", nil))
	for _, raw := range body["conversation_coverage"].([]any) {
		row := raw.(map[string]any)
		want := row["chat_id"] == "user:2"
		if row["coverage_complete"] != want {
			t.Fatalf("terminal peer gap polluted another conversation: %v", body)
		}
	}
}

func TestQuietAccountHistoryIsInformation(t *testing.T) {
	h := evaluate(healthInputs{Now: time.Now(), Store: true, Sync: engine.Status{State: engine.StateConnected}, Diagnostics: engine.Diagnostics{Authorized: true, Connected: true}})
	if h.Status != "ok" {
		t.Fatalf("silence and partial history do not prove a broken sync: %+v", h)
	}
}
