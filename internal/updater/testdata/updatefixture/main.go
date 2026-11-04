// updatefixture exercises the real updater without starting the desktop or Telegram.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/updater"
)

var version = "1.0.0"

func main() {
	dir := os.Getenv("UPDATER_FIXTURE_DIR")
	if version == "2.0.0" {
		if err := updater.MarkHealthy(filepath.Join(dir, "downloads"), version); err != nil {
			panic(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "restarted"), []byte(version), 0o600); err != nil {
			panic(err)
		}
		return
	}
	exe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	u := &updater.Updater{
		Repo: "bwoliveira/telegram-mcp-local", Version: version,
		API: os.Getenv("TELEGRAM_MCP_UPDATES_API"), Client: http.DefaultClient,
		PublicKey: os.Getenv("UPDATER_FIXTURE_PUBLIC_KEY"), Dir: filepath.Join(dir, "downloads"),
		Target: updater.Target{Kind: "appimage", Path: exe},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if found, err := u.Check(ctx); err != nil || found {
		panic(fmt.Sprintf("v1 check: found=%v err=%v", found, err))
	}
	if err := json.NewEncoder(os.Stdout).Encode(u.State()); err != nil {
		panic(err)
	}
	input := bufio.NewScanner(os.Stdin)
	if !input.Scan() || input.Text() != "check" {
		panic("explicit second check required")
	}
	if found, err := u.Check(ctx); err != nil || !found {
		panic(fmt.Sprintf("v2 check: found=%v err=%v", found, err))
	}
	if err := json.NewEncoder(os.Stdout).Encode(u.State()); err != nil {
		panic(err)
	}
	// Check must leave the installed v1 running until the person asks to apply.
	if !input.Scan() || input.Text() != "apply" {
		panic("explicit apply required")
	}
	if err := u.Apply(ctx); err != nil {
		panic(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(u.State()); err != nil {
		panic(err)
	}
	// The production helper waits for this PID, then executes the replaced file.
}
