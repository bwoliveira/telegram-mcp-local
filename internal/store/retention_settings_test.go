package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

func TestJanitorUsesLiveInstanceRetentionSettings(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	settings, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer settings.Close()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "store.db"), "lifecycle", WithClock(func() time.Time { return now }), WithRetentionSettings(settings))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	lifecycleMessage(t, s, 1, nil)
	media := filepath.Join(s.dir, "media", "old.ogg")
	export := filepath.Join(s.dir, "exports", "old.ndjson")
	seed := func() {
		lifecycleExec(t, s, `INSERT INTO media_files SELECT row_id,'full',?,4,'hash',? FROM messages`, media, now.AddDate(0, 0, -40).Unix())
		lifecycleExec(t, s, `INSERT INTO exports(account,path,filter_json,count,bytes,created_at) VALUES('lifecycle',?,'{}',1,4,?)`, export, now.AddDate(0, 0, -10).Unix())
	}
	set := func(key, value string) {
		if err := settings.SetSetting(ctx, key, value); err != nil {
			t.Fatal(err)
		}
	}
	seed()
	set("media_retention_days", "0")
	set("export_retention_days", "0")
	for _, values := range [][2]string{{"0", "0"}, {"60", "20"}} {
		set("media_retention_days", values[0])
		set("export_retention_days", values[1])
		if err = s.JanitorOnce(ctx, nil); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"media_files", "exports"} {
			if n := lifecycleCount(t, s, `SELECT count(*) FROM `+table); n != 1 {
				t.Fatalf("%s lost with %v", table, values)
			}
		}
	}
	set("media_retention_days", "1")
	set("export_retention_days", "1")
	if err = s.JanitorOnce(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"media_files", "exports"} {
		if n := lifecycleCount(t, s, `SELECT count(*) FROM `+table); n != 0 {
			t.Fatalf("%s did not use changed retention", table)
		}
	}
	// Missing settings retain 30/7 defaults, without policy leaking from s.
	other := lifecycleOpen(t, func() time.Time { return now })
	lifecycleMessage(t, other, 1, nil)
	lifecycleExec(t, other, `INSERT INTO media_files SELECT row_id,'full',?,4,'hash',? FROM messages`, filepath.Join(other.dir, "media", "old.ogg"), now.AddDate(0, 0, -40).Unix())
	if err = other.JanitorOnce(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if n := lifecycleCount(t, other, `SELECT count(*) FROM media_files`); n != 0 {
		t.Fatal("default store inherited disabled retention")
	}
	if err = settings.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.JanitorOnce(ctx, nil); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("settings read failure ignored: %v", err)
	}
}
