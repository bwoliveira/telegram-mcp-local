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

func TestHiddenRevisionMigrationV5AtomicBackfill(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "migration.db")
	s, err := Open(ctx, path, confAccount)
	if err != nil {
		t.Fatal(err)
	}
	m := confMessage(engine.PeerRef{Type: engine.PeerUser, ID: 10}, 8, "visible revision")
	edited := time.Unix(2000, 0)
	m.EditDate = &edited
	confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
	for _, column := range []string{"server_edit_date", "content_hash"} {
		if _, err := s.writer.Exec(`ALTER TABLE messages DROP COLUMN ` + column); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.writer.Exec(`PRAGMA user_version=4`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("synthetic interrupted migration")
	_, err = Open(ctx, path, confAccount, FaultAt(func(stage string) error {
		if stage == "migration_v5_before_commit" {
			return interrupted
		}
		return nil
	}))
	if !errors.Is(err, interrupted) {
		t.Fatalf("rollback error=%v", err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	var columns, version int
	if err := raw.QueryRow(`SELECT count(*) FROM pragma_table_info('messages') WHERE name IN ('server_edit_date','content_hash')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := raw.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if columns != 0 || version != 4 {
		t.Fatalf("partial revision migration: columns=%d version=%d", columns, version)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	for reopen := 0; reopen < 2; reopen++ {
		s, err = Open(ctx, path, confAccount)
		if err != nil {
			t.Fatal(err)
		}
		got := confGet(t, s, m.Key)
		if got.EditDate == nil || !got.EditDate.Equal(edited) || got.ServerEditDate == nil || !got.ServerEditDate.Equal(edited) || got.Text != m.Text || got.ContentHash != "" {
			t.Fatalf("backfill/reopen %d changed content or lost revision: %+v", reopen, got)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
