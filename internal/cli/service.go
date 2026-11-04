package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/daemon"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/panel"
	"github.com/bwoliveira/telegram-mcp-local/internal/platform"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
)

func credentialConfig(cfg config, create bool) (config, error) {
	token, err := service.ResolveToken(cfg.DataDir, cfg.Token, create)
	if err != nil {
		return cfg, fmt.Errorf("cannot load HTTP credential (initialize with `telegram-mcp serve`): %w", err)
	}
	cfg.Token = token
	return cfg, nil
}

type serviceManager interface {
	Install(service.Spec) (string, error)
	Uninstall() error
	Start() error
	Stop() error
	Status() (string, error)
}

func serviceCmd(cfg config, args []string) error {
	return runService(context.Background(), cfg, args, service.Manager{}, platform.Open, os.Stdout)
}

func runService(ctx context.Context, cfg config, args []string, manager serviceManager, open func(string) error, out io.Writer) error {
	usageErr := errors.New("usage: telegram-mcp service install [--no-open]|uninstall|stop|start|status")
	if len(args) == 0 || len(args) > 2 || (len(args) == 2 && (args[0] != "install" || args[1] != "--no-open")) {
		return usageErr
	}
	switch args[0] {
	case "install":
		var err error
		cfg, err = credentialConfig(cfg, true)
		if err != nil {
			return err
		}
		if os.Getenv("TELEGRAM_MCP_ENGINE") != "fake" {
			if _, _, _, err := daemon.PrepareAccountCredentials(cfg.DataDir, engine.AccountID("primary")); err != nil {
				return err
			}
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		path, err := manager.Install(service.Spec{Binary: executable(), Args: []string{"serve"}, Env: serviceEnv(cfg), LogDir: filepath.Join(home, "Library", "Logs")})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "installed %s\nThe daemon runs at login and restarts if it stops.\nMCP: %s/mcp\n", path, cfg.baseURL())
		if len(args) == 2 {
			return nil
		}
		ready, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := waitHealthyContext(ready, cfg.baseURL()+"/healthz"); err != nil {
			return fmt.Errorf("service installed but not answering; see %s", logPath())
		}
		if err := openPanel(ctx, cfg, open); err != nil {
			return fmt.Errorf("service installed; %w", err)
		}
		return nil
	case "uninstall":
		if err := manager.Uninstall(); err != nil {
			return err
		}
		fmt.Fprintln(out, "service removed (credentials preserved)")
		return nil
	case "stop":
		if err := manager.Stop(); err != nil {
			return err
		}
		fmt.Fprintln(out, "service stopped; Telegram is not being received until `telegram-mcp service start`")
		return nil
	case "start":
		if err := manager.Start(); err != nil {
			return err
		}
		fmt.Fprintln(out, "service started; run `telegram-mcp open` for the panel")
		return nil
	case "status":
		status, err := manager.Status()
		if err != nil {
			return err
		}
		fmt.Fprintln(out, status)
		return nil
	default:
		return usageErr
	}
}

func waitHealthyContext(ctx context.Context, url string) error {
	client := daemonClient()
	defer client.CloseIdleConnections()
	client.Timeout = 2 * time.Second
	for {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return errors.New("invalid health endpoint")
		}
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func printConfig(cfg config) { writeConfig(os.Stdout, cfg) }
func writeConfig(out io.Writer, cfg config) {
	desktop := desktopCommand(cfg)
	server := map[string]any{"type": "stdio", "command": desktop.Command, "args": desktop.Args, "env": desktop.Env}
	compact, _ := json.Marshal(server)
	quoted := "'" + strings.ReplaceAll(string(compact), "'", "'\\''") + "'"
	fmt.Fprintf(out, "Claude Code (uses the persisted credential, not a bearer in client configuration):\n  claude mcp add-json --scope user telegram %s\n\n", quoted)
	body, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{"telegram": server}}, "", "  ")
	fmt.Fprintln(out, "Claude Desktop (Chat and Cowork) — "+panel.DesktopConfigPath()+":")
	fmt.Fprintln(out, string(body))
}
