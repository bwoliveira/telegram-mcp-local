// Legacy literals are isolated here to verify that inherited branding and APIs stay absent.
package cli

import (
	"github.com/bwoliveira/telegram-mcp-local/internal/appconfig"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadIsolatesTelegramDataFromWhatsApp(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"TELEGRAM_MCP_DATA", "TELEGRAM_MCP_STORE_DIR", "TELEGRAM_MCP_TOKEN", "TELEGRAM_MCP_PORT"} {
		t.Setenv(key, "")
	}
	t.Setenv("WHATSAPP_MCP_DATA", filepath.Join(home, "whatsapp-data"))
	t.Setenv("WHATSAPP_MCP_TOKEN", "legacy-token")
	t.Setenv("WHATSAPP_MCP_PORT", "12345")
	t.Setenv("WACLI_STORE_DIR", filepath.Join(home, "whatsapp-store"))
	t.Setenv("WACLI_BIN", "/not/a/telegram/engine")
	if err := os.MkdirAll(filepath.Join(home, ".wacli"), 0o700); err != nil {
		t.Fatal(err)
	}
	cfg, err := load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != filepath.Join(home, ".telegram-mcp") {
		t.Fatalf("data directory = %q", cfg.DataDir)
	}
	if cfg.StoreDir != filepath.Join(home, ".telegram-mcp", "store") {
		t.Fatalf("store directory = %q", cfg.StoreDir)
	}
	if cfg.Token != "" || cfg.Port != appconfig.DefaultPort {
		t.Fatalf("legacy credentials or port imported: %#v", cfg)
	}
}

// legacyToolName rejects inherited tool aliases in the executable contract.
func legacyToolName(name string, oldTools []string) bool {
	return slices.Contains(oldTools, name) || strings.Contains(name, "whatsapp") || strings.Contains(name, "wacli")
}
