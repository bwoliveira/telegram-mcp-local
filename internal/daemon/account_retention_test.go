package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

func TestAccountJanitorUsesPersistedRetentionBeforeEngineStarts(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	settings, err := state.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if err = settings.SetSetting(ctx, "export_retention_days", "0"); err != nil {
		t.Fatal(err)
	}
	if err = settings.Close(); err != nil {
		t.Fatal(err)
	}
	old := time.Now().AddDate(0, 0, -8)
	db, err := store.Open(ctx, filepath.Join(data, "accounts", "primary", "store.db"), "primary", store.WithClock(func() time.Time { return old }))
	if err != nil {
		t.Fatal(err)
	}
	export := filepath.Join(data, "exports", "saved.ndjson")
	if err = os.MkdirAll(filepath.Dir(export), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(export, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = db.RegisterStatsExport(ctx, "primary", export, "{}", 1, 4); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Factory: func(cfg engine.Config) (engine.Controller, error) {
		if _, err := os.Stat(export); err != nil {
			t.Errorf("startup janitor ignored persisted zero: %v", err)
		}
		return fake.New(cfg)
	}}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	a, err := d.startAccount(ctx, AccountSpec{StoreDir: t.TempDir(), DataDir: data}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer a.stop()
	if _, err = a.store.ReadStatsExport(ctx, "primary", export); err != nil {
		t.Fatalf("startup removed metadata: %v", err)
	}
	if err = a.server.SetExportRetention(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if err = a.store.JanitorOnce(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(export); !os.IsNotExist(err) {
		t.Fatalf("janitor ignored updated setting: %v", err)
	}
}
