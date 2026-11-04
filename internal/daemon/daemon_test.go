package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/panel"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Generated for this test process, never a real installation credential.
var testToken = base64.RawURLEncoding.EncodeToString(randomToken())

func randomToken() []byte {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

func testConfig(t *testing.T, port int) Config {
	t.Helper()
	root := t.TempDir()
	return Config{Port: port, RequirePort: true, Token: testToken, Factory: fake.New,
		StoreDir: filepath.Join(root, "engine"), DataDir: filepath.Join(root, "data")}
}

func startDaemon(t *testing.T, port int, requirePort bool) (*Daemon, string) {
	t.Helper()
	cfg := testConfig(t, port)
	cfg.RequirePort = requirePort
	d, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Stop)
	return d, cfg.StoreDir
}

func get(port int, path string) (int, string, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(port)+path, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), err
}

func waitSync(t *testing.T, d *Daemon, want engine.ConnState) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if d.Engine().Status().State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("engine never reached %q: %+v", want, d.Engine().Status())
}

func TestStartRequiresTokenBeforeConstructingEngine(t *testing.T) {
	cfg := testConfig(t, 0)
	cfg.Token = " 	"
	called := false
	cfg.Factory = func(c engine.Config) (engine.Controller, error) { called = true; return fake.New(c) }
	if d, err := Start(cfg); err == nil {
		d.Stop()
		t.Fatal("empty bearer token was accepted")
	} else if !strings.Contains(err.Error(), "token") {
		t.Fatalf("unclear token error: %v", err)
	}
	if called {
		t.Fatal("engine constructed before token validation")
	}
}

func TestDefaultEngineRequiresExplicitFakeSelection(t *testing.T) {
	cfg := testConfig(t, 0)
	cfg.Factory = nil
	t.Setenv("TELEGRAM_MCP_SECRETS", "file")
	t.Setenv("TELEGRAM_API_ID", "")
	t.Setenv("TELEGRAM_API_HASH", "")
	for _, selection := range []string{"", "gotd", "typo"} {
		t.Setenv("TELEGRAM_MCP_ENGINE", selection)
		d, err := Start(cfg)
		if err == nil {
			d.Stop()
			t.Fatalf("engine selection %q silently used fake", selection)
		}
		if !strings.Contains(err.Error(), "engine") {
			t.Fatalf("unclear engine error: %v", err)
		}
	}
	t.Setenv("TELEGRAM_MCP_ENGINE", "fake")
	d, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	if d.Engine().Mode() != engine.ModeReadOnly {
		t.Fatal("fake must default to read-only")
	}
}

func TestFactoryReceivesIsolatedAccountConfiguration(t *testing.T) {
	cfg := testConfig(t, 0)
	var configs []engine.Config
	cfg.Factory = func(c engine.Config) (engine.Controller, error) {
		configs = append(configs, c)
		return fake.New(c)
	}
	d, err := Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	second, err := d.startAccount(context.Background(), AccountSpec{Slug: "work", StoreDir: t.TempDir(), DataDir: t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer second.stop()
	if len(configs) != 2 || configs[0].Account != "primary" || configs[1].Account != "work" {
		t.Fatalf("account configs: %+v", configs)
	}
	for _, c := range configs {
		if c.Mode != engine.ModeReadOnly || c.Logger == nil {
			t.Fatalf("unsafe engine defaults: %+v", c)
		}
	}
	if d.Primary().Engine() != d.Engine() || second.Engine() == d.Engine() {
		t.Fatal("account engines were not isolated")
	}
	info := d.Primary().Info(context.Background())
	if info.Name != "Test Account" {
		t.Fatalf("account identity did not come from engine: %+v", info)
	}
}

func TestSetPortMovesTheMCPWithoutDroppingIt(t *testing.T) {
	d, _ := startDaemon(t, 0, true)
	first := d.Port()
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	busy := taken.Addr().(*net.TCPAddr).Port
	var inUse ErrPortInUse
	if err := d.SetPort(busy); !errors.As(err, &inUse) || inUse.Port != busy {
		t.Fatalf("taken port must be refused: %v", err)
	}
	if code, _, err := get(first, "/healthz"); err != nil || code != 200 {
		t.Fatalf("current port stopped: %d %v", code, err)
	}
	if err := d.SetPort(80); err == nil {
		t.Fatal("ports below 1024 must be refused")
	}
	second := freePort(t)
	if err := d.SetPort(second); err != nil {
		t.Fatal(err)
	}
	if code, _, err := get(second, "/healthz"); err != nil || code != 200 {
		t.Fatalf("new port does not answer: %d %v", code, err)
	}
	if d.MCPURL() != "http://127.0.0.1:"+strconv.Itoa(second)+"/mcp" {
		t.Fatalf("MCPURL = %s", d.MCPURL())
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, _, err := get(first, "/healthz"); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("old port kept answering after move")
}

func TestATakenPortKeepsEngineRunningAndSaysWho(t *testing.T) {
	other, _ := startDaemon(t, 0, true)
	port := other.Port()
	cfg := testConfig(t, port)
	if d, err := Start(cfg); err == nil {
		d.Stop()
		t.Fatal("CLI must refuse taken port")
	}
	d, _ := startDaemon(t, port, false)
	p := d.PortProblem()
	if p == nil || p.Port != port || !p.OtherGateway || p.Suggest == 0 || p.Suggest == port {
		t.Fatalf("port problem = %+v", p)
	}
	waitSync(t, d, engine.StateConnected)
	if st := d.Status(); !st.MCPDown || st.Tone != "warn" {
		t.Fatalf("status with MCP down: %+v", st)
	}
	if err := d.SetPort(p.Suggest); err != nil {
		t.Fatal(err)
	}
	if d.PortProblem() != nil {
		t.Fatal("free port must clear problem")
	}
	if code, _, err := get(p.Suggest, "/healthz"); err != nil || code != 200 {
		t.Fatalf("suggested port does not answer: %d %v", code, err)
	}
}

type statusController struct {
	engine.Controller
	connection engine.Status
}

func (s statusController) Status() engine.Status { return s.connection }

func TestRecoverableEngineStatesAreWarnings(t *testing.T) {
	for _, state := range []engine.ConnState{engine.StateResyncing, engine.StateReconnecting} {
		t.Run(string(state), func(t *testing.T) {
			cfg := testConfig(t, 0)
			cfg.Factory = func(c engine.Config) (engine.Controller, error) {
				controller, err := fake.New(c)
				return statusController{Controller: controller, connection: engine.Status{State: state}}, err
			}
			d, err := Start(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer d.Stop()
			if status := d.Status(); status.Tone != "warn" || status.Sync != string(state) {
				t.Fatalf("recoverable engine state reported as failure: %+v", status)
			}
		})
	}
}

func TestWatchFollowsEngine(t *testing.T) {
	d, _ := startDaemon(t, 0, true)
	updates, stop := d.Watch()
	defer stop()
	if st := <-updates; st.Sync != "connected" {
		t.Fatalf("initial status: %+v", st)
	}
	if err := d.Engine().Disconnect(context.Background(), "user"); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case st := <-updates:
			if st.Sync == "paused" {
				return
			}
		case <-deadline:
			t.Fatal("watcher never saw engine pause")
		}
	}
}

type fakeHost struct{ port func(int) error }

func (fakeHost) Settings() panel.HostSettings {
	return panel.HostSettings{Autostart: true, CloseToTray: true, CanHide: true, DataDir: "/data", Version: "1.0.0"}
}
func (fakeHost) SetAutostart(bool) error   { return nil }
func (fakeHost) SetCloseToTray(bool) error { return nil }
func (fakeHost) SetLanguage(string) error  { return nil }
func (h fakeHost) SetPort(p int) error     { return h.port(p) }
func (fakeHost) OpenURL(string) error      { return nil }
func (fakeHost) OpenDataFolder() error     { return nil }
func (fakeHost) Update() panel.UpdateState {
	return panel.UpdateState{State: "available", Current: "1.0.0", Latest: "1.1.0"}
}
func (fakeHost) CheckUpdate()           {}
func (fakeHost) InstallUpdate() error   { return nil }
func (fakeHost) Quit()                  {}
func (fakeHost) ShowWindow(string)      {}
func (fakeHost) EraseEverything() error { return nil }

func TestEveryPageRendersInTheApp(t *testing.T) {
	var d *Daemon
	cfg := testConfig(t, 0)
	cfg.Panel = func(p *panel.Panel) { p.Host = fakeHost{port: func(port int) error { return d.SetPort(port) }} }
	var err error
	d, err = Start(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Stop()
	h := d.AppHandler()
	for _, path := range []string{"/", "/instalacao", "/conta", "/status", "/funcoes", "/receitas", "/configuracoes", "/configuracoes?porta=1", "/estado", "/transcricao", "/documentacao", "/conectar", "/conectar/claude-desktop", "/conectar/claude-code", "/conectar/codex", "/conectar/cursor", "/conectar/codex?conectado=1", "/conectar/outra", "/conectar/nada", "/ajuda"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "wails://localhost"+path, nil))
		body := rec.Body.String()
		if rec.Code != 200 || strings.Contains(body, "can't evaluate") {
			t.Errorf("%s answered %d: %.300s", path, rec.Code, body)
		}
		if strings.Contains(body, "<html") && !strings.Contains(body, "data-app") {
			t.Errorf("%s not marked as app's", path)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "wails://localhost/configuracoes", nil))
	if !strings.Contains(rec.Body.String(), "Configurações") || !strings.Contains(rec.Body.String(), "1.1.0") {
		t.Error("settings page does not show update")
	}
}

func TestTheAccountsNameReachesTheStatus(t *testing.T) {
	d, _ := startDaemon(t, 0, true)
	if n := len(d.Accounts()); n != 1 || d.Accounts()[0] != d.Primary() || d.Primary().Slug != "" {
		t.Fatalf("one primary account expected: %d", n)
	}
	h := d.AppHandler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "wails://localhost/conta/nome", strings.NewReader("label=++Trabalho++"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(rec, req)
	if rec.Code >= 400 {
		t.Fatalf("naming account answered %d: %s", rec.Code, rec.Body)
	}
	status, isErr := callTool(t, d.Port(), "telegram_status", nil)
	acct, _ := status["account"].(map[string]any)
	if isErr || acct["label"] != "Trabalho" {
		t.Fatalf("telegram_status account = %v (error %v)", status["account"], isErr)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "wails://localhost/conta", nil))
	if !strings.Contains(rec.Body.String(), `value="Trabalho"`) {
		t.Error("account page does not show label")
	}
}
