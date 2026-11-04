package mcp

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// Deterministically exercise the unlocked queue-commit seam, without sleeps or
// Telegram: a fresh download/export registration appears before MCP unlinks.
type recreateAfterQueue struct {
	*store.Store
	after func(context.Context, []string) error
}

func (s recreateAfterQueue) PurgeMediaPaths(ctx context.Context, a engine.AccountID, paths []string) error {
	if err := s.Store.PurgeMediaPaths(ctx, a, paths); err != nil {
		return err
	}
	return s.after(ctx, paths)
}

func TestMediaPurgeKeepsDownloadRecreatedAfterQueueCommit(t *testing.T) {
	s, st, _ := reviewCanonicalServer(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: fake.PrivatePeer, ID: 4}
	d, err := s.DownloadMedia(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	s.readStore = recreateAfterQueue{Store: st, after: func(ctx context.Context, paths []string) error {
		if len(paths) != 1 || paths[0] != d.Path {
			t.Fatalf("unexpected queue paths: %v", paths)
		}
		if err := os.WriteFile(d.Path, []byte("replacement"), 0600); err != nil {
			return err
		}
		_, err := st.Apply(ctx, engine.Batch{Account: "primary", Downloads: []engine.DownloadRecord{{Key: key, Path: d.Path, Bytes: 11, At: s.clock()}}})
		return err
	}}
	n, _, _, _ := s.PurgeMediaContext(ctx, "", 0, 0, false)
	body, err := os.ReadFile(d.Path)
	if err != nil || string(body) != "replacement" {
		t.Fatalf("fresh download removed after queue commit: %q %v (deleted=%d)", body, err, n)
	}
	assertFreshMediaReference(t, s, key, d.Path)
	if n != 0 {
		t.Fatalf("protected replacement counted as purged: %d", n)
	}
}

func TestMediaAndExportPurgeKeepFreshReferencesAfterQueueCommit(t *testing.T) {
	s, st, _ := reviewCanonicalServer(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: fake.PrivatePeer, ID: 4}
	d, err := s.DownloadMedia(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	body := paritySuccess(t, toolCall(t, s, "export_messages", map[string]any{"chat_id": "me"}))
	export := body["path"].(string)
	recreate := func(ctx context.Context, paths []string) error {
		for _, path := range paths {
			if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
				return err
			}
			if path == export {
				if err := st.RegisterStatsExport(ctx, "primary", path, "{}", 1, 11); err != nil {
					return err
				}
			} else {
				if _, err := st.Apply(ctx, engine.Batch{Account: "primary", Downloads: []engine.DownloadRecord{{Key: key, Path: path, Bytes: 11, At: s.clock()}}}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	s.readStore = recreateAfterQueue{Store: st, after: recreate}
	_, _, _, _ = s.PurgeMediaContext(ctx, "", 0, 0, false)
	_, _, _ = s.PurgeExportsContext(ctx, 0, false)
	for _, path := range []string{d.Path, export} {
		b, err := os.ReadFile(path)
		if err != nil || string(b) != "replacement" {
			t.Errorf("fresh artifact removed: %s: %q %v", path, b, err)
		}
	}
	assertFreshMediaReference(t, s, key, d.Path)
	if _, err := st.ReadStatsExport(ctx, "primary", export); err != nil {
		t.Fatalf("fresh export metadata missing: %v", err)
	}
}

func assertFreshMediaReference(t *testing.T, s *Server, key engine.MessageKey, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(filepath.Dir(s.mediaDir), "accounts", string(key.Account), "store.db")+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	err = db.QueryRow(`SELECT count(*) FROM media_files f JOIN messages m ON m.row_id=f.row_id WHERE m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=? AND f.path=?`, key.Account, key.Peer.Type, key.Peer.ID, key.ID, path).Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("fresh metadata missing: count=%d %v", count, err)
	}
}
