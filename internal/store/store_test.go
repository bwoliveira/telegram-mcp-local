package store

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"path/filepath"
	"testing"
)

func TestS15OpenMigrationAndAccountIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "account", "store.db")
	s, err := Open(ctx, path, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.checkAccount("other"); err != engine.ErrAccountMismatch {
		t.Fatalf("cross account: %v", err)
	}
	var version int
	if err = s.writer.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != currentSchemaVersion {
		t.Fatalf("version %d: %v", version, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, "other")
	if err != engine.ErrAccountMismatch {
		if s != nil {
			s.Close()
		}
		t.Fatalf("reopen different account: %v", err)
	}
}
