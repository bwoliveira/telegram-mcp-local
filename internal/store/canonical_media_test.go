package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestCanonicalMediaDownloadPurgeAndQueueBoundaries(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	s, err := Open(ctx, filepath.Join(data, "accounts", "primary", "store.db"), "primary")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1}, ID: 1}
	if _, err = s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Date: time.Now()}}}); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(data, "media", "user_1", "audio.ogg")
	if err = os.MkdirAll(filepath.Dir(media), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(media, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Apply(ctx, engine.Batch{Account: "primary", Downloads: []engine.DownloadRecord{{Key: key, Path: media, At: time.Now()}}}); err != nil {
		t.Fatalf("canonical download: %v", err)
	}
	for _, path := range []string{filepath.Join(data, "settings.db"), filepath.Join(data, "accounts", "other", "store.db")} {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = s.Apply(ctx, engine.Batch{Account: "primary", Downloads: []engine.DownloadRecord{{Key: key, Path: path}}}); err == nil {
			t.Fatalf("download accepted %s", path)
		}
		if err = s.PurgeMediaPaths(ctx, "primary", []string{path}); err == nil {
			t.Fatalf("purge accepted %s", path)
		}
		lifecycleExec(t, s, `INSERT INTO files_to_unlink VALUES(?,0,'probe')`, path)
	}
	if err = s.PurgeMediaPaths(ctx, "primary", []string{media}); err != nil {
		t.Fatal(err)
	}
	if err = s.Cleanup(ctx, Retention{}); err == nil {
		t.Fatal("unsafe queue should fail closed")
	}
	if _, err = os.Stat(media); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canonical queue did not unlink: %v", err)
	}
	for _, path := range []string{filepath.Join(data, "settings.db"), filepath.Join(data, "accounts", "other", "store.db")} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("foreign file removed: %v", err)
		}
	}
}
