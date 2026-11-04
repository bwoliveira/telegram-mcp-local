package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

func TestConfigModeUsesEphemeralSessionCSRFAndExplicitConfirmation(t *testing.T) {
	cfg := config{DataDir: filepath.Join(t.TempDir(), "data")}
	testutil.NativeSecrets(t, cfg.DataDir)
	var err error
	cfg, err = credentialConfig(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	cfg.PanelBootstrapSecret, err = httpserver.EnsurePanelBootstrapSecret(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	var changed int
	h := httpserver.Handler(mcp.New(mcp.Config{}), httpserver.Options{Token: cfg.Token, PanelBootstrapSecret: cfg.PanelBootstrapSecret, Register: func(mux *http.ServeMux) {
		mux.HandleFunc("POST /api/mode", func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" || r.Header.Get("X-CSRF-Token") == "" {
				t.Error("mode must use CSRF session, not bearer")
			}
			var body struct {
				Mode    string `json:"mode"`
				Confirm bool   `json:"confirm"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Mode != "read_write" || !body.Confirm {
				t.Error("mode change not explicitly confirmed")
			}
			changed++
			w.WriteHeader(204)
		})
	}})
	server := httptest.NewServer(h)
	defer server.Close()
	cfg.Port = testServerConfig(t, server).Port
	if err := configCmd(context.Background(), cfg, []string{"set", "mode", "read_write"}); err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatal("mode not changed", changed)
	}
	if err := configCmd(context.Background(), cfg, []string{"set", "mode", "invalid"}); err == nil {
		t.Fatal("invalid mode accepted")
	}
}
