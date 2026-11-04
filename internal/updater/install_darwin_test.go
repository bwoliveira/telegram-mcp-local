package updater

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeApp makes a signed .app whose program prints version.
func fakeApp(t *testing.T, dir, version string) string {
	t.Helper()
	app := filepath.Join(dir, "Telegram MCP.app")
	macos := filepath.Join(app, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "main.go")
	_ = os.WriteFile(src, []byte(`package main
import "fmt"
func main() { fmt.Print("`+version+`") }`), 0o644)
	if out, err := exec.Command("go", "build", "-o", filepath.Join(macos, "Telegram MCP"), src).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	_ = os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleExecutable</key><string>Telegram MCP</string><key>CFBundleIdentifier</key><string>test.tgmcp</string></dict></plist>`), 0o644)
	if out, err := exec.Command("codesign", "--force", "--sign", "-", app).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return app
}

func TestBundleExecutableUsesInfoPlistName(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "Telegram MCP.app")
	contents := filepath.Join(bundle, "Contents")
	if err := os.MkdirAll(contents, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleExecutable</key><string>telegram-mcp</string></dict></plist>`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := bundleExecutable(bundle)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(bundle, "Contents", "MacOS", "telegram-mcp")
	if got != want {
		t.Fatalf("bundle executable = %q, want %q", got, want)
	}
}

func TestInstallDMGSwapsTheBundle(t *testing.T) {
	if _, err := exec.LookPath("hdiutil"); err != nil {
		t.Skip("needs hdiutil")
	}
	installed := fakeApp(t, t.TempDir(), "1.0.0")
	stage := t.TempDir()
	fakeApp(t, stage, "1.1.0")
	os.Remove(filepath.Join(stage, "main.go"))
	dmg := filepath.Join(t.TempDir(), "Telegram-MCP.dmg")
	if out, err := exec.Command("hdiutil", "create", "-quiet", "-volname", "Telegram MCP", "-srcfolder", stage, "-format", "UDZO", dmg).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}

	var script string
	var env map[string]string
	old := relaunch
	relaunch = func(s string, e map[string]string) error { script, env = s, e; return nil }
	defer func() { relaunch = old }()

	if err := installDMG(context.Background(), dmg, installed, filepath.Join(t.TempDir(), "updates"), "1.0.0", "1.1.0"); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(filepath.Join(installed, "Contents", "MacOS", "Telegram MCP")).Output()
	if err != nil || string(out) != "1.1.0" {
		t.Fatalf("the bundle in place runs %q (%v), want the new version", out, err)
	}
	if _, err := os.Stat(installed + ".old"); err != nil {
		t.Fatal("the old bundle must wait beside the new one until the app quits")
	}
	if !strings.Contains(script, "ROLLBACK_EXECUTABLE") || strings.Contains(script, "open -n -W") || env["TARGET"] != installed || env["OLD"] != installed+".old" || env["EXECUTABLE"] != filepath.Join(installed, "Contents", "MacOS", "Telegram MCP") || env["ROLLBACK_EXECUTABLE"] != filepath.Join(installed+".old", "Contents", "MacOS", "Telegram MCP") {
		t.Fatalf("relaunch: %q %v", script, env)
	}
}

func TestInstallDMGRefusesABrokenSignature(t *testing.T) {
	if _, err := exec.LookPath("hdiutil"); err != nil {
		t.Skip("needs hdiutil")
	}
	installed := fakeApp(t, t.TempDir(), "1.0.0")
	stage := t.TempDir()
	app := fakeApp(t, stage, "1.1.0")
	os.Remove(filepath.Join(stage, "main.go"))
	// Changed after signing: the seal no longer matches.
	_ = os.WriteFile(filepath.Join(app, "Contents", "Info.plist"), []byte("<plist/>"), 0o644)
	dmg := filepath.Join(t.TempDir(), "Telegram-MCP.dmg")
	if out, err := exec.Command("hdiutil", "create", "-quiet", "-volname", "Telegram MCP", "-srcfolder", stage, "-format", "UDZO", dmg).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	old := relaunch
	relaunch = func(string, map[string]string) error { t.Fatal("must not relaunch"); return nil }
	defer func() { relaunch = old }()
	if err := installDMG(context.Background(), dmg, installed, filepath.Join(t.TempDir(), "updates"), "1.0.0", "1.1.0"); err == nil {
		t.Fatal("a bundle with a broken signature must be refused")
	}
	if out, _ := exec.Command(filepath.Join(installed, "Contents", "MacOS", "Telegram MCP")).Output(); string(out) != "1.0.0" {
		t.Fatal("a refused update must leave the installed app alone")
	}
}
