// Legacy literals are isolated here to verify that inherited branding and APIs stay absent.
package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestManagerInstall(t *testing.T) {
	m, fake, path := newTestManager(t)
	t.Setenv("TELEGRAM_MCP_TOKEN", "inherited-token-marker")
	t.Setenv("TELEGRAM_API_HASH", "inherited-hash-marker")
	fake.before = func(_ int) {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unit must exist before reload/enable: %v", err)
		}
	}
	got, err := m.Install(validSpec())
	if err != nil || got != path {
		t.Fatalf("Install = %q, %v", got, err)
	}
	expectCommands(t, fake, []string{"daemon-reload"}, []string{"enable", "telegram-mcp.service"}, []string{"restart", "telegram-mcp.service"})
	unit, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Description=Telegram MCP on localhost", "Restart=always", "WantedBy=default.target", `Environment="TELEGRAM_MCP_PORT=47831"`, "UMask=0077"} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("unit lacks %q: %s", want, unit)
		}
	}
	for _, secret := range []string{"wacli", "inherited-token-marker", "inherited-hash-marker"} {
		if strings.Contains(string(unit), secret) {
			t.Errorf("unit contains %q", secret)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("unit mode = %o", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o700 {
		t.Errorf("directory mode = %o", info.Mode().Perm())
	}
	// Updating a unit is deterministic and replaces old, overly broad permissions.
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(validSpec()); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(unit) != string(again) {
		t.Fatal("unit rendering is nondeterministic")
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("updated unit mode = %o", info.Mode().Perm())
	}
}
