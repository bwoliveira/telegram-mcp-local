package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Build a real v4-shaped database using only temporary synthetic fixtures.
func participantRightsV4Fixture(t *testing.T) (string, engine.PeerRef) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")
	s, err := Open(context.Background(), path, confAccount)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	group := engine.PeerRef{Type: engine.PeerChat, ID: 902}
	confApply(t, s, engine.Batch{Account: confAccount,
		Peers:        []engine.PeerUpsert{{Peer: engine.Peer{Ref: group, Kind: engine.KindGroup}}},
		Participants: []engine.ParticipantsSnapshot{{Group: group, At: confTime, Size: 1, Participants: []engine.Participant{rightsParticipant(t, participantRightsFixture{})}}},
	})
	for _, col := range []string{"admin_rights", "banned_rights", "banned_until", "can_edit"} {
		if _, err := s.writer.Exec(`ALTER TABLE participants DROP COLUMN ` + col); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.writer.Exec(`PRAGMA user_version=4`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	return path, group
}

func TestParticipantRightsMigrationV5Atomic(t *testing.T) {
	ctx := context.Background()
	path, group := participantRightsV4Fixture(t)
	interrupted := errors.New("fixture migration interrupted")
	s, err := Open(ctx, path, confAccount, FaultAt(func(stage string) error {
		if stage == "migration_v5_before_commit" {
			return interrupted
		}
		return nil
	}))
	if s != nil {
		s.Close()
	}
	if !errors.Is(err, interrupted) {
		t.Fatalf("migration error=%v; want injected rollback", err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	var version, newColumns, participants int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM pragma_table_info('participants') WHERE name IN ('admin_rights','banned_rights','banned_until','can_edit')`).Scan(&newColumns); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM participants WHERE role='admin'`).Scan(&participants); err != nil {
		t.Fatal(err)
	}
	if version != 4 || newColumns != 0 || participants != 1 {
		t.Fatalf("partial migration: version=%d columns=%d participants=%d", version, newColumns, participants)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		s, err := Open(ctx, path, confAccount, FaultAt(func(stage string) error {
			if reopen > 0 && stage == "migration_v5_before_commit" {
				return errors.New("migration reran on v5")
			}
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.reader.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
			t.Fatalf("version=%d err=%v; want %d", version, err, currentSchemaVersion)
		}
		assertParticipantRights(t, s, group, participantRightsFixture{})
		g, err := s.ReadGroup(ctx, confAccount, group)
		if err != nil {
			t.Fatal(err)
		}
		if g.Participants[0].Role != "admin" || g.Participants[0].Name != "Fixture admin" || g.Size != 1 || !g.SnapshotAt.Equal(confTime) {
			t.Errorf("migration lost existing snapshot: %+v", g)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestParticipantRightsMigrationPreservesExistingColumns(t *testing.T) {
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
	group := engine.PeerRef{Type: engine.PeerChat, ID: 903}
	want := participantRightsFixture{AdminRights: 0xffffffff, BannedRights: 0x80000001, BannedUntil: 2000000000, CanEdit: true}
	confApply(t, s, engine.Batch{Account: confAccount,
		Peers:        []engine.PeerUpsert{{Peer: engine.Peer{Ref: group, Kind: engine.KindGroup}}},
		Participants: []engine.ParticipantsSnapshot{{Group: group, At: confTime, Size: 1, Participants: []engine.Participant{rightsParticipant(t, want)}}},
	})
	// Legacy migration fixtures can lower the version while retaining columns.
	if _, err := s.writer.Exec(`PRAGMA user_version=4`); err != nil {
		t.Fatal(err)
	}
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

func TestParticipantRightsJSONOmitEmpty(t *testing.T) {
	data, err := json.Marshal(rightsParticipant(t, participantRightsFixture{}))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"admin_rights", "banned_rights", "banned_until", "can_edit"} {
		if _, ok := fields[field]; ok {
			t.Errorf("zero %s not omitted: %s", field, data)
		}
	}
}
