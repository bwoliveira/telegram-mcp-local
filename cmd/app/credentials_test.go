package main

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/daemon"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

func TestDesktopFirstRunCredentialRetryAndQR(t *testing.T) {
	root := testutil.IsolateHome(t)
	testutil.NativeSecrets(t, filepath.Join(root, "data"), "primary")
	t.Setenv("TELEGRAM_MCP_ENGINE", "")
	t.Setenv("TELEGRAM_MCP_DATA", filepath.Join(root, "data"))
	t.Setenv("TELEGRAM_API_ID", "")
	t.Setenv("TELEGRAM_API_HASH", "")
	a := &App{dataDir: filepath.Join(root, "data"), storeDir: filepath.Join(root, "store"), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Without credentials, the normal production startup must stop before
	// constructing a real engine or contacting Telegram.
	a.startDaemon()
	if a.current() != nil {
		t.Fatal("missing credentials did not block production startup")
	}
	w := httptest.NewRecorder()
	a.handler.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `name="api_hash"`) {
		t.Fatal("first-run form not shown")
	}
	submit := func(id, hash string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/credentials", strings.NewReader(url.Values{"api_id": {id}, "api_hash": {hash}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		a.handler.ServeHTTP(w, r)
		return w
	}
	if w := submit("0", strings.Repeat("a", 32)); w.Code != 400 || a.current() != nil {
		t.Fatal("invalid credentials started daemon")
	}
	// The retry must use fakeengine; no real Telegram connection is allowed.
	t.Setenv("TELEGRAM_MCP_ENGINE", "fake")
	if w := submit("123", strings.Repeat("a", 32)); w.Code != 200 || a.current() == nil {
		t.Fatal("valid credentials did not retry startup")
	}
	defer a.shutdown()
	id, hash, _, err := daemon.PrepareAccountCredentials(a.dataDir, "primary")
	if err != nil || id != 123 || string(hash) != strings.Repeat("a", 32) {
		t.Fatal("credentials not persisted")
	}
	// Exercise QR through the replacement app handler with the fake engine only.
	if err := a.current().Engine().LogOut(t.Context()); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.handler.ServeHTTP(w, httptest.NewRequest("POST", "/api/pair", strings.NewReader(`{}`)))
	if w.Code != 200 || a.current().Engine().Login().State != engine.LoginShowQR {
		t.Fatal("QR flow unreachable after setup")
	}
	if !a.credentialsReady() {
		t.Fatal("next startup did not reuse vault")
	}
}
