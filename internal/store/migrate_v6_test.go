package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Construct the genuine v5 schema by applying only migrations available in v5.
func reactionV5Fixture(t *testing.T) (string, engine.MessageKey) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "v5.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &Store{writer: db, account: confAccount, now: func() time.Time { return confTime }}
	ctx := context.Background()
	for _, migrate := range []func(context.Context) error{s.migrateV1, s.migrateV2, s.migrateV3, s.migrateV4, s.migrateV5} {
		if err := migrate(ctx); err != nil {
			t.Fatal(err)
		}
	}
	key := engine.MessageKey{Account: confAccount, Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
	if _, err := db.Exec(`INSERT INTO accounts(account,updated_at) VALUES(?,1000);
INSERT INTO peers(account,peer_type,peer_id,kind,updated_at) VALUES(?,'user',10,'user',1000);
INSERT INTO messages(account,peer_type,peer_id,msg_id,date,text,source,first_seen_at,updated_at) VALUES(?,'user',10,8,1000,'synthetic v5','backfill',1000,1000);
INSERT INTO reactions(row_id,counts_json,recent_json,counts_hash,updated_at) SELECT row_id,'[{"emoji":"thumb","count":1}]','[]','legacy-hash',1000 FROM messages;`, confAccount, confAccount, confAccount); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path, key
}

func TestReactionMigrationV6UpgradeRollbackAndReplay(t *testing.T) {
	path, key := reactionV5Fixture(t)
	ctx := context.Background()
	interrupted := errors.New("synthetic v6 interruption")
	s, err := Open(ctx, path, confAccount, FaultAt(func(stage string) error {
		if stage == "migration_v6_before_commit" {
			return interrupted
		}
		return nil
	}))
	if s != nil {
		_ = s.Close()
	}
	if !errors.Is(err, interrupted) {
		t.Fatalf("migration rollback error=%v", err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	var version, columns int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM pragma_table_info('reactions') WHERE name='version'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if version != 5 || columns != 0 {
		t.Fatalf("partial migration version=%d columns=%d", version, columns)
	}
	var hash string
	if err := raw.QueryRow(`SELECT counts_hash FROM reactions`).Scan(&hash); err != nil || hash != "legacy-hash" {
		t.Fatalf("rollback changed payload hash=%q err=%v", hash, err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		s, err = Open(ctx, path, confAccount, FaultAt(func(stage string) error {
			if reopen > 0 && stage == "migration_v6_before_commit" {
				return errors.New("v6 reran")
			}
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		if err := s.reader.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
			t.Fatalf("version=%d err=%v", version, err)
		}
		if v := reactionRevision(t, s, key); v != int64(reopen) {
			t.Fatalf("reopened baseline=%d", v)
		}
		r := reactionRevisionState(key, 1+reopen)
		confNoEvents(t, confApply(t, s, engine.Batch{Account: confAccount, Reactions: []engine.ReactionUpdate{r}}))
		if reopen == 0 {
			a := confApply(t, s, engine.Batch{Account: confAccount, Reactions: []engine.ReactionUpdate{reactionRevisionState(key, 2)}})
			if len(a.Reactions) != 1 || a.Reactions[0].Version != 1 {
				t.Fatalf("first upgraded revision=%+v", a)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReactionMigrationV6PreservesExistingRevision(t *testing.T) {
	s := confOpen(t)
	m := confMessage(engine.PeerRef{Type: engine.PeerUser, ID: 10}, 8, "synthetic lowered-version fixture")
	m.Reactions = reactionRevisionState(m.Key, 1).Counts
	confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
	if _, err := s.writer.Exec(`PRAGMA user_version=5`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateV6(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v := reactionRevision(t, s, m.Key); v != 1 {
		t.Fatalf("migration reset existing revision=%d", v)
	}
}
