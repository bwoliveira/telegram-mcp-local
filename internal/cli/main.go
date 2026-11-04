// Package cli is the headless Telegram gateway's command line: for servers, for
// a computer that stays on, and for installing with an AI agent. The
// telegram-mcp command and the desktop app's subcommands share it, so an
// installed desktop package keeps every terminal command without a window.
//
//	telegram-mcp serve               run the daemon (sync + MCP endpoint)
//	telegram-mcp bridge              stdio MCP for Claude Desktop, forwarding to the daemon
//	telegram-mcp service install     start the daemon at login and keep it running
//	telegram-mcp service uninstall
//	telegram-mcp service stop|start  stop and start it again, without removing it
//	telegram-mcp open                open the control panel
//	telegram-mcp config              print the client configuration
//	telegram-mcp version
package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"

	"github.com/bwoliveira/telegram-mcp-local/internal/appconfig"
	"github.com/bwoliveira/telegram-mcp-local/internal/bridge"
	"github.com/bwoliveira/telegram-mcp-local/internal/daemon"
	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/localasr"
	"github.com/bwoliveira/telegram-mcp-local/internal/panel"
	"github.com/bwoliveira/telegram-mcp-local/internal/platform"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
)

var version = "dev"

// executable is the program that login services and Claude Desktop start.
var executable = platform.Executable

// Options identify the program running the command line.
type Options struct {
	// Version is printed by `version` and reported by the daemon.
	Version string
	// Executable replaces the running binary's path in installed services and
	// client configuration, e.g. the AppImage file instead of its mount point.
	Executable string
}

// commands are the subcommands Run accepts; anything else is an error.
var commands = map[string]bool{
	"serve": true, "bridge": true, "service": true, "config": true,
	"transcription": true, "open": true, "auth": true,
	"version": true, "--version": true, "-v": true,
	"help": true, "--help": true, "-h": true,
}

// IsCommand reports whether name is one of the command line's subcommands.
func IsCommand(name string) bool { return commands[name] }

// Run executes the subcommand in os.Args[1] (serve without one) and exits
// the process on failure.
func Run(opts Options) {
	if opts.Version != "" {
		version = opts.Version
	}
	if path := opts.Executable; path != "" {
		executable = func() string { return path }
	}
	run()
}

type config struct {
	Port                 int
	Token                string
	PanelBootstrapSecret string
	StoreDir             string
	DataDir              string
}

func (c config) baseURL() string { return "http://127.0.0.1:" + strconv.Itoa(c.Port) }

func load() (config, error) {
	c := config{Port: appconfig.DefaultPort, Token: os.Getenv("TELEGRAM_MCP_TOKEN")}
	if p, ok, err := appconfig.PortFromEnv(); err != nil {
		return c, err
	} else if ok {
		c.Port = p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return c, err
	}
	c.DataDir = os.Getenv("TELEGRAM_MCP_DATA")
	if c.DataDir == "" {
		c.DataDir = filepath.Join(home, ".telegram-mcp")
	}
	// Keep the port selected in the panel; an explicit environment value
	// remains authoritative (and is validated above).
	c.Port = appconfig.ResolvePort(c.DataDir)
	c.StoreDir = os.Getenv("TELEGRAM_MCP_STORE_DIR")
	if c.StoreDir == "" {
		c.StoreDir = filepath.Join(c.DataDir, "store")
	}
	c.DataDir, err = filepath.Abs(c.DataDir)
	if err != nil {
		return c, err
	}
	c.StoreDir, err = filepath.Abs(c.StoreDir)
	return c, err
}

// serviceEnv keeps the login service isolated from unrelated application state.
// The explicit fake selection must survive a minimal service environment.
func serviceEnv(cfg config) map[string]string {
	env := map[string]string{
		"TELEGRAM_MCP_STORE_DIR": cfg.StoreDir,
		"TELEGRAM_MCP_DATA":      cfg.DataDir,
		"TELEGRAM_MCP_PORT":      strconv.Itoa(cfg.Port),
	}
	for _, key := range []string{"TELEGRAM_MCP_ENGINE", "TELEGRAM_MCP_LANG", "TELEGRAM_MCP_SECRETS"} {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	return env
}

// logPath is where the login service writes the daemon's log.
func logPath() string {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "Library", "Logs", "telegram-mcp.log")
	case "linux":
		return "journalctl --user -u telegram-mcp.service"
	}
	return ""
}

func run() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	// Printing the version or usage needs no configuration, data or display.
	switch cmd {
	case "version", "--version", "-v":
		fmt.Println(version)
		return
	case "help", "--help", "-h":
		fmt.Print(usage)
		return
	}
	cfg, err := load()
	if err != nil {
		fatal(err)
	}
	// Shared installation helpers use i18n too. Terminal commands always
	// speak English; only the long-running daemon hosts the localized panel.
	if cmd == "serve" {
		i18n.Set(language(cfg.DataDir))
	} else {
		i18n.Set("en")
	}
	switch cmd {
	case "serve":
		err = serve(cfg)
	case "bridge":
		cfg, err = credentialConfig(cfg, false)
		if err != nil {
			fatal(err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		err = bridge.Run(ctx, bridge.Options{
			URL:        func() string { return cfg.baseURL() + "/mcp" },
			Token:      cfg.Token,
			NotRunning: "the Telegram MCP daemon is not running; start it with `telegram-mcp service install` or `telegram-mcp serve`",
		}, os.Stdin, os.Stdout)
	case "service":
		err = serviceCmd(cfg, os.Args[2:])
	case "config":
		err = configCmd(context.Background(), cfg, os.Args[2:])
	case "transcription":
		err = transcriptionCmd(cfg, os.Args[2:])
	case "open":
		cfg.PanelBootstrapSecret, err = httpserver.ReadPanelBootstrapSecret(cfg.DataDir)
		if err != nil {
			err = fmt.Errorf("cannot load panel bootstrap authority (start `telegram-mcp serve` first): %w", err)
		} else {
			err = openCommand(context.Background(), cfg, platform.Open)
		}
	case "auth":
		cfg, err = credentialConfig(cfg, false)
		if err == nil {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			err = runAuth(ctx, cfg, os.Args[2:], terminalReader(os.Stdin, os.Stderr), os.Stderr)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fatal(err)
	}
}

const usage = `telegram-mcp — Telegram for MCP clients, on localhost

  serve               run the engine and serve MCP at http://127.0.0.1:47831/mcp
  bridge              stdio MCP server for Claude Desktop that forwards to the daemon
  service install     run the daemon at login and restart it if it stops (launchd / systemd --user)
  service uninstall   remove that service
  service stop|start  stop the service (as if the computer were off) and start it again
  service status      show the background service state
  auth [--phone]      sign in through the daemon; phone/code/2FA only at protected prompts
  auth --qr           print a private local QR HTML path; keep running until authenticated
  config              print the configuration for Claude Code and Claude Desktop
  transcription install   set up voice-note transcription on this computer (whisper.cpp)
  open                open the control panel in the browser
  version             print the version

Environment: TELEGRAM_MCP_PORT, TELEGRAM_MCP_TOKEN (first-install seed only), TELEGRAM_MCP_LANG (panel: pt-BR, es, en; CLI output is English), TELEGRAM_MCP_STORE_DIR, TELEGRAM_MCP_DATA, TELEGRAM_MCP_ENGINE
Development: TELEGRAM_MCP_ENGINE=fake selects synthetic data; gotd is the default Telegram engine.
QR login renews automatically for up to 10 minutes, or until explicitly cancelled.
Data defaults to ~/.telegram-mcp; no unrelated application session or state is imported.
Tokens are generated on first serve/install and persisted in internal/secrets.
An optional token seed must encode 32 random bytes as unpadded base64url.
Terminal auth accepts credentials only at prompts; auth --qr uses a private local QR page.
`

// language is the one the panel and the messages show: TELEGRAM_MCP_LANG,
// else the one kept in the data folder (appconfig.Chosen).
func language(dataDir string) string {
	if lang := os.Getenv("TELEGRAM_MCP_LANG"); lang != "" {
		return lang
	}
	return appconfig.Chosen(dataDir)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "telegram-mcp:", err)
	os.Exit(1)
}

func serve(cfg config) error {
	var err error
	cfg, err = credentialConfig(cfg, true)
	if err != nil {
		return err
	}
	if err := service.PrepareData(cfg.DataDir); err != nil {
		return err
	}
	logger := slog.New(httpserver.Redact(slog.NewTextHandler(os.Stderr, nil)))
	slog.SetDefault(logger)
	if err := appconfig.MarkNew(cfg.DataDir); err != nil {
		logger.Warn("could not write config.json", "error", err)
	}
	d, err := daemon.Start(daemon.Config{
		Port: cfg.Port, RequirePort: true, Token: cfg.Token,
		StoreDir: cfg.StoreDir, DataDir: cfg.DataDir,
		LogPath: logPath(), Version: version, Logger: logger,
		Desktop: desktopCommand(cfg),
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n  Panel: telegram-mcp open\n  MCP:   %s/mcp\n\n", cfg.baseURL())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	d.Stop()
	return nil
}

// desktopCommand is how Claude Desktop reaches this daemon: this program's
// bridge subcommand, with the port when it is not the default.
func desktopCommand(cfg config) panel.DesktopCommand {
	c := panel.DesktopCommand{Command: executable(), Args: []string{"bridge"}, Env: map[string]string{"TELEGRAM_MCP_DATA": cfg.DataDir}}
	if c.Command == "" {
		c.Command = "telegram-mcp"
	}
	if cfg.Port != appconfig.DefaultPort {
		c.Env["TELEGRAM_MCP_PORT"] = strconv.Itoa(cfg.Port)
	}
	if mode := os.Getenv("TELEGRAM_MCP_SECRETS"); mode != "" {
		c.Env["TELEGRAM_MCP_SECRETS"] = mode
	}
	return c
}

func transcriptionCmd(cfg config, args []string) error {
	if len(args) == 0 || args[0] != "install" {
		return errors.New("usage: telegram-mcp transcription install")
	}
	engine := localasr.New(cfg.DataDir)
	if st := engine.Status(); !st.Supported {
		return errors.New("local transcription is not available on this system")
	} else if st.Ready {
		fmt.Println("local transcription is already installed")
		return nil
	}
	fmt.Println("setting up local transcription (whisper.cpp, about 600 MB)…")
	if err := engine.InstallNow(context.Background(), os.Stdout); err != nil {
		return err
	}
	fmt.Println("local transcription is ready: voice notes are now transcribed on this computer")
	return nil
}
