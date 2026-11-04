package service

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
)

func TestTokenGeneratedPersistedAndPrivate(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("encrypted file backend is Linux-only")
	}
	t.Setenv("TELEGRAM_MCP_SECRETS", "file")
	dir := filepath.Join(t.TempDir(), "data")
	token, err := ResolveToken(dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != 32 {
		t.Fatal("token must encode 32 random bytes")
	}
	again, err := ResolveToken(dir, "", false)
	if err != nil || again != token {
		t.Fatalf("token not persisted: %v", err)
	}
	other, err := ResolveToken(filepath.Join(t.TempDir(), "other"), "", true)
	if err != nil || other == token {
		t.Fatal("token reused across data folders")
	}
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0600)
		if d.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Errorf("unsafe mode %s: %o", path, info.Mode().Perm())
		}
		if !d.IsDir() {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(b, []byte(token)) {
				t.Error("token stored in plaintext")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTokenMissingInvalidAndConflictingSeedFailClosed(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("file backend")
	}
	t.Setenv("TELEGRAM_MCP_SECRETS", "file")
	dir := filepath.Join(t.TempDir(), "data")
	if _, err := ResolveToken(dir, "", false); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("missing token: %v", err)
	}
	if _, err := ResolveToken(dir, "synthetic-invalid-marker", true); err == nil || bytes.Contains([]byte(err.Error()), []byte("synthetic-invalid-marker")) {
		t.Fatalf("unsafe seed error: %v", err)
	}
	seed := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	token, err := ResolveToken(dir, seed, true)
	if err != nil || token != seed {
		t.Fatalf("seed: %v", err)
	}
	_, err = ResolveToken(dir, base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)), true)
	if err == nil {
		t.Fatal("conflicting env seed replaced persisted credential")
	}
	again, err := ResolveToken(dir, "", false)
	if err != nil || again != seed {
		t.Fatal("persisted credential was overwritten")
	}
}

func TestTokenBackendSelectionIsPersistedAndConflictsFailClosed(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("file backend")
	}
	t.Setenv("TELEGRAM_MCP_SECRETS", "file")
	dir := filepath.Join(t.TempDir(), "data")
	token, err := ResolveToken(dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	marker, err := os.ReadFile(filepath.Join(dir, "http-token.backend"))
	if err != nil || string(marker) != "file" {
		t.Fatalf("backend not persisted: %q %v", marker, err)
	}
	t.Setenv("TELEGRAM_MCP_SECRETS", "")
	again, err := ResolveToken(dir, "", false)
	if err != nil || again != token {
		t.Fatalf("default changed persisted backend: %v", err)
	}
	t.Setenv("TELEGRAM_MCP_SECRETS", "keyring")
	if _, err := ResolveToken(dir, "", true); err == nil {
		t.Fatal("backend override created second credential")
	}
	t.Setenv("TELEGRAM_MCP_SECRETS", "")
	if err := os.WriteFile(filepath.Join(dir, "http-token.backend"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveToken(dir, "", true); !errors.Is(err, secrets.ErrCorrupt) {
		t.Fatalf("invalid backend accepted: %v", err)
	}
}

func TestConcurrentTokenCreationReturnsOneCredential(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("file backend")
	}
	t.Setenv("TELEGRAM_MCP_SECRETS", "file")
	dir := filepath.Join(t.TempDir(), "data")
	var wg sync.WaitGroup
	tokens := make(chan string, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := ResolveToken(dir, "", true)
			if err != nil {
				t.Error(err)
			}
			tokens <- v
		}()
	}
	wg.Wait()
	close(tokens)
	var first string
	for v := range tokens {
		if first == "" {
			first = v
		}
		if v != first {
			t.Error("concurrent creation rotated the token")
		}
	}
}
