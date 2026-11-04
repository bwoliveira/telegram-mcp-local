package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

func reviewCanonicalServer(t *testing.T) (*Server, *store.Store, *time.Time) {
	t.Helper()
	ctx := context.Background()
	data := t.TempDir()
	now := fake.Epoch()
	clock := func() time.Time { return now }
	// Inject the same live State policy as daemon.startAccount.
	settings, err := state.Open(filepath.Join(data, "settings"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = settings.Close() })
	st, err := store.Open(ctx, filepath.Join(data, "accounts", "primary", "store.db"), "primary", store.WithClock(clock), store.WithRetentionSettings(settings))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctrl, err := fake.New(engine.Config{Account: "primary", Mode: engine.ModeReadWrite, Clock: clock, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Engine: ctrl, ReadStore: st, State: settings, Clock: clock, MediaDir: filepath.Join(data, "media"), ExportDir: filepath.Join(data, "exports")}), st, &now
}

func TestReviewCanonicalDownload(t *testing.T) {
	s, _, _ := reviewCanonicalServer(t)
	_, err := s.DownloadMedia(context.Background(), engine.MessageKey{Account: "primary", Peer: fake.PrivatePeer, ID: 4})
	if err != nil {
		t.Fatalf("DownloadMedia using daemon layout failed: %v", err)
	}
}

func TestReviewCanonicalMediaPurge(t *testing.T) {
	s, _, _ := reviewCanonicalServer(t)
	path := filepath.Join(s.mediaDir, "user_1001", "orphan.ogg")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	n, _, _, err := s.PurgeMediaContext(context.Background(), "", 0, 0, true)
	if err != nil || n != 1 {
		t.Fatalf("preview %d %v", n, err)
	}
	n, _, _, err = s.PurgeMediaContext(context.Background(), "", 0, 0, false)
	if err != nil {
		t.Fatalf("purge using daemon layout failed: n=%d err=%v", n, err)
	}
}

func TestReviewDisabledExportRetentionSurvivesJanitor(t *testing.T) {
	s, st, now := reviewCanonicalServer(t)
	if err := s.SetExportRetention(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	body := paritySuccess(t, toolCall(t, s, "export_messages", map[string]any{"chat_id": "me"}))
	path := body["path"].(string)
	*now = now.AddDate(0, 0, 8)
	n, _, err := s.SweepMedia(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("disabled MCP retention changed files: %d %v", n, err)
	}
	if err = st.JanitorOnce(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("janitor removed export despite configured retention=0: %v", err)
	}
}
