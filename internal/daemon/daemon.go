// Package daemon assembles a Telegram engine, local state, transcription,
// the MCP server, the control panel and the loopback HTTP listener.
package daemon

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/gotdengine"
	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
	"github.com/bwoliveira/telegram-mcp-local/internal/localasr"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/panel"
	"github.com/bwoliveira/telegram-mcp-local/internal/webhook"
)

type Config struct {
	Port int
	// RequirePort makes Start fail when the port is taken, as the command
	// line does: a second daemon must not fight the first one for the
	// Telegram session. The app keeps the engine running and reports the port.
	RequirePort bool
	Token       string
	// Factory builds one controller per account. Nil selects production gotd;
	// synthetic mode requires TELEGRAM_MCP_ENGINE=fake.
	production bool
	Factory    engine.Factory
	StoreDir   string
	DataDir    string
	LogPath    string
	Version    string
	Logger     *slog.Logger
	// Desktop is what Claude Desktop runs to reach this daemon.
	Desktop panel.DesktopCommand
	// Panel adjusts the control panel before it is served, for the app to
	// plug in what only it can do.
	Panel func(*panel.Panel)
}

// Daemon is a running gateway.
type Daemon struct {
	cfg    Config
	logger *slog.Logger
	cancel context.CancelFunc

	// primary is the main number, the one the panel and the tray show.
	// accounts holds it first, then any further numbers.
	primary *Account
	panel   *panel.Panel
	routes  *http.ServeMux

	watchDone     chan struct{}
	webhookLimits *webhook.Limiter

	mu        sync.Mutex
	accounts  []*Account
	port      int
	srv       *http.Server
	listenErr *PortProblem

	watch watchers
}

// PortProblem says why the MCP is not answering: its port is taken.
type PortProblem = panel.PortProblem

// Start brings the gateway up. With RequirePort it fails when the port is
// taken; otherwise the engine starts anyway and PortProblem says why the MCP
// is off.
func Start(cfg Config) (*Daemon, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Version != "" {
		mcp.Version = cfg.Version
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("a bearer token is required to start the daemon")
	}
	logger = slog.New(httpserver.Redact(logger.Handler()))
	if cfg.Factory == nil {
		switch os.Getenv("TELEGRAM_MCP_ENGINE") {
		case "fake":
			cfg.Factory = fake.New
		case "", "gotd":
			cfg.Factory = gotdengine.Factory
			cfg.production = true
		default:
			return nil, errors.New("unknown engine selection TELEGRAM_MCP_ENGINE (expected gotd or fake)")
		}
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("cannot create %s: %w", cfg.DataDir, err)
	}
	panelBootstrapSecret, err := httpserver.EnsurePanelBootstrapSecret(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("cannot initialize panel bootstrap authority: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(panelBootstrapSecret), []byte(cfg.Token)) == 1 {
		return nil, errors.New("panel bootstrap authority must differ from the MCP bearer token")
	}

	// Claim the port before starting sync, so the command line fails here
	// rather than after it has taken the store.
	ln, err := listen(cfg.Port)
	var problem *PortProblem
	if err != nil {
		problem = describePortProblem(cfg.Port)
		if cfg.RequirePort {
			return nil, fmt.Errorf("cannot listen on %s: %w (is another Telegram MCP already running?)", addr(cfg.Port), err)
		}
		logger.Warn("the MCP port is taken; the engine runs, the MCP waits for another port", "port", cfg.Port, "error", err)
	}

	port := cfg.Port
	if ln != nil {
		port = ln.Addr().(*net.TCPAddr).Port
	}
	d := &Daemon{cfg: cfg, logger: logger, port: port, listenErr: problem, watchDone: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	d.cancel = cancel

	// Transcription models and programs are shared by every number.
	asr := localasr.New(cfg.DataDir)
	primary, err := d.startAccount(ctx, AccountSpec{StoreDir: cfg.StoreDir, DataDir: cfg.DataDir}, asr)
	if err != nil {
		cancel()
		closeQuietly(ln)
		return nil, err
	}
	d.primary, d.accounts = primary, []*Account{primary}

	d.panel = &panel.Panel{ReadStore: primary.store, AccountID: "primary", Server: primary.server, Engine: primary.eng, State: primary.st, Token: cfg.Token,
		Endpoint: d.MCPURL, Desktop: cfg.Desktop, LogPath: cfg.LogPath, MCPProblem: d.PortProblem, DataDir: cfg.DataDir}
	if cfg.Panel != nil {
		cfg.Panel(d.panel)
	}
	d.panel.Start(ctx)
	register := func(mux *http.ServeMux) {
		d.panel.Register(mux)
		d.panel.RegisterWebhookDocs(mux)
		httpserver.RegisterMediaAPIs(mux, primary.server, primary.hooks)
	}
	d.routes = httpserver.Routes(primary.server, httpserver.Options{Token: cfg.Token, PanelBootstrapSecret: panelBootstrapSecret, SessionDir: filepath.Join(cfg.DataDir, "accounts", "primary", "browser"), SessionAccount: "primary", Logger: logger, Register: register, Trusted: panel.IsInternal})

	if ln != nil {
		d.serve(ln)
		logger.Info("serving MCP", "url", d.MCPURL(), "store", cfg.StoreDir, "token", cfg.Token != "")
	}
	go d.watchLoop(ctx)
	return d, nil
}

func addr(port int) string    { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }
func baseURL(port int) string { return "http://" + addr(port) }

func listen(port int) (net.Listener, error) { return net.Listen("tcp", addr(port)) }

func closeQuietly(ln net.Listener) {
	if ln != nil {
		_ = ln.Close()
	}
}

func (d *Daemon) serve(ln net.Listener) {
	srv := httpserver.NewServer(httpserver.Guard(d.routes, d.logger))
	d.mu.Lock()
	d.srv = srv
	d.mu.Unlock()
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			d.logger.Error("the MCP listener stopped", "error", err)
		}
	}()
}

// Stop cancels engine work, waits for Run and the watcher, drains HTTP
// requests, and closes local databases.
func (d *Daemon) Stop() {
	d.cancel()
	accounts := d.Accounts()
	var wg sync.WaitGroup
	for _, a := range accounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.cancel()
			<-a.syncDone
		}()
	}
	wg.Wait()
	<-d.watchDone
	d.mu.Lock()
	srv := d.srv
	d.srv = nil
	d.mu.Unlock()
	if srv != nil {
		httpserver.Shutdown(srv)
	}
	if d.panel != nil {
		d.panel.Stop()
	}
	for _, a := range accounts {
		a.stop()
	}
}

// Accounts are the numbers the daemon runs, the main one first.
func (d *Daemon) Accounts() []*Account {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]*Account(nil), d.accounts...)
}

// Primary is the main number.
func (d *Daemon) Primary() *Account { return d.primary }

func (d *Daemon) accountCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.accounts)
}

// Port is the port the MCP is configured for.
func (d *Daemon) Port() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.port
}

// MCPURL is the MCP's address.
func (d *Daemon) MCPURL() string { return baseURL(d.Port()) + "/mcp" }

// PortProblem is why the MCP is not answering, or nil when it is.
func (d *Daemon) PortProblem() *PortProblem {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.listenErr
}

// HTTPHandler is what the loopback listener serves, guard included.
func (d *Daemon) HTTPHandler() http.Handler { return httpserver.Guard(d.routes, d.logger) }

// AppHandler is what the app's window loads, in memory: the same routes,
// trusted as the panel's own page.
func (d *Daemon) AppHandler() http.Handler { return panel.Internal(d.routes) }

// Engine is the primary account controller for the desktop lifecycle.
func (d *Daemon) Engine() engine.Controller { return d.primary.eng }

// Panel is the control panel.
func (d *Daemon) Panel() *panel.Panel { return d.panel }

// ErrPortInUse means another program listens on the port asked for.
type ErrPortInUse struct{ Port int }

func (e ErrPortInUse) Error() string {
	return fmt.Sprintf("port %d is already in use by another program", e.Port)
}

// SetPort moves the MCP to another port without touching the engine: the new
// listener opens before the old one closes, so the MCP is never left with
// none, and when the new port is taken the old one keeps answering.
func (d *Daemon) SetPort(port int) error {
	if port < 1024 || port > 65535 {
		return errors.New("port must be a number from 1024 to 65535")
	}
	d.mu.Lock()
	same := port == d.port && d.srv != nil
	d.mu.Unlock()
	if same {
		return nil
	}
	ln, err := listen(port)
	if err != nil {
		return ErrPortInUse{Port: port}
	}
	d.mu.Lock()
	old := d.srv
	d.port, d.srv, d.listenErr = port, nil, nil
	d.mu.Unlock()
	for _, a := range d.Accounts() {
		a.server.SetBaseURL(baseURL(port))
	}
	d.serve(ln)
	if old != nil {
		go httpserver.Shutdown(old)
	}
	d.logger.Info("MCP moved to another port", "url", d.MCPURL())
	d.watch.publish(d.status(context.Background()))
	return nil
}

// FreePortNear finds a port that can be listened on, starting after port.
func FreePortNear(port int) int {
	for p := port + 1; p < port+200 && p <= 65535; p++ {
		if ln, err := listen(p); err == nil {
			ln.Close()
			return p
		}
	}
	return 0
}

// describePortProblem says who holds the port: another Telegram MCP (the
// command-line service, typically) or some other program, and which port is
// free instead.
func describePortProblem(port int) *PortProblem {
	p := &PortProblem{Port: port, Suggest: FreePortNear(port)}
	client := &http.Client{Timeout: 2 * time.Second}
	if resp, err := client.Get(baseURL(port) + "/healthz"); err == nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 16))
		resp.Body.Close()
		p.OtherGateway = resp.StatusCode == http.StatusOK && strings.TrimSpace(string(body)) == "ok"
	}
	return p
}
