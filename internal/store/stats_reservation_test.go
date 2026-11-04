package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestStatsReservedExportsSurviveRestartAndExpire(t *testing.T) {
	for _, published := range []bool{false, true} {
		t.Run(map[bool]string{false: "during-stream", true: "after-rename"}[published], func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(1800000000, 0)
			dir := t.TempDir()
			db := filepath.Join(dir, "accounts", "primary", "store.db")
			s, err := Open(ctx, db, "primary", WithClock(func() time.Time { return now }))
			if err != nil {
				t.Fatal(err)
			}
			root, err := s.StatsExportDir(ctx, "primary")
			if err != nil {
				t.Fatal(err)
			}
			if err = os.MkdirAll(root, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "interrupted.ndjson")
			if err = s.ReserveStatsExport(ctx, "primary", path, `{}`); err != nil {
				t.Fatal(err)
			}
			for _, reserved := range []string{path, path + ".partial"} {
				if r, err := s.ReadStatsExport(ctx, "primary", reserved); err != nil || r.Bytes != 0 {
					t.Fatal(r, err)
				}
			}
			if err = os.WriteFile(path+".partial", []byte("private data\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if published {
				if err = os.Rename(path+".partial", path); err != nil {
					t.Fatal(err)
				}
			}
			// No completion or abort: emulate a writer lost at either crash boundary.
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			now = now.Add(8 * 24 * time.Hour)
			s, err = Open(ctx, db, "primary", WithClock(func() time.Time { return now }))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err = s.Cleanup(ctx); err != nil {
				t.Fatal(err)
			}
			for _, reserved := range []string{path, path + ".partial"} {
				if _, err = os.Stat(reserved); !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if _, err = s.ReadStatsExport(ctx, "primary", reserved); !errors.Is(err, sql.ErrNoRows) {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestStatsReservationCommitAndAbort(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	dir, err := s.StatsExportDir(ctx, "primary")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "complete.ndjson")
	if err = s.ReserveStatsExport(ctx, "primary", path, `{"direction":"in"}`); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.CommitStatsExport(ctx, "primary", path, 1, 3); err != nil {
		t.Fatal(err)
	}
	r, err := s.ReadStatsExport(ctx, "primary", path)
	if err != nil || r.Bytes != 3 || r.Count != 1 || r.FilterJSON != `{"direction":"in"}` {
		t.Fatal(r, err)
	}
	if _, err = s.ReadStatsExport(ctx, "primary", path+".partial"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if err = s.CommitStatsExport(ctx, "primary", path, 99, 3); err == nil {
		t.Fatal("committed twice")
	}
	if err = s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatal("cleanup removed live export", err)
	}
	aborted := filepath.Join(dir, "aborted.ndjson")
	if err = s.ReserveStatsExport(ctx, "primary", aborted, `{}`); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(aborted+".partial", []byte("sensitive\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = s.AbortStatsExport(ctx, "primary", aborted); err != nil {
		t.Fatal(err)
	}
	if err = s.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(aborted + ".partial"); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestStatsFractionalTimestampBoundaries(t *testing.T) {
	s := statsFixture(t)
	for _, tc := range []struct {
		name string
		f    StatsFilter
		want int64
	}{
		{"fractional-since", StatsFilter{Since: time.Unix(1000, 500000000)}, 3},
		{"fractional-until", StatsFilter{Until: time.Unix(1000, 500000000)}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := s.StatsMessages(context.Background(), "primary", tc.f, "chat", 50)
			if err != nil || r.Total != tc.want {
				t.Fatal(r, err)
			}
			var count int64
			if err = s.StreamExportMessages(context.Background(), "primary", tc.f, func(m engine.Message) error { count++; return nil }); err != nil || count != tc.want {
				t.Fatal(count, err)
			}
		})
	}
}
