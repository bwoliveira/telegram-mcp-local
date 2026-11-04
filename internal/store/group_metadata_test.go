package store

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func groupMetadataSnapshot() engine.ParticipantsSnapshot {
	created := confTime.Add(-time.Hour)
	return engine.ParticipantsSnapshot{
		Group:        engine.PeerRef{Type: engine.PeerChannel, ID: 900},
		Participants: []engine.Participant{{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 80}, Name: "Member", Role: "member"}},
		Size:         1, About: "Synthetic description", At: confTime,
		Owner: &engine.PeerRef{Type: engine.PeerUser, ID: 99}, CreatedAt: &created,
		LinkedChat: &engine.PeerRef{Type: engine.PeerChannel, ID: 901}, AmAdmin: true,
	}
}

func assertGroupSnapshotMetadata(t *testing.T, s *Store, p engine.ParticipantsSnapshot) {
	t.Helper()
	ctx := context.Background()
	strict, err := s.ReadGroup(ctx, confAccount, p.Group)
	if err != nil {
		t.Fatal(err)
	}
	legacy, found, err := s.Group(ctx, confAccount, p.Group)
	if err != nil || !found {
		t.Fatalf("Group found=%v err=%v", found, err)
	}
	for _, g := range []engine.Group{strict, legacy} {
		if !reflect.DeepEqual(g.Owner, p.Owner) || !reflect.DeepEqual(g.LinkedChat, p.LinkedChat) || g.AmAdmin != p.AmAdmin {
			t.Fatalf("metadata mismatch: owner=%v linked=%v admin=%v; want owner=%v linked=%v admin=%v", g.Owner, g.LinkedChat, g.AmAdmin, p.Owner, p.LinkedChat, p.AmAdmin)
		}
		if (g.CreatedAt == nil) != (p.CreatedAt == nil) || g.CreatedAt != nil && g.CreatedAt.Unix() != p.CreatedAt.Unix() {
			t.Fatalf("created_at=%v, want %v", g.CreatedAt, p.CreatedAt)
		}
		if g.About != p.About || g.Size != p.Size || g.Partial != p.Partial || !g.SnapshotAt.Equal(p.At) || len(g.Participants) != len(p.Participants) {
			t.Fatalf("snapshot metadata or membership changed: %+v", g)
		}
	}
}

func groupWriterChanges(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	if err := s.writer.QueryRowContext(context.Background(), "SELECT total_changes()").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestParticipantsMetadataRoundTripReplayAndStaleSnapshot(t *testing.T) {
	s := confOpen(t)
	p := groupMetadataSnapshot()
	// The persisted Batch contract must also retain nullable metadata in JSON.
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var decoded engine.ParticipantsSnapshot
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, p) {
		t.Fatal("snapshot JSON round trip lost metadata")
	}
	b := engine.Batch{Account: confAccount, Participants: []engine.ParticipantsSnapshot{decoded}}
	confApply(t, s, b)
	assertGroupSnapshotMetadata(t, s, p)
	before := groupWriterChanges(t, s)
	s.now = func() time.Time { return confTime.Add(time.Minute) }
	// SQLite stores seconds: another time zone/subsecond representation of the
	// same created_at must not turn an identical replay into SQL writes.
	represented := p.CreatedAt.In(time.FixedZone("Synthetic offset", 3600)).Add(time.Nanosecond)
	b.Participants[0].CreatedAt = &represented
	confApply(t, s, b)
	if after := groupWriterChanges(t, s); after != before {
		t.Fatalf("identical replay wrote %d rows", after-before)
	}
	stale := p
	stale.At = p.At.Add(-time.Second)
	stale.Owner, stale.CreatedAt, stale.LinkedChat, stale.AmAdmin = nil, nil, nil, false
	confApply(t, s, engine.Batch{Account: confAccount, Participants: []engine.ParticipantsSnapshot{stale}})
	assertGroupSnapshotMetadata(t, s, p)
	if after := groupWriterChanges(t, s); after != before {
		t.Fatalf("stale snapshot wrote %d rows", after-before)
	}
}

func TestParticipantsMetadataChangesAtSameTimestamp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*engine.ParticipantsSnapshot)
	}{
		{"owner", func(p *engine.ParticipantsSnapshot) { p.Owner = &engine.PeerRef{Type: engine.PeerUser, ID: 100} }},
		{"owner cleared", func(p *engine.ParticipantsSnapshot) { p.Owner = nil }},
		{"created_at", func(p *engine.ParticipantsSnapshot) { v := p.CreatedAt.Add(time.Second); p.CreatedAt = &v }},
		{"created_at cleared", func(p *engine.ParticipantsSnapshot) { p.CreatedAt = nil }},
		{"linked id", func(p *engine.ParticipantsSnapshot) {
			p.LinkedChat = &engine.PeerRef{Type: engine.PeerChannel, ID: 902}
		}},
		{"linked type", func(p *engine.ParticipantsSnapshot) { p.LinkedChat = &engine.PeerRef{Type: engine.PeerChat, ID: 901} }},
		{"linked cleared", func(p *engine.ParticipantsSnapshot) { p.LinkedChat = nil }},
		{"admin revoked", func(p *engine.ParticipantsSnapshot) { p.AmAdmin = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := confOpen(t)
			p := groupMetadataSnapshot()
			confApply(t, s, engine.Batch{Account: confAccount, Participants: []engine.ParticipantsSnapshot{p}})
			assertGroupSnapshotMetadata(t, s, p)
			before := groupWriterChanges(t, s)
			tc.change(&p)
			b := engine.Batch{Account: confAccount, Participants: []engine.ParticipantsSnapshot{p}}
			confApply(t, s, b)
			assertGroupSnapshotMetadata(t, s, p)
			if after := groupWriterChanges(t, s); after == before {
				t.Fatal("same-timestamp metadata change was treated as replay")
			}
			before = groupWriterChanges(t, s)
			confApply(t, s, b)
			if after := groupWriterChanges(t, s); after != before {
				t.Fatalf("replay of changed metadata wrote %d rows", after-before)
			}
		})
	}
}
