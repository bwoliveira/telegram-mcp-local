package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/appconfig"
)

func TestLoadTelegramOverrides(t *testing.T) {
	data := t.TempDir()
	store := t.TempDir()
	t.Setenv("TELEGRAM_MCP_DATA", data)
	t.Setenv("TELEGRAM_MCP_STORE_DIR", store)
	t.Setenv("TELEGRAM_MCP_TOKEN", "test-token")
	t.Setenv("TELEGRAM_MCP_PORT", "49123")
	cfg, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != data || cfg.StoreDir != store || cfg.Token != "test-token" || cfg.Port != 49123 {
		t.Fatalf("Telegram overrides not respected: %#v", cfg)
	}
}

func TestDesktopCommandKeepsTelegramDataFolder(t *testing.T) {
	cfg := config{DataDir: t.TempDir(), Port: 49123}
	command := desktopCommand(cfg)
	if command.Command == "" || len(command.Args) != 1 || command.Args[0] != "bridge" {
		t.Fatalf("invalid bridge command: %#v", command)
	}
	if command.Env["TELEGRAM_MCP_DATA"] != cfg.DataDir || command.Env["TELEGRAM_MCP_PORT"] != "49123" {
		t.Fatalf("bridge loses Telegram connection settings: %#v", command.Env)
	}
}

func TestLoadUsesSavedPanelPort(t *testing.T) {
	data := t.TempDir()
	t.Setenv("TELEGRAM_MCP_DATA", data)
	t.Setenv("TELEGRAM_MCP_PORT", "")
	if err := appconfig.Save(data, appconfig.Config{Port: 49124}); err != nil {
		t.Fatal(err)
	}
	cfg, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 49124 {
		t.Fatalf("CLI lost the panel's saved port: %d", cfg.Port)
	}
}

func TestClientConfigNeverContainsPanelBootstrapSecret(t *testing.T) {
	const secret = "synthetic-panel-bootstrap-secret-must-not-leak"
	var out bytes.Buffer
	writeConfig(&out, config{Port: 49123, DataDir: t.TempDir(), PanelBootstrapSecret: secret})
	if strings.Contains(out.String(), secret) {
		t.Fatalf("client configuration exposed panel bootstrap authority: %s", out.String())
	}
}

func TestServiceEnvironmentKeepsExplicitFakeSelection(t *testing.T) {
	t.Setenv("TELEGRAM_MCP_ENGINE", "fake")
	t.Setenv("TELEGRAM_MCP_LANG", "en")
	cfg := config{Port: 49123, Token: "test-token", DataDir: "/telegram-data", StoreDir: "/telegram-store"}
	env := serviceEnv(cfg)
	want := map[string]string{
		"TELEGRAM_MCP_PORT":      "49123",
		"TELEGRAM_MCP_DATA":      "/telegram-data",
		"TELEGRAM_MCP_STORE_DIR": "/telegram-store",
		"TELEGRAM_MCP_ENGINE":    "fake",
		"TELEGRAM_MCP_LANG":      "en",
	}
	if len(env) != len(want) {
		t.Fatalf("unexpected service variables: %#v", env)
	}
	for key, value := range want {
		if env[key] != value {
			t.Errorf("service %s = %q, want %q", key, env[key], value)
		}
	}
}
