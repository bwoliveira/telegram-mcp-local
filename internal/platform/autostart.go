package platform

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AppID belongs to this fork, not the upstream author's application.
const AppID = "com.bwoliveira.telegram-mcp"

// SetAutostart registers a per-user login entry. It never starts another app
// now, needs no administrator, and uses the persistent AppImage path if supplied.
func SetAutostart(on bool, executable string) error {
	if runtime.GOOS == "windows" {
		return setRunAtLogin(on, executable)
	}
	path, err := autostartPath()
	if err != nil {
		return err
	}
	if !on {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := validateExecutable(executable); err != nil {
		return err
	}
	var body []byte
	if runtime.GOOS == "darwin" {
		body = launchAgent(executable)
	} else {
		body, err = desktopAutostart(executable)
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// Atomic replacement keeps a moved/upgraded app from leaving a partial entry.
	f, err := os.CreateTemp(filepath.Dir(path), ".autostart-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// AutostartEnabled checks the exact owned entry and executable, so an upgrade
// that moves the app repairs its login path instead of trusting a stale file.
func AutostartEnabled(executable string) (bool, error) {
	if runtime.GOOS == "windows" {
		return runAtLoginEnabled(executable)
	}
	path, err := autostartPath()
	if err != nil {
		return false, err
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var want []byte
	if runtime.GOOS == "darwin" {
		want = launchAgent(executable)
	} else {
		want, err = desktopAutostart(executable)
		if err != nil {
			return false, err
		}
	}
	return bytes.Equal(body, want), nil
}

func autostartPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "LaunchAgents", AppID+".plist"), nil
	}
	if runtime.GOOS != "linux" {
		return "", errors.New("autostart is unsupported on this platform")
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "autostart", AppID+".desktop"), nil
}

func validateExecutable(executable string) error {
	// Windows validation is handled by its native command-line encoder.
	if !filepath.IsAbs(executable) {
		return errors.New("autostart executable must be absolute")
	}
	if strings.ContainsAny(executable, "\x00\n\r") {
		return errors.New("autostart executable contains a control character")
	}
	for _, r := range executable {
		if r < 0x20 || r == 0x7f {
			return errors.New("autostart executable contains a control character")
		}
	}
	return nil
}

func desktopAutostart(executable string) ([]byte, error) {
	if err := validateExecutable(executable); err != nil {
		return nil, err
	}
	// Exec has two escaping layers: Desktop Entry string then quoted argument.
	// Percent signs are field codes even inside quotes, so literal % is %%.
	token := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "`", "\\`", "$", `\$`, "%", "%%").Replace(executable)
	token = strings.ReplaceAll(token, `\`, `\\`)
	return []byte(fmt.Sprintf("[Desktop Entry]\nType=Application\nName=%s\nExec=\"%s\" --hidden\nIcon=telegram-mcp\nTerminal=false\nX-GNOME-Autostart-enabled=true\n", AppName, token)), nil
}

func launchAgent(executable string) []byte {
	escape := func(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>Label</key><string>` + escape(AppID) + `</string><key>ProgramArguments</key><array><string>` + escape(executable) + `</string><string>--hidden</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><false/></dict></plist>
`)
}
