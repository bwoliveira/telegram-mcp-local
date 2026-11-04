package mcp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

func TestTelegramHistoryRestoredCursorSurvivesFirstPageFailureThroughWrapper(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "history.db"), "people")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 1001}
	oldest := time.Unix(1000, 0)
	flood := time.Unix(2000, 0)
	cursor := engine.Cursor{Peer: peer, OldestID: 7, OldestDate: &oldest, Complete: true, State: "idle", FloodUntil: &flood}
	if _, err := db.Apply(ctx, engine.Batch{Account: "people", Cursor: &cursor}); err != nil {
		t.Fatal(err)
	}
	// Deliberately hides gotd's optional HistoryProgress, as managedController
	// does in production: durable progress must also work through ReadStore.
	e := &telegramHistoryEngine{Controller: peopleFake(t), backfill: func(context.Context, engine.BackfillRequest) (engine.BackfillResult, error) {
		return engine.BackfillResult{}, engine.ErrNoAccess
	}}
	s := New(Config{Engine: e, ReadStore: db})
	if _, _, err := s.requestTelegramHistory(ctx, "user:1001", 10, 1, 0); err != nil {
		t.Fatal(err)
	}
	done := waitTelegramHistory(t, s)
	if len(done.Results) != 1 || done.Results[0].OldestID != 7 || !done.Results[0].Complete || done.Results[0].FloodUntil == nil || !done.Results[0].FloodUntil.Equal(flood) {
		t.Fatalf("restart failure erased durable cursor: %+v", done)
	}
}

func TestTelegramHistoryCoverageUsesProductionSQLiteGapEvidence(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "history.db"), "people")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f := peopleFake(t)
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 1001}
	oldest := time.Unix(1000, 0)
	cursor := engine.Cursor{Peer: peer, OldestID: 1, OldestDate: &oldest, Complete: true, State: "idle"}
	if _, err := db.Apply(ctx, engine.Batch{Account: "people", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Kind: engine.KindUser, Access: engine.AccessOK}}}, Cursor: &cursor}); err != nil {
		t.Fatal(err)
	}
	s := New(Config{Engine: f, ReadStore: db})
	job := HistoryJob{Chats: []string{"user:1001"}, Results: []engine.BackfillResult{{Peer: peer, Complete: true}}}
	coverage := s.telegramHistoryCoverage(ctx, job)
	if len(coverage) != 1 || !coverage[0].Complete || coverage[0].Partial || coverage[0].HistorySince == nil || !coverage[0].HistorySince.Equal(oldest) {
		t.Fatalf("cursor proof lost: %+v", coverage)
	}
	gap := engine.GapMark{Peer: &peer, Reason: engine.GapChannelTooLong, FromID: 5, ToID: 9, Detected: time.Unix(2000, 0)}
	if _, err := db.Apply(ctx, engine.Batch{Account: "people", Gaps: []engine.GapMark{gap}}); err != nil {
		t.Fatal(err)
	}
	coverage = s.telegramHistoryCoverage(ctx, job)
	if coverage[0].Complete || !coverage[0].Partial || coverage[0].HistorySince == nil || coverage[0].Error != "" {
		t.Fatalf("open gap hidden by accessible start: %+v", coverage)
	}
}
