package service

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
)

func TestSEC_F02_DataCopyDoesNotIncludeDecryptionKey(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux encrypted file backend")
	}
	t.Setenv("TELEGRAM_MCP_SECRETS", "file")
	dir := filepath.Join(t.TempDir(), "data")
	token, err := ResolveToken(dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(dir, tokenKeyDir(dir))
	if err != nil || rel == "." || filepath.IsLocal(rel) {
		t.Fatal("decryption key must be outside the copied data tree", rel, err)
	}
	copied := filepath.Join(t.TempDir(), "copied")
	if err := os.MkdirAll(copied, 0700); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		dest := filepath.Join(copied, rel)
		if d.IsDir() {
			return os.MkdirAll(dest, 0700)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dest, raw, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := secrets.Open("http-token:"+dir, secrets.Options{Mode: "file", DataDir: filepath.Join(copied, "secrets"), KeyDir: filepath.Join(t.TempDir(), "missing-key")})
	if err == nil {
		if raw, err := store.Get(secrets.Session); err == nil || string(raw) == token {
			t.Fatal("copied data decrypted without original sibling key")
		}
	} else if !errors.Is(err, secrets.ErrCorrupt) {
		t.Fatal("unexpected copy error", err)
	}
	// Positive control: the original encrypted credential still works.
	if got, err := ResolveToken(dir, "", false); err != nil || got != token {
		t.Fatal("original credential unavailable", err)
	}
}

func TestPrepareDataFilesArePrivateAndRejectLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix modes")
	}
	dir := filepath.Join(t.TempDir(), "data")
	if err := PrepareData(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := PrepareData(dir); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"state.db", "state.db-wal", "state.db-shm"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("unsafe sqlite file", name, err)
		}
	}
	path := filepath.Join(dir, "state.db")
	os.Remove(path)
	os.Symlink(filepath.Join(t.TempDir(), "not-a-database"), path)
	if err := PrepareData(dir); err == nil {
		t.Fatal("followed database symlink")
	}
}
