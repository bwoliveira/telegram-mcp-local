package main

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
)

func appLogger(out io.Writer) *slog.Logger {
	return slog.New(httpserver.Redact(slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelInfo})))
}

// openLog writes the app's log to a file in the system's log folder, keeping
// the previous run's beside it once the file passes 10 MB.
func openLog(dir string) (*slog.Logger, *os.File, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(dir, "telegram-mcp.log")
	if info, err := os.Stat(path); err == nil && info.Size() > 10<<20 {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	var out io.Writer = f
	if version == "dev" {
		out = io.MultiWriter(f, os.Stderr)
	}
	return appLogger(out), f, nil
}
