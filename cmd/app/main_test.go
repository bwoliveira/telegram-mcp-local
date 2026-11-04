package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestVersionCommand(t *testing.T) {
	const stampedVersion = "v9.8.7-version-test"
	const helperEnv = "TELEGRAM_MCP_TEST_VERSION_COMMAND"
	if os.Getenv(helperEnv) == "1" {
		version = stampedVersion
		os.Args = []string{"telegram-mcp", "version"}
		main()
		os.Exit(0)
	}

	// Any attempt to initialize app storage must fail: version should not
	// construct the App, create logs, or require a desktop session.
	root := t.TempDir()
	blockedHome := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(blockedHome, nil, 0600); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestVersionCommand$")
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case helperEnv, "TELEGRAM_MCP_SECRETS", "TELEGRAM_MCP_DATA", "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CONFIG_HOME", "DISPLAY", "WAYLAND_DISPLAY":
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	cmd.Env = append(cmd.Env, helperEnv+"=1", "HOME="+blockedHome,
		"USERPROFILE="+blockedHome, "APPDATA="+blockedHome, "LOCALAPPDATA="+blockedHome,
		"XDG_DATA_HOME="+blockedHome, "XDG_STATE_HOME="+blockedHome,
		"XDG_CONFIG_HOME="+blockedHome)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("version command failed: %v\nstdout: %s\nstderr: %s", err, &stdout, &stderr)
	}
	if got, want := stdout.String(), stampedVersion+"\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Errorf("stderr = %q, want empty", got)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(blockedHome) {
		t.Errorf("version command created app files: %v", entries)
	}
}

// lockedBuffer collects a child's output while the test polls it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// appCommand runs this test binary as the app executable with args, isolated
// from the developer's home, display, credentials and listener.
func appCommand(t *testing.T, ctx context.Context, root string, args ...string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestTerminalSubcommandsRunHeadless$")
	cmd.Env = []string{
		"TELEGRAM_MCP_TEST_APP_ARGS=" + strings.Join(args, "\x00"),
		"HOME=" + root,
		"USERPROFILE=" + root,
		"APPDATA=" + filepath.Join(root, "roaming"),
		"LOCALAPPDATA=" + filepath.Join(root, "local"),
		"TEMP=" + root,
		"TMP=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"XDG_DATA_HOME=" + filepath.Join(root, "share"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"TMPDIR=" + root,
		"PATH=" + filepath.Join(root, "no-external-programs"),
		"TELEGRAM_MCP_DATA=" + filepath.Join(root, "data"),
		"TELEGRAM_MCP_STORE_DIR=" + filepath.Join(root, "store"),
		"TELEGRAM_MCP_ENGINE=fake",
		"TELEGRAM_MCP_LANG=en",
	}
	return cmd
}

// TestTerminalSubcommandsRunHeadless proves that an installed desktop package's
// telegram-mcp keeps the command line: serve runs the daemon with no display
// (Linux DEB and AppImage, Windows and macOS alike), and help/version print.
func TestTerminalSubcommandsRunHeadless(t *testing.T) {
	if args := os.Getenv("TELEGRAM_MCP_TEST_APP_ARGS"); args != "" {
		os.Args = append([]string{"telegram-mcp"}, strings.Split(args, "\x00")...)
		main()
		os.Exit(0)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	help := appCommand(t, ctx, t.TempDir(), "help")
	var stdout, stderr bytes.Buffer
	help.Stdout, help.Stderr = &stdout, &stderr
	if err := help.Run(); err != nil {
		t.Fatalf("help: %v\n%s", err, &stderr)
	}
	for _, want := range []string{"serve ", "service install", "open "} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("help output lacks %q: %s", want, &stdout)
		}
	}

	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM shutdown is a Unix executable contract")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	root := t.TempDir()
	serve := appCommand(t, ctx, root, "serve")
	serve.Env = append(serve.Env, "TELEGRAM_MCP_PORT="+strconv.Itoa(port))
	var logs lockedBuffer
	serve.Stdout, serve.Stderr = &logs, &logs
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- serve.Wait() }()
	ready := false
	for deadline := time.Now().Add(time.Minute); time.Now().Before(deadline) && !ready; {
		select {
		case err := <-done:
			t.Fatalf("serve exited early: %v\n%s", err, logs.String())
		case <-time.After(100 * time.Millisecond):
		}
		if strings.Contains(logs.String(), "serving MCP") {
			if conn, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
				conn.Close()
				ready = true
			}
		}
	}
	if !ready {
		_ = serve.Process.Kill()
		t.Fatalf("serve did not listen on %d:\n%s", port, logs.String())
	}
	if _, err := os.Stat(filepath.Join(root, "state", "telegram-mcp")); err == nil {
		t.Error("serve started the desktop app: its log folder exists")
	}

	// The package's bridge reaches that daemon with the credential serve
	// persisted, as the standalone command line's bridge does.
	bridgeCmd := appCommand(t, ctx, root, "bridge")
	bridgeCmd.Env = append(bridgeCmd.Env, "TELEGRAM_MCP_PORT="+strconv.Itoa(port))
	bridgeCmd.Stdin = strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}` + "\n" +
		`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" +
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}` + "\n")
	var bridgeOut, bridgeErr bytes.Buffer
	bridgeCmd.Stdout, bridgeCmd.Stderr = &bridgeOut, &bridgeErr
	if err := bridgeCmd.Run(); err != nil {
		t.Fatalf("bridge: %v\nstderr: %s", err, &bridgeErr)
	}
	tools := -1
	for _, line := range strings.Split(bridgeOut.String(), "\n") {
		var msg struct {
			ID     int `json:"id"`
			Result struct {
				Tools []json.RawMessage `json:"tools"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(line), &msg) == nil && msg.ID == 2 {
			tools = len(msg.Result.Tools)
		}
	}
	if tools != 40 {
		t.Errorf("bridge listed %d tools, want 40:\n%s\nstderr: %s", tools, &bridgeOut, &bridgeErr)
	}
	if err := serve.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve shutdown: %v\n%s", err, logs.String())
		}
	case <-ctx.Done():
		t.Fatal("serve did not stop after SIGTERM")
	}
}
