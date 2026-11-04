package service

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func systemdPath() (string, error) {
	config := os.Getenv("XDG_CONFIG_HOME")
	// XDG specifies absolute paths; relative values are ignored, not resolved
	// against the CLI's working directory.
	if !filepath.IsAbs(config) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("user home must be an absolute path")
		}
		config = filepath.Join(home, ".config")
	}
	if !safeValue(config) {
		return "", fmt.Errorf("invalid service configuration directory")
	}
	return filepath.Join(config, "systemd", "user", unitName), nil
}

// systemdQuote uses systemd.syntax quoting, not shell or Go string quoting.
// Percent specifiers apply in both Environment and ExecStart. Dollar expansion
// applies to command arguments only, NOT the executable (the first token).
// Environment values also keep dollar signs literally. Callers reject control
// characters before rendering.
func systemdQuote(value string, command bool) string {
	value = strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`).Replace(value)
	if command {
		value = strings.ReplaceAll(value, "$", "$$")
	}
	return `"` + value + `"`
}

func (m Manager) installSystemd(spec Spec) (string, error) {
	// systemd rejects these characters in executable paths even when they are
	// syntactically quoted. Environment values can still contain them safely.
	if strings.ContainsAny(spec.Binary, "\\\"'") {
		return "", fmt.Errorf("systemd executable path must not contain quotes or backslashes")
	}
	path, err := systemdPath()
	if err != nil {
		return "", err
	}
	var unit strings.Builder
	unit.WriteString("[Unit]\nDescription=Telegram MCP on localhost\n\n[Service]\n")
	fmt.Fprintf(&unit, "ExecStart=%s %s\n", systemdQuote(spec.Binary, false), systemdQuote("serve", true))
	keys := make([]string, 0, len(spec.Env))
	for key := range spec.Env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		fmt.Fprintf(&unit, "Environment=%s\n", systemdQuote(key+"="+spec.Env[key], false))
	}
	unit.WriteString("UMask=0077\nRestart=always\nRestartSec=5\n\n[Install]\nWantedBy=default.target\n")
	if err := writeUnit(path, unit.String()); err != nil {
		return "", err
	}
	if _, err := m.run("systemctl", "--user", "daemon-reload"); err != nil {
		return "", err
	}
	if _, err := m.run("systemctl", "--user", "enable", unitName); err != nil {
		return "", err
	}
	if _, err := m.run("systemctl", "--user", "restart", unitName); err != nil {
		return "", err
	}
	return path, nil
}

func (m Manager) uninstallSystemd() error {
	path, err := systemdPath()
	if err != nil {
		return err
	}
	// Never remove the unit when disable/stop failed: it may still be running.
	if _, err := m.run("systemctl", "--user", "disable", "--now", unitName); err != nil {
		return err
	}
	if err := removeUnit(path); err != nil {
		return err
	}
	_, err = m.run("systemctl", "--user", "daemon-reload")
	return err
}
