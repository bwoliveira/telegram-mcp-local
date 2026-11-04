package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

func TestMediaPurgeRejectsTraversalAndSymlinks(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "media")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "keep.txt")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{MediaDir: root})
	if _, _, _, err := s.PurgeMedia("..", 0, 0, false); err == nil {
		t.Fatal("parent traversal accepted")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside file removed: %v", err)
	}
	link := filepath.Join(root, "alias")
	if err := os.Symlink(parent, link); err != nil {
		t.Skip(err)
	}
	inv, err := s.Inventory(context.Background())
	if err != nil || inv.Files != 0 {
		t.Fatalf("symlink in inventory: %+v %v", inv, err)
	}
	if _, _, _, err := s.PurgeMedia("alias", 0, 0, false); err == nil {
		t.Fatal("symlink root accepted")
	}
}

func TestMediaRetentionDefaultsAndDryRunOverridesConfirm(t *testing.T) {
	root := t.TempDir()
	chat := filepath.Join(root, "user_1001")
	if err := os.Mkdir(chat, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(chat, "audio.ogg")
	if err := os.WriteFile(path, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{MediaDir: root})
	if got := s.retentionDays(context.Background()); got != 30 {
		t.Fatalf("default retention=%d", got)
	}
	raw := json.RawMessage(`{"confirm":true,"dry_run":true}`)
	result, ok := s.MediaTool(context.Background(), "purge_media", raw)
	if !ok || result["isError"] != false || toolBody(t, result)["preview"] != true {
		t.Fatalf("dry run: %+v", result)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("dry run removed file: %v", err)
	}
}

func TestMediaSweepRetentionThirtyAndSevenDaysAndExplicitZero(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	settings, err := state.Open(filepath.Join(data, "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer settings.Close()
	media := filepath.Join(data, "media")
	exports := filepath.Join(data, "exports")
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s := New(Config{State: settings, MediaDir: media, ExportDir: exports, Clock: func() time.Time { return now }})
	paths := []string{filepath.Join(media, "user_1001", "old.ogg"), filepath.Join(exports, "old.ndjson")}
	ages := []int{31, 8}
	create := func() {
		for i, p := range paths {
			if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("data"), 0600); err != nil {
				t.Fatal(err)
			}
			at := now.AddDate(0, 0, -ages[i])
			if err := os.Chtimes(p, at, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	create()
	n, b, err := s.SweepMedia(ctx)
	if err != nil || n != 2 || b != 8 {
		t.Fatalf("default sweep: %d %d %v", n, b, err)
	}
	create()
	if err := s.SetRetention(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExportRetention(ctx, 0); err != nil {
		t.Fatal(err)
	}
	n, b, err = s.SweepMedia(ctx)
	if err != nil || n != 0 || b != 0 {
		t.Fatalf("disabled sweep: %d %d %v", n, b, err)
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("explicit zero removed artifact: %v", err)
		}
	}
}

func TestMediaPersistentPurgeWithoutQueueCapabilityFailsClosed(t *testing.T) {
	s, st, _ := mediaStoreServer(t)
	// Deliberately expose only the basic read seam, hiding mutable capabilities.
	s.readStore = struct{ ReadStore }{st}
	path := filepath.Join(s.mediaDir, "user_1001", "keep.ogg")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	n, _, _, err := s.PurgeMediaContext(context.Background(), "", 0, 0, true)
	if err != nil || n != 1 {
		t.Fatalf("preview blocked: %d %v", n, err)
	}
	n, _, _, err = s.PurgeMediaContext(context.Background(), "", 0, 0, false)
	if err == nil || n != 0 {
		t.Fatalf("purge ran without durable queue: %d %v", n, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("failed-closed purge removed file: %v", err)
	}
}

func TestMediaLinksRevokePurgeAndDenyRecreatedFile(t *testing.T) {
	root := t.TempDir()
	chat := filepath.Join(root, "user_1001")
	if err := os.Mkdir(chat, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(chat, "audio.ogg")
	if err := os.WriteFile(path, []byte("audio"), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{MediaDir: root})
	token := s.links.add(path, "audio/ogg", time.Minute)
	serve := func() int {
		r := httptest.NewRecorder()
		s.links.ServeHTTP(r, httptest.NewRequest(http.MethodGet, "/media/"+token, nil))
		return r.Code
	}
	if got := serve(); got != 200 {
		t.Fatalf("initial link=%d", got)
	}
	if _, _, _, err := s.PurgeMedia("user:1001", 0, 0, false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(chat, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := serve(); got != 404 {
		t.Fatalf("old capability revived after purge=%d", got)
	}
}
