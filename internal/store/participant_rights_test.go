package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// The fixture is synthetic Telegram-independent permission data. The high bit
// checks that SQLite round-trips the complete unsigned 32-bit flag range.
type participantRightsFixture struct {
	AdminRights  uint32 `json:"admin_rights"`
	BannedRights uint32 `json:"banned_rights"`
	BannedUntil  int    `json:"banned_until"`
	CanEdit      bool   `json:"can_edit"`
}

func rightsParticipant(t *testing.T, rights participantRightsFixture) engine.Participant {
	t.Helper()
	data, err := json.Marshal(rights)
	if err != nil {
		t.Fatal(err)
	}
	var p engine.Participant
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
	p.Peer = engine.PeerRef{Type: engine.PeerUser, ID: 80}
	p.Role = "admin"
	p.Name = "Fixture admin"
	return p
}

func assertParticipantRights(t *testing.T, s *Store, group engine.PeerRef, want participantRightsFixture) {
	t.Helper()
	ctx := context.Background()
	read, err := s.ReadGroup(ctx, confAccount, group)
	if err != nil {
		t.Fatal(err)
	}
	legacy, ok, err := s.Group(ctx, confAccount, group)
	if err != nil || !ok {
		t.Fatalf("legacy Group: found=%v err=%v", ok, err)
	}
	for name, g := range map[string]engine.Group{"ReadGroup": read, "Group": legacy} {
		if len(g.Participants) != 1 {
			t.Fatalf("%s participants=%+v", name, g.Participants)
		}
		data, err := json.Marshal(g.Participants[0])
		if err != nil {
			t.Fatal(err)
		}
		var got participantRightsFixture
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s rights=%+v; want %+v", name, got, want)
		}
	}
}

func TestParticipantRightsSameTimestampReplay(t *testing.T) {
	for _, field := range []string{"admin_rights", "banned_rights", "banned_until", "can_edit"} {
		t.Run(field, func(t *testing.T) {
			s := confOpen(t)
			group := engine.PeerRef{Type: engine.PeerChat, ID: 901}
			base := participantRightsFixture{}
			b := engine.Batch{Account: confAccount,
				Peers:        []engine.PeerUpsert{{Peer: engine.Peer{Ref: group, Kind: engine.KindGroup}}},
				Participants: []engine.ParticipantsSnapshot{{Group: group, At: confTime, Size: 1, Participants: []engine.Participant{rightsParticipant(t, base)}}},
			}
			confApply(t, s, b)
			// Only one permission changes, without advancing the snapshot's
			// second-resolution timestamp or altering role/display metadata.
			want := base
			switch field {
			case "admin_rights":
				want.AdminRights = 0xffffffff
			case "banned_rights":
				want.BannedRights = 0x80000000
			case "banned_until":
				want.BannedUntil = 2000000000
			case "can_edit":
				want.CanEdit = true
			}
			b.Peers = nil
			b.Participants[0].Participants[0] = rightsParticipant(t, want)
			confApply(t, s, b)
			assertParticipantRights(t, s, group, want)
			var before, after int64
			if err := s.writer.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			confApply(t, s, b)
			if err := s.writer.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Errorf("identical rights replay changed %d rows", after-before)
			}
			// Revocation must replace values rather than merge/OR old privileges.
			b.Participants[0].Participants[0] = rightsParticipant(t, base)
			confApply(t, s, b)
			assertParticipantRights(t, s, group, base)
		})
	}
}

func TestParticipantRightsRoundTrip(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(ctx, path, confAccount)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	})
	group := engine.PeerRef{Type: engine.PeerChannel, ID: 900}
	want := participantRightsFixture{AdminRights: 0x80000001, BannedRights: 0xffffffff, BannedUntil: 2000000000, CanEdit: true}
	confApply(t, s, engine.Batch{Account: confAccount,
		Peers:        []engine.PeerUpsert{{Peer: engine.Peer{Ref: group, Kind: engine.KindSupergroup}}},
		Participants: []engine.ParticipantsSnapshot{{Group: group, At: confTime, Size: 1, Participants: []engine.Participant{rightsParticipant(t, want)}}},
	})
	assertParticipantRights(t, s, group, want)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = nil
	s, err = Open(ctx, path, confAccount)
	if err != nil {
		t.Fatal(err)
	}
	assertParticipantRights(t, s, group, want)
}
