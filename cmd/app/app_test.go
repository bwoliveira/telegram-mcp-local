package main

import (
	"bytes"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/daemon"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

func TestDesktopStartsWithPersistedCredential(t *testing.T) {
	root := testutil.IsolateHome(t)
	testutil.NativeSecrets(t, filepath.Join(root, "data"))
	t.Setenv("TELEGRAM_MCP_DATA", filepath.Join(root, "data"))
	t.Setenv("TELEGRAM_MCP_STORE_DIR", "")
	t.Setenv("TELEGRAM_MCP_ENGINE", "fake")
	t.Setenv("TELEGRAM_MCP_TOKEN", "")
	a := &App{dataDir: filepath.Join(root, "data"), storeDir: filepath.Join(root, "data", "store"), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a.startDaemon()
	d := a.current()
	if d == nil {
		t.Fatal("desktop failed to generate and persist its HTTP credential")
	}
	defer a.shutdown()
	if d.Engine().Status().State != engine.StateConnected {
		t.Fatal("fake engine not connected")
	}
	token, err := service.ResolveToken(a.dataDir, "", false)
	if err != nil || token == "" {
		t.Fatal("bridge cannot resolve desktop credential", err)
	}
	req := httptest.NewRequest("GET", "http://127.0.0.1/api/status", nil)
	response := httptest.NewRecorder()
	d.HTTPHandler().ServeHTTP(response, req)
	if response.Code != 401 && response.Code != 403 {
		t.Fatalf("unauthenticated API = %d", response.Code)
	}
}

func TestInstanceIDIsValidForDBus(t *testing.T) {
	t.Setenv("TELEGRAM_MCP_DATA", "")
	if instanceID() != bundleID {
		t.Fatal("default instance ID changed")
	}
	seen := map[string]bool{}
	for n := range 128 {
		t.Setenv("TELEGRAM_MCP_DATA", fmt.Sprintf("/isolated/fixture-%d", n))
		id := instanceID()
		if seen[id] {
			t.Fatal("data folders share an instance", id)
		}
		seen[id] = true
		for _, component := range strings.Split(id, ".") {
			if component == "" || component[0] >= '0' && component[0] <= '9' {
				t.Fatal("invalid D-Bus component", id)
			}
		}
	}
}

func TestStartupFailureEscapesHTML(t *testing.T) {
	h := startFailure(os.ErrPermission, "<private>&log")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if bytes.Contains(w.Body.Bytes(), []byte("<private>")) || !bytes.Contains(w.Body.Bytes(), []byte("&lt;private&gt;")) {
		t.Fatal("log path not escaped")
	}
}

func TestTrayIconsHaveDistinctStates(t *testing.T) {
	source, err := png.Decode(bytes.NewReader(appIcon))
	if err != nil {
		t.Fatal(err)
	}
	if source.Bounds().Dx() < 128 {
		t.Fatal("app icon must be high resolution")
	}
	seen := map[string]bool{}
	for tone, c := range toneColours {
		for _, icon := range [][]byte{tint(trayGlyph, c), templateIcon(trayGlyph, tone)} {
			decoded, err := png.Decode(bytes.NewReader(icon))
			if err != nil || decoded.Bounds().Empty() {
				t.Fatal("invalid tray PNG", tone, err)
			}
			if seen[string(icon)] {
				t.Fatalf("indistinguishable tray state %s", tone)
			}
			seen[string(icon)] = true
		}
	}
}

func TestTrayPresentation(t *testing.T) {
	i18n.Set("en")
	defer i18n.Set("")
	for _, tc := range []struct {
		name              string
		state             engine.ConnState
		tone, title, want string
	}{
		{"connected", engine.StateConnected, "ok", "Connected", "Connected"},
		{"disconnected", engine.StatePaused, "warn", "Paused", "Disconnected"},
		{"pairing", engine.StatePaused, "warn", i18n.T("Conectando o Telegram"), i18n.T("Conectando o Telegram")},
		{"login required", engine.StateNotAuthorized, "fail", "Not connected", "Not connected · Log in"},
		{"revoked", engine.StateRevoked, "fail", "Session ended", "Session ended · Log in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tone, label := trayPresentation(daemon.Status{Tone: tc.tone, Title: tc.title, Sync: string(tc.state)})
			if tone != tc.tone || label != tc.want {
				t.Fatalf("got %q %q", tone, label)
			}
		})
	}
	if tone, _ := trayPresentation(daemon.Status{Tone: "unknown"}); tone != "fail" {
		t.Fatal("unknown status must fail closed")
	}
}
