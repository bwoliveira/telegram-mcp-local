package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestContentIdentityMigrationV7RollbackAndReopen(t *testing.T) {
	path, key := reactionV5Fixture(t)
	ctx := context.Background()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &Store{writer: raw}
	if err := fixture.migrateV6(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE messages SET content_hash='original-cached-hash',version=9`); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("synthetic v7 interruption")
	s, err := Open(ctx, path, confAccount, FaultAt(func(stage string) error {
		if stage == "migration_v7_before_commit" {
			return interrupted
		}
		return nil
	}))
	if s != nil {
		s.Close()
	}
	if !errors.Is(err, interrupted) {
		t.Fatalf("migration error=%v", err)
	}
	raw, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var version, columns int
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM pragma_table_info('messages') WHERE name='content_identity'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if version != 6 || columns != 0 {
		t.Fatalf("partial migration version=%d columns=%d", version, columns)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		s, err = Open(ctx, path, confAccount, FaultAt(func(stage string) error {
			if reopen > 0 && stage == "migration_v7_before_commit" {
				return errors.New("v7 reran")
			}
			return nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		got := confGet(t, s, key)
		if got.ContentHash != "original-cached-hash" || got.ContentIdentity != "" || confCount(t, s, "SELECT version FROM messages") != 9 {
			t.Fatalf("migration changed historical content/evidence: %+v", got)
		}
		if err := s.reader.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != currentSchemaVersion {
			t.Fatalf("version=%d err=%v", version, err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
