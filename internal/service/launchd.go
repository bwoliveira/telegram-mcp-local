package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

func launchdPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(home) || !safeValue(home) {
		return "", fmt.Errorf("invalid user home directory")
	}
	return filepath.Join(home, "Library", "LaunchAgents", label+".plist"), nil
}

func launchdDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func xmlEscape(value string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(value)
}

func (m Manager) installLaunchd(spec Spec) (string, error) {
	path, err := launchdPath()
	if err != nil {
		return "", err
	}
	logDir := spec.LogDir
	if logDir == "" {
		logDir = filepath.Join(filepath.Dir(filepath.Dir(path)), "Logs")
	}
	var args strings.Builder
	for _, arg := range append([]string{spec.Binary}, spec.Args...) {
		fmt.Fprintf(&args, "\t\t<string>%s</string>\n", xmlEscape(arg))
	}
	var env strings.Builder
	keys := make([]string, 0, len(spec.Env))
	for key := range spec.Env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		fmt.Fprintf(&env, "\t\t<key>%s</key>\n\t\t<string>%s</string>\n", xmlEscape(key), xmlEscape(spec.Env[key]))
	}
	log := xmlEscape(filepath.Join(logDir, "telegram-mcp.log"))
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key>
	<array>
%s	</array>
	<key>EnvironmentVariables</key>
	<dict>
%s	</dict>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ThrottleInterval</key><integer>10</integer>
	<key>ProcessType</key><string>Background</string>
	<key>StandardOutPath</key><string>%s</string>
	<key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, label, args.String(), env.String(), log, log)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return "", err
	}
	// A new install needs no bootout; replacing an existing job must stop it
	// successfully before changing its definition.
	if _, err := os.Lstat(path); err == nil {
		if err := m.stopLaunchd(path); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := writeUnit(path, plist); err != nil {
		return "", err
	}
	if _, err := m.run("launchctl", "bootstrap", launchdDomain(), path); err != nil {
		return "", err
	}
	return path, nil
}

// Query existence without interpreting localized command output. launchctl's
// ESRCH (3) / ENOENT service lookup (113) means the job is already unloaded;
// bus/permission failures remain errors and never become successful stops.
func (m Manager) launchdLoaded() (bool, error) {
	_, err := m.run("launchctl", "print", launchdDomain()+"/"+label)
	if err == nil {
		return true, nil
	}
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) && (exit.ExitCode() == 113 || exit.ExitCode() == 3) {
		return false, nil
	}
	return false, err
}
func (m Manager) stopLaunchd(path string) error {
	loaded, err := m.launchdLoaded()
	if err != nil {
		return err
	}
	if !loaded {
		return nil
	}
	_, err = m.run("launchctl", "bootout", launchdDomain(), path)
	return err
}
