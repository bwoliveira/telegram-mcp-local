package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

func TestStatsRegistrationValidatesAndReadsBack(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1800000000, 0)
	data := t.TempDir()
	s, err := Open(ctx, filepath.Join(data, "accounts", "primary", "store.db"), "primary", WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir, err := s.StatsExportDir(ctx, "primary")
	if err != nil || dir != filepath.Join(data, "exports") {
		t.Fatal(dir, err)
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "safe.ndjson")
	if err = os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterStatsExport(ctx, "primary", path, `{"direction":"in"}`, 0, 3); err != nil {
		t.Fatal(err)
	}
	record, err := s.ReadStatsExport(ctx, "primary", path)
	if err != nil || record.Path != path || record.FilterJSON != `{"direction":"in"}` || record.Count != 0 || record.Bytes != 3 || !record.CreatedAt.Equal(now) {
		t.Fatalf("record %+v %v", record, err)
	}
	if err = s.RegisterStatsExport(ctx, "primary", path, `{}`, 0, 3); err == nil {
		t.Fatal("duplicate registration replaced record")
	}
	record, err = s.ReadStatsExport(ctx, "primary", path)
	if err != nil || record.FilterJSON != `{"direction":"in"}` {
		t.Fatal(record, err)
	}
	for _, account := range []engine.AccountID{"", "other"} {
		if err = s.RegisterStatsExport(ctx, account, path, `{}`, 0, 3); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatal(err)
		}
		if _, err = s.ReadStatsExport(ctx, account, path); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err = s.RegisterStatsExport(canceled, "primary", path, `{}`, 0, 3); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	unsafe := filepath.Join(dir, "unsafe.ndjson")
	if err = os.WriteFile(unsafe, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	restore := testutil.LoosenPermissions(t, unsafe)
	if err = s.RegisterStatsExport(ctx, "primary", unsafe, `{}`, 0, 3); !errors.Is(err, engine.ErrInvalid) {
		t.Fatal(err)
	}
	restore()
	for _, tc := range []struct {
		filter string
		count  int
		bytes  int64
	}{{"invalid", 0, 3}, {"{}", -1, 3}, {"{}", 0, 7}} {
		if err = s.RegisterStatsExport(ctx, "primary", unsafe, tc.filter, tc.count, tc.bytes); !errors.Is(err, engine.ErrInvalid) {
			t.Fatal(err)
		}
	}
	symlink := filepath.Join(dir, "symlink.ndjson")
	if err = os.Symlink(unsafe, symlink); err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterStatsExport(ctx, "primary", symlink, `{}`, 0, 3); !errors.Is(err, engine.ErrInvalid) {
		t.Fatal(err)
	}
	outside := filepath.Join(data, "outside.ndjson")
	if err = os.WriteFile(outside, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterStatsExport(ctx, "primary", outside, `{}`, 0, 3); !errors.Is(err, engine.ErrInvalid) {
		t.Fatal(err)
	}
	now = now.Add(8 * 24 * time.Hour)
	if err = s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err = os.Stat(outside); err != nil {
		t.Fatal("retention touched unregistered outside file", err)
	}
}
