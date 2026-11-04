package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
)

// IsolateHome redirects both Unix and Windows client/config locations. HOME
// alone does not isolate os.UserHomeDir or Claude Desktop on Windows.
func IsolateHome(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	for key, name := range map[string]string{
		"HOME": "home", "USERPROFILE": "home", "APPDATA": "roaming", "LOCALAPPDATA": "local",
		"XDG_CONFIG_HOME": "config", "XDG_DATA_HOME": "share", "XDG_STATE_HOME": "state", "XDG_CACHE_HOME": "cache",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, path)
	}
	return root
}

// NativeSecrets exercises the platform's production backend with namespaces
// bound ONLY to the caller's unique temporary data directory. Native keyring
// tests must run in an unlocked interactive session, not OpenSSH session 0.
// Cleanup removes and reads back every synthetic slot even after test failure.
func NativeSecrets(t testing.TB, dataDir string, accounts ...string) {
	t.Helper()
	dir, err := filepath.Abs(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	temp, err := filepath.Abs(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(temp, dir)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		t.Fatal("native secrets require a unique directory beneath the test temp root")
	}
	mode := "file"
	if runtime.GOOS != "linux" {
		mode = "keyring"
	}
	t.Setenv("TELEGRAM_MCP_SECRETS", mode)
	if mode == "file" {
		return
	}
	namespaces := []string{"http-token:" + dir}
	for _, account := range accounts {
		namespaces = append(namespaces, "telegram:"+dir+":"+account)
	}
	for _, namespace := range namespaces {
		s, err := secrets.Open(namespace, secrets.Options{Mode: mode})
		if err != nil {
			t.Fatalf("native keyring requires an interactive unlocked session: %v", err)
		}
		t.Cleanup(func() {
			if err := s.DeleteAll(); err != nil {
				t.Errorf("remove synthetic credentials: %v", err)
			}
			for _, key := range []secrets.Key{secrets.APIID, secrets.APIHash, secrets.Session} {
				if _, err := s.Get(key); err != secrets.ErrNotFound {
					t.Errorf("synthetic credential still present: %v", err)
				}
			}
		})
	}
}
