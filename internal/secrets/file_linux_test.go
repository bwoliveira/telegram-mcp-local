package secrets

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func fileOptions(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	return Options{Mode: "file", DataDir: filepath.Join(root, "state"), KeyDir: filepath.Join(root, "config", "keys")}
}
func mustFile(t *testing.T, account string, o Options) Store {
	t.Helper()
	s, err := openWith(account, o, nil, "linux")
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestFileRoundTripDeleteAndIsolation(t *testing.T) {
	o := fileOptions(t)
	a := mustFile(t, "a", o)
	b := mustFile(t, "b", o)
	value := []byte("synthetic-file-secret")
	for _, k := range allKeys {
		if err := a.Set(k, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Set(Session, []byte("other")); err != nil {
		t.Fatal(err)
	}
	a = mustFile(t, "a", o)
	for _, k := range allKeys {
		got, err := a.Get(k)
		if err != nil || !bytes.Equal(got, value) {
			t.Fatal("round trip failed", err)
		}
	}
	f := a.(*fileStore)
	data, err := os.ReadFile(f.dataPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, value) {
		t.Fatal("plaintext in data")
	}
	if err := a.Set(Session, value); err != nil {
		t.Fatal(err)
	}
	next, _ := os.ReadFile(f.dataPath)
	if bytes.Equal(data, next) {
		t.Fatal("nonce reused")
	}
	if err := a.Delete(APIHash); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Get(APIHash); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := a.DeleteAll(); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{f.dataPath, f.keyPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("account file remains")
		}
	}
	for _, k := range allKeys {
		if _, err := a.Get(k); !errors.Is(err, ErrNotFound) {
			t.Fatal(err)
		}
	}
	if got, err := b.Get(Session); err != nil || string(got) != "other" {
		t.Fatal("other account affected")
	}
	if err := a.DeleteAll(); err != nil {
		t.Fatal(err)
	}
}
func TestFileAuthenticationFailures(t *testing.T) {
	for _, kind := range []string{"wrong-key", "tamper", "truncated", "missing-key"} {
		t.Run(kind, func(t *testing.T) {
			o := fileOptions(t)
			s := mustFile(t, "a", o)
			if err := s.Set(Session, []byte("test-only")); err != nil {
				t.Fatal(err)
			}
			f := s.(*fileStore)
			switch kind {
			case "wrong-key":
				if err := os.WriteFile(f.keyPath, bytes.Repeat([]byte{42}, 32), 0600); err != nil {
					t.Fatal(err)
				}
			case "tamper":
				b, _ := os.ReadFile(f.dataPath)
				b[len(b)-1] ^= 1
				if err := os.WriteFile(f.dataPath, b, 0600); err != nil {
					t.Fatal(err)
				}
			case "truncated":
				if err := os.WriteFile(f.dataPath, []byte("short"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-key":
				if err := os.Remove(f.keyPath); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Get(Session); err == nil {
				t.Fatal("corrupt store accepted")
			}
			if err := s.Set(Session, []byte("replacement")); err == nil {
				t.Fatal("corrupt store overwritten")
			}
			if _, err := openWith("a", o, nil, "linux"); err == nil {
				t.Fatal("corrupt store opened")
			}
			// Logout must work even if the ciphertext or key is damaged.
			if err := s.DeleteAll(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestFileRefusesUnsafePermissionsAndSymlinks(t *testing.T) {
	for _, target := range []string{"data", "key", "data-dir", "key-dir", "symlink"} {
		t.Run(target, func(t *testing.T) {
			o := fileOptions(t)
			s := mustFile(t, "a", o)
			if err := s.Set(Session, []byte("fake")); err != nil {
				t.Fatal(err)
			}
			f := s.(*fileStore)
			p := map[string]string{"data": f.dataPath, "key": f.keyPath, "data-dir": o.DataDir, "key-dir": o.KeyDir, "symlink": f.dataPath}[target]
			if target == "symlink" {
				if err := os.Rename(p, p+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p+".real", p); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Chmod(p, 0777); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.Get(Session); err == nil {
				t.Fatal("unsafe path accepted")
			}
			if _, err := openWith("a", o, nil, "linux"); err == nil {
				t.Fatal("unsafe path opened")
			}
		})
	}
}
func TestFileAccountBindingAndRedaction(t *testing.T) {
	const secret = "synthetic-no-leak-marker"
	o := fileOptions(t)
	o.DataDir = filepath.Join(o.DataDir, secret)
	a := mustFile(t, secret, o).(*fileStore)
	b := mustFile(t, "b", o).(*fileStore)
	if err := a.Set(Session, []byte(secret)); err != nil {
		t.Fatal(err)
	}
	// Even copying both files into another account cannot bypass namespace AAD.
	for _, pair := range [][2]string{{a.dataPath, b.dataPath}, {a.keyPath, b.keyPath}} {
		data, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pair[1], data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.Get(Session); !errors.Is(err, ErrCorrupt) {
		t.Fatal("cross-account ciphertext accepted")
	}
	if err := os.Chmod(a.keyPath, 0644); err != nil {
		t.Fatal(err)
	}
	_, readErr := a.Get(Session)
	for _, err := range []error{readErr, a.Set(Session, []byte(secret)), a.Delete(Session), a.DeleteAll()} {
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatal("missing or unsafe file error")
		}
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, a), secret) {
			t.Fatal("unsafe file formatting")
		}
	}
}

func TestSelectionAndFallback(t *testing.T) {
	t.Setenv("TELEGRAM_MCP_SECRETS", "")
	ring := &fakeRing{failure: errors.New("synthetic backend failure")}
	o := fileOptions(t)
	o.Mode = ""
	s, err := openWith("a", o, ring, "linux")
	if err != nil {
		t.Fatal(err)
	}
	if s.Backend() != File || !s.Fallback() {
		t.Fatal("fallback not explicit")
	}
	if err := s.Set(Session, []byte("synthetic-fallback-session")); err != nil {
		t.Fatal(err)
	}
	if value, err := s.Get(Session); err != nil || string(value) != "synthetic-fallback-session" {
		t.Fatal("fallback not usable", err)
	}
	o.Mode = "keyring"
	if _, err := openWith("a", o, ring, "linux"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("forced keyring fell back", err)
	}
	o.Mode = ""
	if _, err := openWith("a", o, ring, "darwin"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("unexpected non-Linux fallback", err)
	}
	t.Setenv("TELEGRAM_MCP_SECRETS", "file")
	o.Mode = ""
	s, err = openWith("b", o, ring, "linux")
	if err != nil || s.Fallback() {
		t.Fatal("env file mode failed", err)
	}
	t.Setenv("TELEGRAM_MCP_SECRETS", "unknown-sensitive-mode")
	if _, err := openWith("a", o, ring, "linux"); err == nil || strings.Contains(err.Error(), "unknown-sensitive-mode") {
		t.Fatal("unsafe mode accepted or echoed")
	}
}
func TestFileConcurrentHandles(t *testing.T) {
	o := fileOptions(t)
	a := mustFile(t, "a", o)
	b := mustFile(t, "a", o)
	var wg sync.WaitGroup
	for i, k := range allKeys {
		wg.Add(1)
		go func(i int, k Key) {
			defer wg.Done()
			s := a
			if i%2 == 1 {
				s = b
			}
			for j := 0; j < 8; j++ {
				if err := s.Set(k, []byte("fake")); err != nil {
					t.Error(err)
				}
			}
		}(i, k)
	}
	wg.Wait()
	for _, k := range allKeys {
		if _, err := a.Get(k); err != nil {
			t.Fatal(err)
		}
	}
}
func TestFileRejectsOverlappingDirectories(t *testing.T) {
	o := fileOptions(t)
	o.KeyDir = filepath.Join(o.DataDir, "keys")
	if _, err := openWith("a", o, nil, "linux"); err == nil {
		t.Fatal("key inside data directory accepted")
	}
}
func TestFileXDGDefaults(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("TELEGRAM_MCP_SECRETS", "file")
	s, err := Open("a", Options{})
	if err != nil {
		t.Fatal(err)
	}
	f := s.(*fileStore)
	if !strings.HasPrefix(f.dataPath, filepath.Join(root, "state", "telegram-mcp")) || !strings.HasPrefix(f.keyPath, filepath.Join(root, "config", "telegram-mcp", "keys")) {
		t.Fatal("wrong default locations")
	}
}
