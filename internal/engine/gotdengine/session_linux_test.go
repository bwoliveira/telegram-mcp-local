//go:build linux

package gotdengine

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
	"github.com/gotd/td/session"
)

// Exercise the real offline CLI with a synthetic credential in isolated encrypted
// storage. No client is created, no Telegram RPC is sent, and no real state is read.
func TestSessionOfflineStatusMeasuresStoredFormat(t *testing.T) {
	ctx := context.Background()
	state := filepath.Join(t.TempDir(), "state")
	store, err := secrets.Open("primary", secrets.Options{Mode: "file", DataDir: filepath.Join(state, "secrets"), KeyDir: state + "-keys"})
	if err != nil {
		t.Fatal(err)
	}
	defer store.DeleteAll()
	raw := SecretsSession{Store: store}
	want := syntheticSession()
	legacy := legacySession(t, want)
	if err := raw.StoreSession(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "telegram-poc")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/telegram-poc")
	build.Dir = "../../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build offline CLI: %v: %s", err, output)
	}
	status := func(expectedBytes int, portable bool) {
		t.Helper()
		output, err := exec.CommandContext(ctx, binary, "status", "--state", state).Output()
		if err != nil {
			t.Fatalf("run offline status: %v", err)
		}
		var got struct {
			Offline  bool `json:"offline"`
			Present  bool `json:"session_present"`
			Bytes    int  `json:"session_bytes"`
			Portable bool `json:"session_le_1800"`
		}
		if err := json.Unmarshal(output, &got); err != nil {
			t.Fatal("invalid status JSON")
		}
		if !got.Offline || !got.Present || got.Bytes != expectedBytes || got.Portable != portable {
			t.Fatalf("incorrect offline size: bytes=%d portable=%t offline=%t present=%t", got.Bytes, got.Portable, got.Offline, got.Present)
		}
		t.Logf("offline status: session_bytes=%d session_le_1800=%t", got.Bytes, got.Portable)
	}
	// Status must not claim a hypothetical compact size before migration.
	status(len(legacy), false)
	loader := &session.Loader{Storage: sessAdapter{raw}}
	got, err := loader.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := loader.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	compact, err := raw.LoadSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) == 0 || len(compact) > 1800 {
		t.Fatalf("stored session size = %d, want 1..1800", len(compact))
	}
	status(len(compact), true)
	got, err = (&session.Loader{Storage: sessAdapter{raw}}).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	requireSessionIdentity(t, want, got)
}
