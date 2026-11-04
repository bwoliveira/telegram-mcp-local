// Command telegram-mcp-bridge is the stdio MCP server Claude Desktop starts.
// It forwards every request to the Telegram MCP app on this computer, finding
// its port in the app's config.json each time, so a port changed in the app
// needs nothing done in Claude Desktop.
//
// It is a program of its own, not the app with an argument, because a
// Windows program built to open windows has no reliable stdio.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/bwoliveira/telegram-mcp-local/internal/appconfig"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"

	"github.com/bwoliveira/telegram-mcp-local/internal/bridge"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runBridge(ctx, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "telegram-mcp-bridge:", err)
		os.Exit(1)
	}
}

func runBridge(ctx context.Context, in io.Reader, out io.Writer) error {
	dataDir := os.Getenv("TELEGRAM_MCP_DATA")
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return errors.New("cannot locate Telegram data")
		}
		dataDir = filepath.Join(home, ".telegram-mcp")
	}
	if _, _, err := appconfig.PortFromEnv(); err != nil {
		return err
	}
	token, err := service.ResolveToken(dataDir, os.Getenv("TELEGRAM_MCP_TOKEN"), false)
	if err != nil {
		return fmt.Errorf("cannot load persisted HTTP credential; initialize with `telegram-mcp serve`: %w", err)
	}
	return bridge.Run(ctx, bridge.Options{URL: func() string { return "http://127.0.0.1:" + strconv.Itoa(appconfig.ResolvePort(dataDir)) + "/mcp" }, Token: token, NotRunning: "the Telegram MCP daemon is not running; start `telegram-mcp serve` or `telegram-mcp service install`"}, in, out)
}
