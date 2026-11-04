// Package service manages the daemon as a per-user background service:
// systemd --user on Linux and launchd on macOS. It never installs a system-wide
// service or enables lingering.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	label    = "com.bwoliveira.telegram-mcp"
	unitName = "telegram-mcp.service"
)

type Spec struct {
	Binary string            // Absolute path of this program.
	Args   []string          // Must be exactly {"serve"}; secrets are not CLI arguments.
	Env    map[string]string // Explicitly allowlisted, non-secret daemon configuration.
	LogDir string            // Optional absolute log directory for launchd only.
}

// Runner executes a service-manager command without a shell. Tests supply a
// recording fake; production uses exec.Command(...).CombinedOutput.
type Runner func(name string, args ...string) ([]byte, error)

// Manager provides an instance-local command seam. Its zero value uses the host
// platform and real command runner. GOOS is optional and allows platform-specific
// tests to run with a fake Runner, without invoking either service manager.
type Manager struct {
	Runner Runner
	GOOS   string
}

func Install(spec Spec) (string, error) { return (Manager{}).Install(spec) }
func Uninstall() error                  { return (Manager{}).Uninstall() }
func Start() error                      { return (Manager{}).Start() }
func Stop() error                       { return (Manager{}).Stop() }

// Status returns systemd's ActiveState (e.g. active, inactive, failed), or the
// launchd job state on macOS. A failed query returns an empty string and
// an error. Inactive is a successful status query, not a command failure.
func Status() (string, error) { return (Manager{}).Status() }

func (m Manager) platform() string {
	if m.GOOS != "" {
		return m.GOOS
	}
	return runtime.GOOS
}

func (m Manager) run(name string, args ...string) ([]byte, error) {
	runner := m.Runner
	if runner == nil {
		runner = func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput()
		}
	}
	out, err := runner(name, args...)
	if err != nil {
		// Service-manager output may include previous logs or legacy units with
		// secrets. Do not copy that output into errors or logs.
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

func (m Manager) Install(spec Spec) (string, error) {
	if err := validateSpec(spec); err != nil {
		return "", err
	}
	switch m.platform() {
	case "linux":
		return m.installSystemd(spec)
	case "darwin":
		return m.installLaunchd(spec)
	default:
		return "", m.unsupported()
	}
}

func (m Manager) Uninstall() error {
	switch m.platform() {
	case "linux":
		return m.uninstallSystemd()
	case "darwin":
		path, err := launchdPath()
		if err != nil {
			return err
		}
		if err := m.stopLaunchd(path); err != nil {
			return err
		}
		return removeUnit(path)
	default:
		return m.unsupported()
	}
}

// Stop stops the daemon without removing its installed service.
func (m Manager) Stop() error {
	switch m.platform() {
	case "linux":
		_, err := m.run("systemctl", "--user", "stop", unitName)
		return err
	case "darwin":
		path, err := launchdPath()
		if err != nil {
			return err
		}
		return m.stopLaunchd(path)
	default:
		return m.unsupported()
	}
}

// Start starts an installed daemon again.
func (m Manager) Start() error {
	switch m.platform() {
	case "linux":
		_, err := m.run("systemctl", "--user", "start", unitName)
		return err
	case "darwin":
		path, err := launchdPath()
		if err != nil {
			return err
		}
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("service is not installed; run telegram-mcp service install: %w", err)
		}
		loaded, err := m.launchdLoaded()
		if err != nil {
			return err
		}
		if loaded {
			return nil
		}
		_, err = m.run("launchctl", "bootstrap", launchdDomain(), path)
		return err
	default:
		return m.unsupported()
	}
}

func (m Manager) Status() (string, error) {
	var out []byte
	var err error
	switch m.platform() {
	case "linux":
		out, err = m.run("systemctl", "--user", "show", "--property=ActiveState", "--value", unitName)
	case "darwin":
		out, err = m.run("launchctl", "print", launchdDomain()+"/"+label)
	default:
		return "", m.unsupported()
	}
	if err != nil {
		return "", err
	}
	status := strings.TrimSpace(string(out))
	if m.platform() == "darwin" {
		// launchctl print includes the job environment, possibly a legacy token.
		// Return only its known state, never the raw diagnostic dump.
		for _, line := range strings.Split(status, "\n") {
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "state = "); ok {
				switch value {
				case "running", "waiting", "exited", "not running":
					return value, nil
				}
			}
		}
	} else {
		switch status {
		case "active", "inactive", "failed", "activating", "deactivating", "reloading", "maintenance", "refreshing":
			return status, nil
		}
	}
	return "", fmt.Errorf("service manager returned an invalid status")
}

func (m Manager) unsupported() error {
	return fmt.Errorf("no service support on %s; run telegram-mcp serve manually", m.platform())
}

func safeValue(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029'
	})
}

func validateSpec(spec Spec) error {
	if !filepath.IsAbs(spec.Binary) || !safeValue(spec.Binary) {
		return fmt.Errorf("service binary must be an absolute path without control characters")
	}
	if !slices.Equal(spec.Args, []string{"serve"}) {
		return fmt.Errorf("service arguments must be exactly serve; credentials must come from the secret store")
	}
	if spec.LogDir != "" && (!filepath.IsAbs(spec.LogDir) || !safeValue(spec.LogDir)) {
		return fmt.Errorf("service log directory must be an absolute path without control characters")
	}
	for key, value := range spec.Env {
		if !safeValue(value) {
			return fmt.Errorf("service environment contains an invalid value")
		}
		switch key {
		case "TELEGRAM_MCP_DATA", "TELEGRAM_MCP_STORE_DIR":
			if !filepath.IsAbs(value) {
				return fmt.Errorf("service data directories must be absolute paths")
			}
		case "TELEGRAM_MCP_PORT":
			port, err := strconv.Atoi(value)
			if err != nil || port < 1 || port > 65535 {
				return fmt.Errorf("service port must be between 1 and 65535")
			}
		case "TELEGRAM_MCP_ENGINE":
			if value != "fake" {
				return fmt.Errorf("unsupported service engine selection")
			}
		case "TELEGRAM_MCP_LANG":
			if !slices.Contains([]string{"pt-BR", "en", "es"}, value) {
				return fmt.Errorf("unsupported service language")
			}
		case "TELEGRAM_MCP_SECRETS":
			if !slices.Contains([]string{"", "file", "keyring"}, value) {
				return fmt.Errorf("unsupported service secret backend")
			}
		default:
			// Do not echo unknown names or values: even a key can contain a secret.
			return fmt.Errorf("service environment contains a disallowed variable; credentials must come from the secret store")
		}
	}
	return nil
}

// writeUnit atomically replaces only a regular unit file, always with mode 0600.
// In particular, an existing world-readable unit cannot keep its old mode.
func writeUnit(path, content string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("service unit must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(dir); err != nil {
		return err
	} else if !info.IsDir() {
		return fmt.Errorf("service unit directory must not be a symlink")
	}
	file, err := os.CreateTemp(dir, ".telegram-mcp-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func removeUnit(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
