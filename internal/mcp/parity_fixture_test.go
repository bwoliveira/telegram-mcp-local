package mcp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// This fixture uses the real private SQLite adapter and synthetic engine only.
// No inherited index/state is configured: dispatch must reach migrated tools.
func parityServer(t *testing.T) (*Server, *fake.Controller, *store.Store) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(root, "store.db"), "primary", store.WithClock(fake.Epoch))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctrl, err := fake.New(engine.Config{Account: "primary", Mode: engine.ModeReadWrite, Clock: fake.Epoch, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	f := ctrl.(*fake.Controller)
	s := New(Config{Engine: f, ReadStore: st, Clock: fake.Epoch, MediaDir: filepath.Join(root, "media"), ExportDir: filepath.Join(root, "exports")})
	return s, f, st
}

func paritySuccess(t *testing.T, result map[string]any) map[string]any {
	t.Helper()
	body := toolBody(t, result)
	// Inspect the error, not data such as status.secret_chats="unsupported".
	if err, ok := body["error"].(string); ok {
		lower := strings.ToLower(err)
		if strings.Contains(lower, "not implemented") || strings.Contains(lower, "unsupported") || strings.Contains(lower, "not supported") {
			t.Fatalf("unexpected stub/unsupported fallback: %s", err)
		}
	}
	if result["isError"] != false {
		t.Fatalf("tool failed: %+v", result)
	}
	return body
}

func parityWaitHistory(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if job := s.HistoryStatus(); job != nil && job.FinishedAt != nil {
			if len(job.Failures) > 0 {
				t.Fatalf("history failed: %+v", job)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("history worker did not finish")
}
