// Command telegram-mcp runs the headless Telegram gateway: for servers, for a
// computer that stays on, and for installing with an AI agent. The desktop app
// runs the same gateway in its own process and accepts the same subcommands.
// See internal/cli for the commands.
package main

import "github.com/bwoliveira/telegram-mcp-local/internal/cli"

var version = "dev"

func main() { cli.Run(cli.Options{Version: version}) }
