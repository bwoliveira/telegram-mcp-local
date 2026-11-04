package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
)

// Exercise the production daemon wiring with a fake engine, actual TCP listener
// and a normal browser cookie jar. Nothing touches the installed daemon/vault.
func TestBrowserSessionSurvivesDaemonRestart(t *testing.T) {
	cfg := testConfig(t, 0)
	start := func() *Daemon {
		t.Helper()
		d, err := Start(cfg)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	d := start()
	running := true
	defer func() {
		if running {
			d.Stop()
		}
	}()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	base := "http://127.0.0.1:" + strconv.Itoa(d.Port())
	req, err := http.NewRequest("POST", base+"/api/session/bootstrap", nil)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := httpserver.ReadPanelBootstrapSecret(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(httpserver.PanelBootstrapHeader, secret)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var link struct {
		URL string `json:"url"`
	}
	err = json.NewDecoder(resp.Body).Decode(&link)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("bootstrap: %d %v", resp.StatusCode, err)
	}
	resp, err = client.Get(link.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	path := filepath.Join(cfg.DataDir, "accounts", "primary", "browser", "browser-sessions.json")
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("production session snapshot: %v %v", info, err)
	}
	d.Stop()
	running = false
	if err := os.Remove(httpserver.PanelBootstrapSecretPath(cfg.DataDir)); err != nil {
		t.Fatal(err)
	}
	d = start()
	running = true
	rotated, err := httpserver.ReadPanelBootstrapSecret(cfg.DataDir)
	if err != nil || rotated == secret {
		t.Fatalf("bootstrap authority did not rotate across migration restart: equal=%t err=%v", rotated == secret, err)
	}
	base = "http://127.0.0.1:" + strconv.Itoa(d.Port())
	resp, err = client.Get(base + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("browser cookie after daemon restart: %d", resp.StatusCode)
	}
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil || session.CSRF == "" {
		t.Fatalf("restored session: %v", err)
	}
}
