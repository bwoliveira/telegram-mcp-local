package service

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPackageLifecycleWithIsolatedSystemctl(t *testing.T) {
	_, log := isolatedSystemctl(t)
	if err := Start(); err != nil {
		t.Fatal(err)
	}
	if err := Stop(); err != nil {
		t.Fatal(err)
	}
	if status, err := Status(); err != nil || status != "inactive" {
		t.Fatalf("Status = %q, %v", status, err)
	}
	if err := Uninstall(); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	want := "--user start telegram-mcp.service\n--user stop telegram-mcp.service\n--user show --property=ActiveState --value telegram-mcp.service\n--user disable --now telegram-mcp.service\n--user daemon-reload\n"
	if string(out) != want {
		t.Fatalf("commands = %q, want %q", out, want)
	}
}

func TestStatusStatesAndEmptyResponse(t *testing.T) {
	for _, state := range []string{"active", "inactive", "failed", "activating", "deactivating", "reloading", ""} {
		t.Run(state, func(t *testing.T) {
			m, fake, _ := newTestManager(t)
			fake.output = []byte(state + "\n")
			got, err := m.Status()
			if state == "" {
				if err == nil || got != "" {
					t.Fatalf("empty response = %q, %v", got, err)
				}
			} else if err != nil || got != state {
				t.Fatalf("status = %q, %v", got, err)
			}
		})
	}
}

func TestUnsupportedPlatformHasNoSideEffects(t *testing.T) {
	m, fake, path := newTestManager(t)
	m.GOOS = "windows"
	if _, err := m.Install(validSpec()); err == nil {
		t.Fatal("Install succeeded")
	}
	if err := m.Uninstall(); err == nil {
		t.Fatal("Uninstall succeeded")
	}
	if err := m.Start(); err == nil {
		t.Fatal("Start succeeded")
	}
	if err := m.Stop(); err == nil {
		t.Fatal("Stop succeeded")
	}
	if _, err := m.Status(); err == nil {
		t.Fatal("Status succeeded")
	}
	expectCommands(t, fake)
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("unsupported platform created files: %v", err)
	}
}

func TestServicePathsRejectInvalidConfiguration(t *testing.T) {
	for _, env := range []map[string]string{
		{"XDG_CONFIG_HOME": "", "HOME": "", "USERPROFILE": ""},
		{"XDG_CONFIG_HOME": "", "HOME": "relative", "USERPROFILE": "relative"},
		{"XDG_CONFIG_HOME": fixturePath("/invalid\nconfig")},
	} {
		m, fake, _ := newTestManager(t)
		for key, value := range env {
			t.Setenv(key, value)
		}
		if _, err := m.Install(validSpec()); err == nil {
			t.Fatal("invalid path accepted by install")
		}
		if err := m.Uninstall(); err == nil {
			t.Fatal("invalid path accepted by uninstall")
		}
		expectCommands(t, fake)
	}
}

func TestInstallFilesystemFailureDoesNotRunCommands(t *testing.T) {
	for _, kind := range []string{"parent_file", "unit_directory", "symlink_directory"} {
		t.Run(kind, func(t *testing.T) {
			m, fake, path := newTestManager(t)
			switch kind {
			case "parent_file":
				if err := os.WriteFile(os.Getenv("XDG_CONFIG_HOME"), []byte("file"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "unit_directory":
				if err := os.MkdirAll(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink_directory":
				if err := os.MkdirAll(filepath.Dir(filepath.Dir(path)), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Dir(path)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := m.Install(validSpec()); err == nil {
				t.Fatal("filesystem error ignored")
			}
			expectCommands(t, fake)
		})
	}
}

func TestUninstallRemovalFailureDoesNotReload(t *testing.T) {
	m, fake, path := newTestManager(t)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "child"), []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.Uninstall(); err == nil {
		t.Fatal("removal failure ignored")
	}
	expectCommands(t, fake, []string{"disable", "--now", "telegram-mcp.service"})
}

func TestSystemdQuoteArgumentDollar(t *testing.T) {
	got := systemdQuote(`$HOME/${USER}/%i/"quoted"\path`, true)
	want := `"$$HOME/$${USER}/%%i/\"quoted\"\\path"`
	if got != want {
		t.Fatalf("argument quoting = %q, want %q", got, want)
	}
}

// This is a parser-only check; systemd-analyze verify does not start or install
// services. All service-manager commands above still use a recording Runner.
// It caught the subtle error of doubling dollars in the executable path: unlike
// subsequent arguments, systemd does not expand dollars in the first token.
func TestSystemdUnitOfflineSyntax(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux systemd syntax")
	}
	analyzer, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("optional offline systemd parser is unavailable")
	}
	m, fake, path := newTestManager(t)
	binary := filepath.Join(t.TempDir(), `a space-$HOME-${USER}-%i`)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := validSpec()
	spec.Binary = binary
	if _, err := m.Install(spec); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(analyzer, "verify", "--man=no", path).CombinedOutput()
	if err != nil {
		t.Fatalf("offline syntax verification: %v: %s", err, out)
	}
	expectCommands(t, fake, []string{"daemon-reload"}, []string{"enable", "telegram-mcp.service"}, []string{"restart", "telegram-mcp.service"})
}

func TestLaunchdUsesInjectedRunnerAndEscapesXML(t *testing.T) {
	m, fake, _ := newTestManager(t)
	m.GOOS = "darwin"
	spec := validSpec()
	spec.Binary = fixturePath(`/opt/a&<b>"$HOME%/telegram-mcp`)
	spec.Env["TELEGRAM_MCP_DATA"] = fixturePath(`/srv/a&<b>"$HOME%`)
	path, err := m.Install(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(os.Getenv("HOME"), "Library", "LaunchAgents", label+".plist")
	if path != want {
		t.Fatalf("plist path = %q, want %q", path, want)
	}
	unit, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), `a&amp;&lt;b&gt;&quot;$HOME%`) {
		t.Fatalf("XML not escaped: %s", unit)
	}
	bootstrap := recordedCommand{"launchctl", []string{"bootstrap", launchdDomain(), path}}
	if !reflect.DeepEqual(fake.calls, []recordedCommand{bootstrap}) {
		t.Fatalf("install commands = %#v", fake.calls)
	}
	if _, err := m.Install(spec); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(); err != nil {
		t.Fatal(err)
	}
	fake.output = []byte("job description\n  state = running\n  TELEGRAM_MCP_TOKEN = synthetic-legacy-secret\n")
	if status, err := m.Status(); err != nil || status != "running" {
		t.Fatalf("Status = %q, %v", status, err)
	}
	if err := m.Uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("uninstall did not remove plist: %v", err)
	}
	wantCalls := []recordedCommand{bootstrap,
		{"launchctl", []string{"print", launchdDomain() + "/" + label}},
		{"launchctl", []string{"bootout", launchdDomain(), path}}, bootstrap,
		{"launchctl", []string{"print", launchdDomain() + "/" + label}},
		{"launchctl", []string{"print", launchdDomain() + "/" + label}},
		{"launchctl", []string{"bootout", launchdDomain(), path}},
		{"launchctl", []string{"print", launchdDomain() + "/" + label}},
		{"launchctl", []string{"print", launchdDomain() + "/" + label}},
		{"launchctl", []string{"bootout", launchdDomain(), path}},
	}
	if !reflect.DeepEqual(fake.calls, wantCalls) {
		t.Fatalf("launchd commands = %#v, want %#v", fake.calls, wantCalls)
	}
}

func TestLaunchdPropagatesCommandFailure(t *testing.T) {
	for _, verb := range []string{"install", "start", "stop", "uninstall", "status"} {
		t.Run(verb, func(t *testing.T) {
			m, fake, _ := newTestManager(t)
			m.GOOS = "darwin"
			path, err := launchdPath()
			if err != nil {
				t.Fatal(err)
			}
			seedUnit(t, path)
			failure := errors.New("launchctl failed")
			fake.failAt, fake.err = 1, failure
			switch verb {
			case "install":
				_, err = m.Install(validSpec())
			case "start":
				err = m.Start()
			case "stop":
				err = m.Stop()
			case "uninstall":
				err = m.Uninstall()
			case "status":
				_, err = m.Status()
			}
			if !errors.Is(err, failure) {
				t.Fatalf("error = %v", err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("failed command removed plist: %v", err)
			}
		})
	}
}

func TestSecretBackendValidation(t *testing.T) {
	for _, value := range []string{"", "file", "keyring", "auto", "secret-marker"} {
		t.Run(value, func(t *testing.T) {
			m, fake, _ := newTestManager(t)
			spec := validSpec()
			spec.Env["TELEGRAM_MCP_SECRETS"] = value
			_, err := m.Install(spec)
			valid := value == "" || value == "file" || value == "keyring"
			if valid && err != nil || !valid && err == nil {
				t.Fatalf("backend %q: %v", value, err)
			}
			if !valid {
				expectCommands(t, fake)
			}
		})
	}
}
