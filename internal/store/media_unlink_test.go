package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestQueuedMediaUnlinkPreservesFreshMediaAndExportReferences(t *testing.T) {
	ctx := context.Background()
	s, media, export := purgeFixture(t)
	if err := s.PurgeMediaPaths(ctx, s.account, []string{media, export}); err != nil {
		t.Fatal(err)
	}
	// Crash-safe seam: metadata is gone and both files are still on disk.
	for _, table := range []string{"media_files", "exports"} {
		if n := lifecycleCount(t, s, `SELECT count(*) FROM `+table); n != 0 {
			t.Fatalf("%s was not committed", table)
		}
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM files_to_unlink`); n != 2 {
		t.Fatalf("queue not committed: %d", n)
	}
	lifecycleExec(t, s, `INSERT INTO media_files SELECT row_id,'full',?,4,'new',0 FROM messages`, media)
	lifecycleExec(t, s, `INSERT INTO exports(account,path,filter_json,count,bytes,created_at) VALUES('lifecycle',?,'{}',1,4,0)`, export)
	if err := s.UnlinkQueuedMedia(ctx, "foreign"); !errors.Is(err, engine.ErrAccountMismatch) {
		t.Fatalf("foreign account: %v", err)
	}
	if err := s.UnlinkQueuedMedia(ctx, s.account); err == nil {
		t.Fatal("protected paths must report failure")
	}
	for _, path := range []string{media, export} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("fresh reference unlinked: %v", err)
		}
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM files_to_unlink`); n != 2 {
		t.Fatalf("failed paths lost from durable queue: %d", n)
	}
	// Once references are deliberately purged, the same operation can drain and
	// acknowledge the old queue safely without any MCP-side Remove.
	if err := s.PurgeMediaPaths(ctx, s.account, []string{media, export}); err != nil {
		t.Fatal(err)
	}
	if err := s.UnlinkQueuedMedia(ctx, s.account); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{media, export} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unreferenced queue not drained: %v", err)
		}
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM files_to_unlink`); n != 0 {
		t.Fatalf("queue not acknowledged: %d", n)
	}
}
