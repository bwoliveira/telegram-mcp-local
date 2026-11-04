//go:build windows

package main

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

// This exercises the real Windows ACL and Credential Manager read. Its unique
// account namespace is empty; no credentials are saved and no engine may start.
func TestWindowsFirstRunCredentialSetup(t *testing.T) {
	dir := t.TempDir()
	testutil.NativeSecrets(t, dir, "primary")
	t.Setenv("TELEGRAM_API_ID", "")
	t.Setenv("TELEGRAM_API_HASH", "")
	t.Setenv("TELEGRAM_MCP_ENGINE", "")
	a := &App{dataDir: dir, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.startDaemon()
	if a.current() != nil {
		t.Fatal("missing credentials started a Telegram engine")
	}
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	html := w.Body.String()
	if w.Code != 200 || !strings.Contains(html, `name="api_id"`) || !strings.Contains(html, `name="api_hash"`) {
		t.Fatalf("first-use HTML: status=%d api_id=%v api_hash=%v", w.Code, strings.Contains(html, `name="api_id"`), strings.Contains(html, `name="api_hash"`))
	}
	t.Log("native internal handler: status=200, HTML fields api_id and api_hash present, no Telegram engine")
}
