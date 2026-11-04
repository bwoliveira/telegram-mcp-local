package platform

import (
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAutostartDocuments(t *testing.T) {
	exe := `/Applications/MCP & Tools/Telegram MCP`
	body := launchAgent(exe)
	var values []string
	d := xml.NewDecoder(strings.NewReader(string(body)))
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if el, ok := tok.(xml.StartElement); ok && el.Name.Local == "string" {
			var s string
			if err := d.DecodeElement(&s, &el); err != nil {
				t.Fatal(err)
			}
			values = append(values, s)
		}
	}
	if len(values) != 3 || values[0] != AppID || values[1] != exe || values[2] != "--hidden" {
		t.Fatalf("plist values: %q", values)
	}
	if !strings.Contains(string(body), "<key>RunAtLoad</key><true/>") || strings.Contains(string(body), "KeepAlive</key><true") {
		t.Fatal("must launch once at login, not respawn")
	}
	desktopPath := `/opt/MCP 100%/quoted"$` + "`" + `\app`
	if runtime.GOOS == "windows" {
		desktopPath = "C:" + desktopPath
	}
	entry, err := desktopAutostart(desktopPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entry), `100%%`) || !strings.Contains(string(entry), `\\"`) || !strings.Contains(string(entry), ` --hidden`) {
		t.Fatalf("unsafe Exec escaping: %s", entry)
	}
	for _, bad := range []string{"", "relative/app", "/opt/app\nHidden=true", "/opt/app\r", "/opt/app\x00"} {
		if _, err := desktopAutostart(bad); err == nil {
			t.Fatalf("accepted invalid executable %q", bad)
		}
	}
}

func TestAutostartLinuxLifecycle(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux XDG integration")
	}
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	exe := filepath.Join(root, "Telegram MCP.AppImage")
	if err := SetAutostart(true, exe); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "autostart", AppID+".desktop")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `Exec="`+exe+`" --hidden`) {
		t.Fatalf("unexpected entry: %s", body)
	}
	on, err := AutostartEnabled(exe)
	if err != nil || !on {
		t.Fatalf("enabled = %v, %v", on, err)
	}
	if on, err := AutostartEnabled(exe + ".moved"); err != nil || on {
		t.Fatalf("stale path enabled = %v, %v", on, err)
	}
	if err := SetAutostart(false, exe); err != nil {
		t.Fatal(err)
	}
	if on, err := AutostartEnabled(exe); err != nil || on {
		t.Fatalf("disabled = %v, %v", on, err)
	}
	if err := SetAutostart(false, exe); err != nil {
		t.Fatal("disable must be idempotent", err)
	}
}
