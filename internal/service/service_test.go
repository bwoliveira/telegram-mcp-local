package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The package API is exercised only with an isolated executable. No command
// lookup can reach a real systemctl or launchctl, even before Runner exists.
func isolatedSystemctl(t *testing.T) (config, log string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("systemd package API")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	config = filepath.Join(root, "config")
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	log = filepath.Join(root, "commands")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SERVICE_TEST_COMMAND_LOG\"\ncase \"$2\" in show) printf 'inactive\\n';; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "systemctl"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("PATH", bin)
	t.Setenv("SERVICE_TEST_COMMAND_LOG", log)
	return config, log
}

func TestInstallUsesXDGConfigHome(t *testing.T) {
	config, log := isolatedSystemctl(t)
	path, err := Install(Spec{Binary: "/opt/telegram-mcp", Args: []string{"serve"}})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(config, "systemd", "user", "telegram-mcp.service")
	if path != want {
		t.Fatalf("installed path = %q, want XDG path %q", path, want)
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "--user daemon-reload\n--user enable telegram-mcp.service\n--user restart telegram-mcp.service\n" {
		t.Fatalf("commands = %q", out)
	}
	unit, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), `ExecStart="/opt/telegram-mcp" "serve"`) {
		t.Fatalf("missing literal command: %s", unit)
	}
}
