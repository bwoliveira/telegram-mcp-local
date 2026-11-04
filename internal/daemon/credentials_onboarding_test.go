package daemon

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

func TestSaveAccountCredentialsFirstRun(t *testing.T) {
	root := testutil.IsolateHome(t)
	testutil.NativeSecrets(t, filepath.Join(root, "data"), "primary")
	t.Setenv("TELEGRAM_API_ID", "")
	t.Setenv("TELEGRAM_API_HASH", "")
	dir := filepath.Join(root, "data")
	if _, _, _, err := PrepareAccountCredentials(dir, "primary"); !errors.Is(err, ErrCredentialsRequired) {
		t.Fatalf("missing credentials: %v", err)
	}
	hash := strings.Repeat("a", 32) // Synthetic, never a Telegram credential.
	for _, id := range []string{"0", "-1", "+1", "1.5", "99999999999999999999999999999999", ""} {
		if err := SaveAccountCredentials(dir, "primary", id, hash); err == nil {
			t.Fatal("invalid ID accepted")
		}
	}
	for _, h := range []string{"", strings.Repeat("g", 32), strings.Repeat("a", 31), strings.Repeat("a", 33)} {
		if err := SaveAccountCredentials(dir, "primary", "123", h); err == nil {
			t.Fatal("invalid hash accepted")
		}
	}
	if err := SaveAccountCredentials(dir, "primary", "123", hash); err != nil {
		t.Fatal(err)
	}
	id, got, session, err := PrepareAccountCredentials(dir, "primary")
	if err != nil || id != 123 || string(got) != hash || session == nil {
		t.Fatal("vault round trip failed")
	}
	if err := SaveAccountCredentials(dir, "primary", "124", strings.Repeat("b", 32)); err == nil {
		t.Fatal("replaced pinned credentials")
	}
	id, got, _, err = PrepareAccountCredentials(dir, "primary")
	if err != nil || id != 123 || string(got) != hash {
		t.Fatal("failed save changed vault")
	}
}
