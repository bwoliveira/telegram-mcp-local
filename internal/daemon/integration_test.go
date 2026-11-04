package daemon

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

func TestProductionCredentialsPersistWithoutEnvironmentOrUnitSecrets(t *testing.T) {
	t.Setenv("TELEGRAM_API_ID", "12345")
	t.Setenv("TELEGRAM_API_HASH", strings.Repeat("a", 32))
	dir := filepath.Join(t.TempDir(), "data")
	testutil.NativeSecrets(t, dir, "primary")
	id, hash, session, err := PrepareAccountCredentials(dir, "primary")
	if err != nil || id != 12345 || hash.Reveal() != strings.Repeat("a", 32) || session == nil {
		t.Fatal("credential preparation failed", err)
	}
	t.Setenv("TELEGRAM_API_ID", "")
	t.Setenv("TELEGRAM_API_HASH", "")
	id, hash, _, err = PrepareAccountCredentials(dir, "primary")
	if err != nil || id != 12345 || hash.Reveal() != strings.Repeat("a", 32) {
		t.Fatal("first-use credentials did not persist", err)
	}
	s, err := service.AccountSecrets(dir, "primary")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(secrets.Session); err != secrets.ErrNotFound {
		t.Fatal("preparation loaded/created a session", err)
	}
	t.Setenv("TELEGRAM_API_HASH", strings.Repeat("b", 32))
	if _, _, _, err = PrepareAccountCredentials(dir, "primary"); err == nil || strings.Contains(err.Error(), strings.Repeat("b", 32)) {
		t.Fatal("conflicting input not refused safely")
	}
	// Preparing credentials never constructs a Telegram client or socket.
}

func TestDaemonBootSweepsBeforeFactoryAndUsesRealReadStore(t *testing.T) {
	cfg := testConfig(t, 0)
	ctx := context.Background()
	path := filepath.Join(cfg.DataDir, "accounts", "primary", "store.db")
	s, err := store.Open(ctx, path, "primary")
	if err != nil {
		t.Fatal(err)
	}
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 900}
	expired := time.Now().Add(-time.Minute)
	key := engine.MessageKey{Account: "primary", Peer: peer, ID: 1}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Source: engine.SourceBackfill, Received: time.Now(), Messages: []engine.Message{{Key: key, Text: "expired boot marker", Date: expired.Add(-time.Minute), ExpiresAt: &expired}}}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	cfg.Factory = func(c engine.Config) (engine.Controller, error) {
		if _, ok := c.Store.(*store.Store); !ok {
			t.Fatal("factory did not get real store")
		}
		if _, ok, err := c.Store.Message(ctx, key); err != nil || ok {
			t.Fatal("engine created before boot expiration barrier", err)
		}
		return fake.New(c)
	}
	d, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	body, isErr := callTool(t, d.Port(), "search_messages", map[string]any{"query": "IGNORE"})
	if isErr || body["count"] == float64(0) {
		t.Fatal("MCP did not use real local store", body)
	}
	rec := httptest.NewRecorder()
	d.AppHandler().ServeHTTP(rec, httptest.NewRequest("GET", "wails://localhost/api/panel/status", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "data-conversation-coverage") {
		t.Fatal("store coverage absent from panel")
	}
}

func TestDaemonRebuildsTerminalControllerAndKeepsStableCapabilities(t *testing.T) {
	cfg := testConfig(t, 0)
	var count atomic.Int32
	cfg.Factory = func(c engine.Config) (engine.Controller, error) {
		n := count.Add(1)
		f, err := fake.New(c)
		if err != nil {
			return nil, err
		}
		if n > 1 {
			if err := f.LogOut(context.Background()); err != nil {
				return nil, err
			}
		}
		return f, nil
	}
	d, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	stable := d.Engine()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := stable.Subscribe(ctx, engine.SubscribeOptions{Buffer: 64})
	old := stable.(*managedController).controller()
	if err := stable.LogOut(ctx); err != nil {
		t.Fatal(err)
	}
	if err := stable.StartLogin(ctx, engine.LoginRequest{Method: engine.LoginPhone, Phone: "+15550000000"}); err != nil {
		t.Fatal(err)
	}
	if stable != d.Engine() || count.Load() != 2 || old == stable.(*managedController).controller() {
		t.Fatal("terminal controller was not replaced through stable capability")
	}
	if err := stable.SubmitCode(ctx, engine.Secret("00000")); err != nil {
		t.Fatal(err)
	}
	if err := stable.SubmitPassword(ctx, engine.Secret("offline-only")); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case e := <-events:
			if e.Kind == engine.EventAuth && e.Auth != nil && e.Auth.State == engine.AuthAuthorized {
				return
			}
		case <-deadline:
			t.Fatal("subscriber did not survive controller generation")
		}
	}
}
