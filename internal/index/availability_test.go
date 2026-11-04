package index

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMissingCompatibilityStoreHasTelegramGuidance(t *testing.T) {
	x, err := Open(filepath.Join(t.TempDir(), "missing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	_, err = x.Coverage(context.Background())
	if err == nil || err.Error() != "the inherited local index is unavailable; Telegram history coverage is not established" {
		t.Fatalf("missing index guidance: %v", err)
	}
}
