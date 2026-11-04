package service

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type recordedCommand struct {
	name string
	args []string
}

type fakeRunner struct {
	calls  []recordedCommand
	failAt int
	err    error
	output []byte
	before func(int)
}

func (f *fakeRunner) run(name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, recordedCommand{name, append([]string(nil), args...)})
	if f.before != nil {
		f.before(len(f.calls))
	}
	if f.failAt == len(f.calls) {
		return f.output, f.err
	}
	return f.output, nil
}

func newTestManager(t *testing.T) (Manager, *fakeRunner, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("USERPROFILE", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	fake := &fakeRunner{}
	return Manager{GOOS: "linux", Runner: fake.run}, fake, filepath.Join(root, "config", "systemd", "user", "telegram-mcp.service")
}

// fixturePath keeps Unix quoting fixtures absolute on the native host too.
// No command uses these paths: every foreign service manager is injected.
func fixturePath(path string) string { return filepath.VolumeName(os.TempDir()) + path }

func validSpec() Spec {
	return Spec{Binary: fixturePath("/opt/telegram-mcp"), Args: []string{"serve"}, Env: map[string]string{
		"TELEGRAM_MCP_DATA": fixturePath("/srv/telegram"), "TELEGRAM_MCP_STORE_DIR": fixturePath("/srv/telegram/store"),
		"TELEGRAM_MCP_PORT": "47831", "TELEGRAM_MCP_ENGINE": "fake", "TELEGRAM_MCP_LANG": "en",
	}}
}

func expectCommands(t *testing.T, fake *fakeRunner, commands ...[]string) {
	t.Helper()
	want := make([]recordedCommand, 0, len(commands))
	for _, args := range commands {
		want = append(want, recordedCommand{"systemctl", append([]string{"--user"}, args...)})
	}
	if len(fake.calls) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(fake.calls, want) {
		t.Fatalf("commands = %#v, want %#v", fake.calls, want)
	}
}

func TestInstallRejectsUnsafeSpecBeforeSideEffects(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Spec)
	}{
		{"token", func(s *Spec) { s.Env["TELEGRAM_MCP_TOKEN"] = "sensitive-marker" }},
		{"hash", func(s *Spec) { s.Env["TELEGRAM_API_HASH"] = "sensitive-marker" }},
		{"mcp_hash", func(s *Spec) { s.Env["TELEGRAM_MCP_API_HASH"] = "sensitive-marker" }},
		{"unknown", func(s *Spec) { s.Env["LD_PRELOAD"] = "sensitive-marker" }},
		{"env_key_injection", func(s *Spec) { s.Env["X\nExecStart=evil"] = "sensitive-marker" }},
		{"env_newline", func(s *Spec) { s.Env["TELEGRAM_MCP_DATA"] = fixturePath("/sensitive-marker\nExecStart=evil") }},
		{"env_carriage_return", func(s *Spec) { s.Env["TELEGRAM_MCP_DATA"] = fixturePath("/sensitive-marker\rRestart=no") }},
		{"env_nul", func(s *Spec) { s.Env["TELEGRAM_MCP_DATA"] = fixturePath("/sensitive-marker\x00") }},
		{"relative_data", func(s *Spec) { s.Env["TELEGRAM_MCP_DATA"] = "relative-sensitive-marker" }},
		{"port", func(s *Spec) { s.Env["TELEGRAM_MCP_PORT"] = "sensitive-marker" }},
		{"port_zero", func(s *Spec) { s.Env["TELEGRAM_MCP_PORT"] = "0" }},
		{"port_overflow", func(s *Spec) { s.Env["TELEGRAM_MCP_PORT"] = "65536" }},
		{"engine", func(s *Spec) { s.Env["TELEGRAM_MCP_ENGINE"] = "sensitive-marker" }},
		{"lang", func(s *Spec) { s.Env["TELEGRAM_MCP_LANG"] = "sensitive-marker" }},
		{"relative_binary", func(s *Spec) { s.Binary = "sensitive-marker" }},
		{"binary_newline", func(s *Spec) { s.Binary = fixturePath("/sensitive-marker\n[Install]") }},
		{"binary_nul", func(s *Spec) { s.Binary = fixturePath("/sensitive-marker\x00") }},
		{"binary_invalid_utf8", func(s *Spec) { s.Binary = fixturePath("/sensitive-marker\xff") }},
		{"binary_double_quote", func(s *Spec) { s.Binary = fixturePath(`/sensitive-marker"`) }},
		{"binary_single_quote", func(s *Spec) { s.Binary = fixturePath(`/sensitive-marker'`) }},
		{"binary_backslash", func(s *Spec) { s.Binary = fixturePath(`/sensitive-marker\`) }},
		{"token_flag", func(s *Spec) { s.Args = []string{"serve", "--token=sensitive-marker"} }},
		{"secret_arg", func(s *Spec) { s.Args = []string{"serve", "sensitive-marker"} }},
		{"arg_newline", func(s *Spec) { s.Args = []string{"serve\nExecStart=sensitive-marker"} }},
		{"missing_serve", func(s *Spec) { s.Args = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, fake, path := newTestManager(t)
			spec := validSpec()
			tt.change(&spec)
			_, err := m.Install(spec)
			if err == nil {
				t.Fatal("unsafe spec accepted")
			}
			if strings.Contains(err.Error(), "sensitive-marker") {
				t.Fatalf("secret in error: %v", err)
			}
			expectCommands(t, fake)
			if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
				t.Fatalf("invalid spec created directory: %v", err)
			}
		})
	}
}

func TestInstallSystemdQuoting(t *testing.T) {
	m, _, path := newTestManager(t)
	spec := validSpec()
	spec.Binary = fixturePath(`/opt/a space/$HOME/${USER}/%i/bin`)
	spec.Env["TELEGRAM_MCP_DATA"] = fixturePath(`/srv/a "quote"\back/$HOME/${USER}/%i`)
	if _, err := m.Install(spec); err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`ExecStart="` + fixturePath(`/opt/a space/$HOME/${USER}/%%i/bin`) + `" "serve"`,
		`Environment="TELEGRAM_MCP_DATA=` + fixturePath(`/srv/a \"quote\"\\back/$HOME/${USER}/%%i`) + `"`,
	} {
		if !strings.Contains(string(unit), want) {
			t.Errorf("missing escaped literal %q: %s", want, unit)
		}
	}
}

func TestManagerInstallCommandErrors(t *testing.T) {
	for _, n := range []int{1, 2} {
		t.Run(string(rune('0'+n)), func(t *testing.T) {
			m, fake, path := newTestManager(t)
			failure := errors.New("command failed")
			fake.failAt, fake.err, fake.output = n, failure, []byte("secret-command-output")
			_, err := m.Install(validSpec())
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "secret-command-output") {
				t.Fatal("leaked command output")
			}
			if len(fake.calls) != n {
				t.Fatalf("commands after failure = %v", fake.calls)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("keep unit for retry: %v", err)
			}
		})
	}
}

func seedUnit(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old unit"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestManagerUninstall(t *testing.T) {
	m, fake, path := newTestManager(t)
	seedUnit(t, path)
	fake.before = func(n int) {
		_, err := os.Stat(path)
		if n == 1 && err != nil {
			t.Fatalf("removed unit before disable: %v", err)
		}
		if n == 2 && !os.IsNotExist(err) {
			t.Fatalf("unit still present at reload: %v", err)
		}
	}
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	expectCommands(t, fake, []string{"disable", "--now", "telegram-mcp.service"}, []string{"daemon-reload"})
}

func TestUninstallPropagatesErrors(t *testing.T) {
	for _, n := range []int{1, 2} {
		t.Run(string(rune('0'+n)), func(t *testing.T) {
			m, fake, path := newTestManager(t)
			seedUnit(t, path)
			failure := errors.New("disable or reload failed")
			fake.failAt, fake.err = n, failure
			if err := m.Uninstall(); !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if len(fake.calls) != n {
				t.Fatalf("commands = %#v", fake.calls)
			}
			_, err := os.Stat(path)
			if n == 1 && err != nil {
				t.Fatalf("disable failure must preserve unit: %v", err)
			}
			if n == 2 && !os.IsNotExist(err) {
				t.Fatalf("reload occurs after removal: %v", err)
			}
		})
	}
}

func TestUninstallMissingUnitStillChecksDisable(t *testing.T) {
	m, fake, _ := newTestManager(t)
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	expectCommands(t, fake, []string{"disable", "--now", "telegram-mcp.service"}, []string{"daemon-reload"})
}

func TestManagerStartStopStatus(t *testing.T) {
	for _, verb := range []string{"start", "stop", "status"} {
		for _, fail := range []bool{false, true} {
			t.Run(verb+map[bool]string{false: "_ok", true: "_error"}[fail], func(t *testing.T) {
				m, fake, path := newTestManager(t)
				fake.output = []byte("inactive\n")
				failure := errors.New("no user bus")
				if fail {
					fake.failAt, fake.err = 1, failure
				}
				var err error
				switch verb {
				case "start":
					err = m.Start()
					expectCommands(t, fake, []string{"start", "telegram-mcp.service"})
				case "stop":
					err = m.Stop()
					expectCommands(t, fake, []string{"stop", "telegram-mcp.service"})
				case "status":
					var status string
					status, err = m.Status()
					if !fail && status != "inactive" {
						t.Errorf("Status = %q", status)
					}
					if fail && status != "" {
						t.Errorf("Status on failure = %q", status)
					}
					expectCommands(t, fake, []string{"show", "--property=ActiveState", "--value", "telegram-mcp.service"})
				}
				if fail && !errors.Is(err, failure) || !fail && err != nil {
					t.Fatalf("error = %v", err)
				}
				if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatalf("read/control created files: %v", err)
				}
			})
		}
	}
}

func TestSystemdConfigFallback(t *testing.T) {
	for _, config := range []string{"", "relative/config"} {
		t.Run(config, func(t *testing.T) {
			m, _, _ := newTestManager(t)
			t.Setenv("XDG_CONFIG_HOME", config)
			got, err := m.Install(validSpec())
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(os.Getenv("HOME"), ".config", "systemd", "user", "telegram-mcp.service")
			if got != want {
				t.Fatalf("path = %q, want %q", got, want)
			}
		})
	}
}

func TestInstallRefusesSymlinkUnit(t *testing.T) {
	m, fake, path := newTestManager(t)
	seedUnit(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Install(validSpec()); err == nil {
		t.Fatal("symlink unit accepted")
	}
	expectCommands(t, fake)
	out, err := os.ReadFile(target)
	if err != nil || string(out) != "untouched" {
		t.Fatalf("symlink target changed: %q, %v", out, err)
	}
}
