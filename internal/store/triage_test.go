package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func triageOpen(t *testing.T, path string, account engine.AccountID) *Store {
	t.Helper()
	s, err := Open(context.Background(), path, account)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func triageSeed(t *testing.T, s *Store, peers ...engine.PeerRef) {
	t.Helper()
	for _, peer := range peers {
		_, err := s.writer.Exec(`INSERT INTO peers(account,peer_type,peer_id,kind,updated_at) VALUES(?,?,?,?,?)`, s.account, peer.Type, peer.ID, defaultKind(peer), 1)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTriageHandledPersistsNanosecondsAndOwnedSnapshots(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	s := triageOpen(t, path, "primary")
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 7}
	triageSeed(t, s, peer)
	marks, err := s.ReadTriageMarks(ctx, "primary")
	if err != nil || marks == nil || len(marks) != 0 {
		t.Fatalf("initial marks = %v, %v", marks, err)
	}
	at := time.Date(2026, 11, 2, 10, 0, 0, 123456789, time.FixedZone("offset", -3*60*60))
	if err := s.MarkTriageHandled(ctx, "primary", peer, "original", at); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = triageOpen(t, path, "primary")
	marks, err = s.ReadTriageMarks(ctx, "primary")
	if err != nil {
		t.Fatal(err)
	}
	m := marks[peer]
	if len(marks) != 1 || m.Peer != peer || m.HandledAt == nil || !m.HandledAt.Equal(at) || m.HandledAt.Location() != time.UTC || m.Note != "original" || m.SnoozedAt != nil || m.SnoozeUntil != nil {
		t.Fatalf("reopened mark = %+v", m)
	}
	*m.HandledAt = time.Time{}
	delete(marks, peer)
	marks, err = s.ReadTriageMarks(ctx, "primary")
	if err != nil || marks[peer].HandledAt == nil || !marks[peer].HandledAt.Equal(at) {
		t.Fatalf("snapshot mutation leaked: %v, %v", marks, err)
	}
	if err := s.MarkTriageHandled(ctx, "primary", peer, "", at.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	marks, err = s.ReadTriageMarks(ctx, "primary")
	if err != nil || marks[peer].Note != "original" || !marks[peer].HandledAt.Equal(at.Add(time.Nanosecond)) {
		t.Fatalf("blank note retention = %v, %v", marks, err)
	}
	var version int
	if err := s.reader.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("schema version = %d, %v", version, err)
	}
}

func TestTriageSnoozeTransitionsClearAndNamespaceIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "primary", "store.db")
	s := triageOpen(t, path, "primary")
	peers := []engine.PeerRef{{Type: engine.PeerUser, ID: 9}, {Type: engine.PeerChat, ID: 9}, {Type: engine.PeerChannel, ID: 9}}
	triageSeed(t, s, peers...)
	other := triageOpen(t, filepath.Join(t.TempDir(), "secondary", "store.db"), "secondary")
	triageSeed(t, other, peers...)
	at := time.Unix(1730000000, 987654321).UTC()
	until := at.Add(time.Hour + time.Nanosecond)
	for _, peer := range peers {
		if err := s.MarkTriageHandled(ctx, "primary", peer, string(peer.Type), at); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SnoozeTriage(ctx, "primary", peers[0], "", at, until); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = triageOpen(t, path, "primary")
	marks, err := s.ReadTriageMarks(ctx, "primary")
	if err != nil {
		t.Fatal(err)
	}
	m := marks[peers[0]]
	if len(marks) != 3 || m.HandledAt != nil || m.SnoozedAt == nil || !m.SnoozedAt.Equal(at) || m.SnoozeUntil == nil || !m.SnoozeUntil.Equal(until) || m.Note != "user" {
		t.Fatalf("snooze = %+v, marks = %v", m, marks)
	}
	*m.SnoozedAt = time.Time{}
	*m.SnoozeUntil = time.Time{}
	marks, err = s.ReadTriageMarks(ctx, "primary")
	if err != nil || !marks[peers[0]].SnoozedAt.Equal(at) || !marks[peers[0]].SnoozeUntil.Equal(until) {
		t.Fatalf("snooze snapshot mutation leaked = %v, %v", marks, err)
	}
	if err := s.SnoozeTriage(ctx, "primary", peers[0], "replacement", at, until); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkTriageHandled(ctx, "primary", peers[0], "", at); err != nil {
		t.Fatal(err)
	}
	marks, err = s.ReadTriageMarks(ctx, "primary")
	m = marks[peers[0]]
	if err != nil || m.HandledAt == nil || m.SnoozedAt != nil || m.SnoozeUntil != nil || m.Note != "replacement" {
		t.Fatalf("handled lifts snooze = %+v, %v", m, err)
	}
	if err := s.ClearTriageMark(ctx, "primary", peers[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.ClearTriageMark(ctx, "primary", peers[0]); err != nil {
		t.Fatalf("clear unmarked existing peer: %v", err)
	}
	marks, err = s.ReadTriageMarks(ctx, "primary")
	if _, ok := marks[peers[0]]; err != nil || ok || len(marks) != 2 || marks[peers[1]].Note != "chat" || marks[peers[2]].Note != "channel" {
		t.Fatalf("namespace clear = %v, %v", marks, err)
	}
	otherMarks, err := other.ReadTriageMarks(ctx, "secondary")
	if err != nil || len(otherMarks) != 0 {
		t.Fatalf("account leakage = %v, %v", otherMarks, err)
	}
	// SQL NULL denotes absence, so the Unix epoch remains a real timestamp.
	if err := other.SnoozeTriage(ctx, "secondary", peers[0], "", time.Unix(0, 0), until); err != nil {
		t.Fatal(err)
	}
	otherMarks, err = other.ReadTriageMarks(ctx, "secondary")
	if err != nil || otherMarks[peers[0]].SnoozedAt == nil || !otherMarks[peers[0]].SnoozedAt.Equal(time.Unix(0, 0)) {
		t.Fatalf("epoch timestamp = %v, %v", otherMarks, err)
	}
}

func TestTriageHidesUpstreamSemantics(t *testing.T) {
	at := time.Date(2026, 11, 2, 12, 0, 0, 123456789, time.UTC)
	until := at.Add(time.Hour)
	for _, tc := range []struct {
		name          string
		mark          TriageMark
		incoming, now time.Time
		want          bool
	}{
		{"unmarked", TriageMark{}, at, at, false},
		{"handled older", TriageMark{HandledAt: &at}, at.Add(-time.Nanosecond), until, true},
		{"handled equal", TriageMark{HandledAt: &at}, at, until, true},
		{"handled newer", TriageMark{HandledAt: &at}, at.Add(time.Nanosecond), at, false},
		{"snoozed equal", TriageMark{SnoozedAt: &at, SnoozeUntil: &until}, at, until.Add(-time.Nanosecond), true},
		{"snoozed newer", TriageMark{SnoozedAt: &at, SnoozeUntil: &until}, at.Add(time.Nanosecond), at, false},
		{"snoozed expires equal", TriageMark{SnoozedAt: &at, SnoozeUntil: &until}, at, until, false},
		{"snoozed expired", TriageMark{SnoozedAt: &at, SnoozeUntil: &until}, at, until.Add(time.Nanosecond), false},
		{"snooze missing start", TriageMark{SnoozeUntil: &until}, at, at, false},
		{"snooze missing end", TriageMark{SnoozedAt: &at}, at, at, false},
		{"handled wins expired snooze", TriageMark{HandledAt: &at, SnoozedAt: &at, SnoozeUntil: &until}, at, until, true},
		{"zero incoming", TriageMark{HandledAt: &at}, time.Time{}, at, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.mark.Hides(tc.incoming, tc.now); got != tc.want {
				t.Fatalf("Hides = %v, want %v", got, tc.want)
			}
		})
	}
}
