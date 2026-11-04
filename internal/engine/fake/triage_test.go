package fake_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

type triageHandledStore interface {
	ReadTriageMarks(context.Context, engine.AccountID) (map[engine.PeerRef]store.TriageMark, error)
	MarkTriageHandled(context.Context, engine.AccountID, engine.PeerRef, string, time.Time) error
}

type triageStore interface {
	triageHandledStore
	SnoozeTriage(context.Context, engine.AccountID, engine.PeerRef, string, time.Time, time.Time) error
	ClearTriageMark(context.Context, engine.AccountID, engine.PeerRef) error
}

func TestTriageTransitionsAndSQLiteRestart(t *testing.T) {
	for _, attached := range []bool{false, true} {
		name := "standalone"
		if attached {
			name = "sqlite"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			cfg := engine.Config{Account: "test"}
			var db *store.Store
			path := filepath.Join(t.TempDir(), "store.db")
			open := func() *store.Store {
				db, err := store.Open(ctx, path, "test")
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = db.Close() })
				return db
			}
			if attached {
				db = open()
				cfg.Store = db
			}
			f := newFake(t, cfg)
			r, ok := any(f).(triageStore)
			if !ok {
				t.Fatal("fake does not implement full triage persistence")
			}
			peer := f.Snapshot().Peers[0].Ref
			at := time.Unix(1730000000, 987654321).UTC()
			until := at.Add(time.Hour)
			if err := r.MarkTriageHandled(ctx, "test", peer, "original", at); err != nil {
				t.Fatal(err)
			}
			if err := r.SnoozeTriage(ctx, "test", peer, "", at, until); err != nil {
				t.Fatal(err)
			}
			if attached {
				// Read the exact durable target, not merely the fake's mirror.
				marks, err := db.ReadTriageMarks(ctx, "test")
				if err != nil || marks[peer].SnoozeUntil == nil || !marks[peer].SnoozeUntil.Equal(until) {
					t.Fatalf("store snooze = %v, %v", marks, err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				db = open()
				cfg.Store = db
				f = newFake(t, cfg)
				r = any(f).(triageStore)
			}
			marks, err := r.ReadTriageMarks(ctx, "test")
			if err != nil {
				t.Fatal(err)
			}
			m := marks[peer]
			if len(marks) != 1 || m.Peer != peer || m.HandledAt != nil || m.SnoozedAt == nil || !m.SnoozedAt.Equal(at) || m.SnoozeUntil == nil || !m.SnoozeUntil.Equal(until) || m.Note != "original" {
				t.Fatalf("snoozed mark = %+v", m)
			}
			*m.SnoozedAt = time.Time{}
			*m.SnoozeUntil = time.Time{}
			delete(marks, peer)
			marks, err = r.ReadTriageMarks(ctx, "test")
			if err != nil || marks[peer].SnoozedAt == nil || !marks[peer].SnoozedAt.Equal(at) || marks[peer].SnoozeUntil == nil || !marks[peer].SnoozeUntil.Equal(until) {
				t.Fatalf("snooze snapshot alias = %v, %v", marks, err)
			}
			if err := r.MarkTriageHandled(ctx, "test", peer, "", at.Add(time.Nanosecond)); err != nil {
				t.Fatal(err)
			}
			marks, err = r.ReadTriageMarks(ctx, "test")
			if err != nil || marks[peer].HandledAt == nil || !marks[peer].HandledAt.Equal(at.Add(time.Nanosecond)) || marks[peer].SnoozedAt != nil || marks[peer].SnoozeUntil != nil || marks[peer].Note != "original" {
				t.Fatalf("handled lifts snooze = %v, %v", marks, err)
			}
			if err := r.SnoozeTriage(ctx, "test", peer, " ", at, at.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}
			marks, err = r.ReadTriageMarks(ctx, "test")
			if err != nil || marks[peer].Note != " " || marks[peer].HandledAt != nil || marks[peer].SnoozeUntil == nil || !marks[peer].SnoozeUntil.Before(at) {
				t.Fatalf("expired snooze and whitespace note = %v, %v", marks, err)
			}
			for range 2 {
				if err := r.ClearTriageMark(ctx, "test", peer); err != nil {
					t.Fatal(err)
				}
			}
			marks, err = r.ReadTriageMarks(ctx, "test")
			if err != nil || len(marks) != 0 {
				t.Fatalf("cleared marks = %v, %v", marks, err)
			}
			if attached {
				marks, err := db.ReadTriageMarks(ctx, "test")
				if err != nil || len(marks) != 0 {
					t.Fatalf("durable clear = %v, %v", marks, err)
				}
			}
			if len(f.Writes()) != 0 {
				t.Fatal("triage emitted Telegram writes")
			}
		})
	}
}

func TestTriageStandaloneHandledSnapshotsAndIsolation(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test"})
	r, ok := any(f).(triageHandledStore)
	if !ok {
		t.Fatal("fake does not implement triage handled persistence")
	}
	ctx := context.Background()
	peer := f.Snapshot().Peers[0].Ref
	at := time.Unix(1730000000, 123456789).UTC()
	marks, err := r.ReadTriageMarks(ctx, "test")
	if err != nil || marks == nil || len(marks) != 0 {
		t.Fatalf("initial marks = %v, %v", marks, err)
	}
	if err := r.MarkTriageHandled(ctx, "test", peer, "retained", at); err != nil {
		t.Fatal(err)
	}
	marks, err = r.ReadTriageMarks(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	m := marks[peer]
	if len(marks) != 1 || m.Peer != peer || m.HandledAt == nil || !m.HandledAt.Equal(at) || m.Note != "retained" || m.SnoozedAt != nil || m.SnoozeUntil != nil {
		t.Fatalf("handled mark = %+v", m)
	}
	*m.HandledAt = time.Time{}
	delete(marks, peer)
	if err := r.MarkTriageHandled(ctx, "test", peer, "", at.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	marks, err = r.ReadTriageMarks(ctx, "test")
	if err != nil || marks[peer].HandledAt == nil || !marks[peer].HandledAt.Equal(at.Add(time.Nanosecond)) || marks[peer].Note != "retained" {
		t.Fatalf("blank note retention = %v, %v", marks, err)
	}
	*marks[peer].HandledAt = time.Time{}
	again, err := r.ReadTriageMarks(ctx, "test")
	if err != nil || again[peer].HandledAt == nil || !again[peer].HandledAt.Equal(at.Add(time.Nanosecond)) {
		t.Fatalf("snapshot aliases retained state = %v, %v", again, err)
	}
	other := newFake(t, engine.Config{Account: "test"})
	isolated, err := any(other).(triageHandledStore).ReadTriageMarks(ctx, "test")
	if err != nil || len(isolated) != 0 {
		t.Fatalf("controllers share marks = %v, %v", isolated, err)
	}
	if len(f.Writes()) != 0 || len(other.Writes()) != 0 {
		t.Fatal("local triage entered Telegram write journal")
	}
}
