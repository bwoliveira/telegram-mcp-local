// Command app is Telegram MCP as a desktop app: one window with the control
// panel, an icon in the tray, and the gateway running inside the app's own
// process for as long as it is open. Started with one of the command line's
// subcommands (serve, service, open, config, auth, version...), it runs that
// command without a window, exactly as the telegram-mcp command does; without
// one (or with only --hidden) it opens the app.
package main

import (
	"fmt"
	"os"
	"slices"

	"github.com/bwoliveira/telegram-mcp-local/internal/bridge"
	"github.com/bwoliveira/telegram-mcp-local/internal/cli"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println(version)
		return
	}
	// Inside an AppImage the bridge has no fixed path of its own, so Claude
	// Desktop starts the AppImage itself with this argument and the token the
	// panel's configuration carries. Linux gives a windowed program its stdio
	// as any other. Without that token it is the command line's bridge, which
	// reads the credential `telegram-mcp serve` persisted.
	if len(os.Args) > 1 && os.Args[1] == "bridge" && os.Getenv("TELEGRAM_MCP_TOKEN") != "" {
		os.Exit(bridge.Main())
	}
	// Every other terminal command runs headless, so a desktop package's
	// telegram-mcp also serves a computer without a display.
	if len(os.Args) > 1 && cli.IsCommand(os.Args[1]) {
		cli.Run(cli.Options{Version: version, Executable: autostartExecutable()})
		return
	}
	a, err := newApp(slices.Contains(os.Args[1:], "--hidden"))
	if err != nil {
		appLogger(os.Stderr).Error("the app could not start", "error", err)
		os.Exit(1)
	}
	if err := a.run(); err != nil {
		a.logger.Error("the app stopped", "error", err)
		os.Exit(1)
	}
}
