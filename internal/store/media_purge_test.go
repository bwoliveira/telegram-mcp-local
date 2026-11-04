package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

type mediaPurger interface {
	PurgeMediaPaths(context.Context, engine.AccountID, []string) error
}

func purgePaths(t *testing.T, s *Store, account engine.AccountID, paths ...string) error {
	t.Helper()
	p, ok := any(s).(mediaPurger)
	if !ok {
		t.Fatal("Store must implement the durable media purge contract")
	}
	return p.PurgeMediaPaths(context.Background(), account, paths)
}

func purgeFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	s := lifecycleOpen(t, time.Now)
	lifecycleMessage(t, s, 1, nil)
	media := filepath.Join(s.dir, "media", "cached.ogg")
	export := filepath.Join(s.dir, "exports", "report.ndjson")
	for _, path := range []string{media, export} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	lifecycleExec(t, s, `INSERT INTO media_files SELECT row_id,'full',?,4,'hash',0 FROM messages`, media)
	lifecycleExec(t, s, `INSERT INTO exports(account,path,filter_json,count,bytes,created_at) VALUES('lifecycle',?,'{}',1,4,0)`, export)
	return s, media, export
}

func TestPurgeMediaPathsCommitsQueueWithoutUnlinkAndResumes(t *testing.T) {
	s, media, export := purgeFixture(t)
	orphan := filepath.Join(s.dir, "media", "orphan.ogg")
	if err := os.WriteFile(orphan, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := purgePaths(t, s, s.account, media, export, orphan, media); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"media_files", "exports"} {
		if n := lifecycleCount(t, s, `SELECT count(*) FROM `+table); n != 0 {
			t.Fatalf("%s rows=%d", table, n)
		}
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM files_to_unlink`); n != 3 {
		t.Fatalf("queued=%d", n)
	}
	for _, path := range []string{media, export, orphan} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("unlinked before caller: %v", err)
		}
	}
	if err := purgePaths(t, s, s.account, media, export, orphan); err != nil {
		t.Fatal(err)
	}
	if err := s.Cleanup(context.Background(), Retention{}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{media, export, orphan} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("janitor did not unlink: %v", err)
		}
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM files_to_unlink`); n != 0 {
		t.Fatalf("queued=%d", n)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM messages`); n != 1 {
		t.Fatalf("messages=%d", n)
	}
}

func TestPurgeMediaPathsRollsBackEntireBatch(t *testing.T) {
	for _, failure := range []string{"export_delete", "queue_insert", "invalid_last_path"} {
		t.Run(failure, func(t *testing.T) {
			s, media, export := purgeFixture(t)
			paths := []string{media, export}
			switch failure {
			case "export_delete":
				lifecycleExec(t, s, `CREATE TRIGGER purge_fail BEFORE DELETE ON exports BEGIN SELECT RAISE(ABORT,'purge failure'); END`)
			case "queue_insert":
				lifecycleExec(t, s, `CREATE TRIGGER purge_fail BEFORE INSERT ON files_to_unlink WHEN new.path LIKE '%.ndjson' BEGIN SELECT RAISE(ABORT,'purge failure'); END`)
			case "invalid_last_path":
				paths = append(paths, filepath.Join(t.TempDir(), "outside.ogg"))
			}
			if err := purgePaths(t, s, s.account, paths...); err == nil {
				t.Fatal("expected failure")
			}
			for _, table := range []string{"media_files", "exports"} {
				if n := lifecycleCount(t, s, `SELECT count(*) FROM `+table); n != 1 {
					t.Fatalf("partial delete %s: %d", table, n)
				}
			}
			if n := lifecycleCount(t, s, `SELECT count(*) FROM files_to_unlink`); n != 0 {
				t.Fatalf("partial queue: %d", n)
			}
		})
	}
}

func TestPurgeMediaPathsRejectsUnsafePathsAndAccounts(t *testing.T) {
	s, media, _ := purgeFixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(s.dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(s.dir, "store.db"), filepath.Join(s.dir, "db-alias")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", s.dir, filepath.Join(s.dir, "store.db"), filepath.Join(s.dir, "store.db-wal"), filepath.Join(s.dir, "db-alias"), filepath.Join(s.dir, "alias", "file"), filepath.Join(outside, "file"), "../escape"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if err := purgePaths(t, s, s.account, path); err == nil {
				t.Fatalf("accepted unsafe %q", path)
			}
		})
	}
	if err := purgePaths(t, s, "other", media); !errors.Is(err, engine.ErrAccountMismatch) {
		t.Fatalf("account error=%v", err)
	}
	if err := purgePaths(t, s, "", media); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("empty account error=%v", err)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM files_to_unlink`); n != 0 {
		t.Fatalf("unsafe queue=%d", n)
	}
}

func TestPurgeMediaPathsCanonicalExportsAndForeignMetadata(t *testing.T) {
	data := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(data, "accounts", "primary", "store.db"), "primary")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	export := filepath.Join(data, "exports", "own.ndjson")
	foreign := filepath.Join(data, "exports", "foreign.ndjson")
	lifecycleExec(t, s, `INSERT INTO exports(account,path,filter_json,count,bytes,created_at) VALUES('primary',?,'{}',0,0,0),('other',?,'{}',0,0,0)`, export, foreign)
	if err := purgePaths(t, s, "primary", export, foreign); !errors.Is(err, engine.ErrAccountMismatch) {
		t.Fatalf("foreign error=%v", err)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM exports`); n != 2 {
		t.Fatalf("partial delete=%d", n)
	}
	if err := purgePaths(t, s, "primary", export); err != nil {
		t.Fatal(err)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM exports WHERE account='other'`); n != 1 {
		t.Fatalf("foreign rows=%d", n)
	}
	if err := purgePaths(t, s, "primary", filepath.Join(data, "accounts", "other", "media.ogg")); err == nil {
		t.Fatal("accepted another account root")
	}
}

func TestPurgeMediaPathsMatchesRelativeMetadata(t *testing.T) {
	s, media, _ := purgeFixture(t)
	relative, err := filepath.Rel(s.dir, media)
	if err != nil {
		t.Fatal(err)
	}
	lifecycleExec(t, s, `UPDATE media_files SET path=?`, relative)
	if err := purgePaths(t, s, s.account, media); err != nil {
		t.Fatal(err)
	}
	if n := lifecycleCount(t, s, `SELECT count(*) FROM media_files`); n != 0 {
		t.Fatalf("relative metadata left=%d", n)
	}
	if err := s.Cleanup(context.Background(), Retention{}); err != nil {
		t.Fatal(err)
	}
}
