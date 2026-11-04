package panel

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/platform"
)

// skipWithoutBackup skips an opt-in real-store case, saying how to run it.
func skipWithoutBackup(t *testing.T, real bool, source, variable string) {
	t.Helper()
	if real && source == "" {
		t.Skip("set " + variable + "=/absolute/path/to/isolated-backup.db (an sqlite3 backup of the store, opened read-only) to measure a real-store copy")
	}
}

// requireIsolatedBackup accepts any caller-chosen copy of a store, but
// refuses what could be the live one: it must be an absolute path to a
// regular file (not a symlink), outside every application data directory,
// and without uncheckpointed WAL beside it (a finished backup made with the
// SQLite backup API or VACUUM INTO has none; a database in use may). The fixture then opens it
// with mode=ro and VACUUMs it into a private temporary copy before
// store.Open may migrate anything. Failures name no path or content.
func requireIsolatedBackup(t *testing.T, source string) {
	t.Helper()
	if err := isolatedBackup(source); err != nil {
		t.Fatal("real DB must be an isolated backup: " + err.Error())
	}
}

func isolatedBackup(source string) error {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return errors.New("path is not absolute and clean")
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("not a regular file")
	}
	if wal, err := os.Lstat(source + "-wal"); err == nil && wal.Size() > 0 {
		return errors.New("uncheckpointed WAL beside it suggests a database in use")
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(source))
	if err != nil {
		return errors.New("directory unresolvable")
	}
	var live []string
	if home, err := os.UserHomeDir(); err == nil {
		live = append(live, filepath.Join(home, ".telegram-mcp"))
	}
	if d, err := platform.DataDir(); err == nil && d != "" {
		live = append(live, d)
	}
	if d := os.Getenv("TELEGRAM_MCP_DATA"); d != "" {
		live = append(live, d)
	}
	for _, d := range live {
		if resolved, err := filepath.EvalSymlinks(d); err == nil {
			d = resolved
		}
		if dir == d || strings.HasPrefix(dir, d+string(filepath.Separator)) {
			return errors.New("inside an application data directory")
		}
	}
	return nil
}

func TestIsolatedBackupGuard(t *testing.T) {
	dir := t.TempDir()
	backup := filepath.Join(dir, "backup.db")
	if err := os.WriteFile(backup, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := isolatedBackup(backup); err != nil {
		t.Fatalf("isolated backup refused: %v", err)
	}
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(backup, link); err != nil {
		t.Fatal(err)
	}
	inUse := filepath.Join(dir, "in-use.db")
	for _, name := range []string{inUse, inUse + "-wal", backup + "-wal"} {
		content := []byte("x")
		if name == backup+"-wal" {
			content = nil // an empty, checkpointed WAL is not a database in use
		}
		if err := os.WriteFile(name, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := isolatedBackup(backup); err != nil {
		t.Fatalf("backup with empty WAL refused: %v", err)
	}
	data := filepath.Join(dir, "data")
	t.Setenv("TELEGRAM_MCP_DATA", data)
	inData := filepath.Join(data, "accounts", "primary", "store.db")
	if err := os.MkdirAll(filepath.Dir(inData), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inData, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for _, bad := range []string{"", "backup.db", dir + "/../" + filepath.Base(dir) + "/backup.db", dir, link, inUse, inData,
		filepath.Join(home, ".telegram-mcp", "accounts", "primary", "store.db")} {
		if err := isolatedBackup(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
