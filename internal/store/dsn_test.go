package store

import (
	"net/url"
	"strings"
	"testing"
)

// SQLite rejects a file URI whose authority is not empty or "localhost". A
// Windows path must not become one (url.URL{Scheme: "file"} made it
// "file://C:%5C..."); every store test fails natively on Windows otherwise.
func TestFileDSNHasNoAuthority(t *testing.T) {
	q := url.Values{"_pragma": {"busy_timeout=5000"}}
	for path, want := range map[string]string{
		`C:\Users\someone\AppData\Local\Telegram MCP\accounts\primary\store.db`: `file:C:%5CUsers%5Csomeone%5CAppData%5CLocal%5CTelegram%20MCP%5Caccounts%5Cprimary%5Cstore.db?_pragma=busy_timeout%3D5000`,
		`/home/someone/.local/share/telegram-mcp/accounts/primary/store.db`:     `file:/home/someone/.local/share/telegram-mcp/accounts/primary/store.db?_pragma=busy_timeout%3D5000`,
		`/tmp/a?b#c.db`: `file:/tmp/a%3Fb%23c.db?_pragma=busy_timeout%3D5000`,
	} {
		got := fileDSN(path, q)
		if got != want {
			t.Errorf("fileDSN(%q) = %q, want %q", path, got, want)
		}
		if strings.HasPrefix(got, "file://") {
			t.Errorf("fileDSN(%q) has an authority: %q", path, got)
		}
	}
}
