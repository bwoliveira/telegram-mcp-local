package gotdengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

func TestStatusUsesDurableStoreCounts(t *testing.T) {
	ctx := context.Background()
	const acc engine.AccountID = "status-test"
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "store.db")
	db, err := store.Open(ctx, path, acc, store.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	user := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	channel := engine.PeerRef{Type: engine.PeerChannel, ID: 99}
	batch := engine.Batch{Account: acc, Source: engine.SourceBackfill,
		Dialogs: []engine.Dialog{{Peer: user}},
		Messages: []engine.Message{
			{Key: engine.MessageKey{Account: acc, Peer: user, ID: 1}, Date: now.Add(-48 * time.Hour), Text: "synthetic old history"},
			{Key: engine.MessageKey{Account: acc, Peer: channel, ID: 1}, Date: now.Add(-24 * time.Hour), Text: "synthetic channel history"},
		},
	}
	if _, err := db.Apply(ctx, batch); err != nil {
		t.Fatal(err)
	}
	newEngine := func() *Engine {
		t.Helper()
		e, err := New(engine.Config{Account: acc, APIID: 1, APIHash: "synthetic-test-only", Store: db, Session: &lifecycleSession{}})
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	assert := func(e *Engine, messages int64, conversations int, last time.Time) {
		t.Helper()
		s := e.Status()
		if s.History.Messages != messages || s.History.Conversations != conversations || !s.History.LastAt.Equal(last) {
			t.Fatalf("durable history = %+v; want messages=%d conversations=%d last=%s", s.History, messages, conversations, last)
		}
	}
	e := newEngine()
	assert(e, 2, 2, now)
	// A fresh controller must report the same retained data before any network activity.
	assert(newEngine(), 2, 2, now)
	// Replaying the indexed page must not inflate counters or the arrival time.
	now = now.Add(time.Minute)
	if _, err := db.Apply(ctx, batch); err != nil {
		t.Fatal(err)
	}
	assert(e, 2, 2, now.Add(-time.Minute))
	if _, err := db.Apply(ctx, engine.Batch{Account: acc, Deletes: []engine.Deletion{{Peer: &channel, IDs: []int{1}}}}); err != nil {
		t.Fatal(err)
	}
	assert(e, 1, 1, now.Add(-time.Minute))
}
