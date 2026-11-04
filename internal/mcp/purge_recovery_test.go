package mcp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

const recoveryQuarantine = ".purge-0123456789abcdef0123456789abcdef"

func seedRecoveryFile(t *testing.T, root, name string) string {
	t.Helper()
	quarantine := filepath.Join(root, recoveryQuarantine)
	if err := os.MkdirAll(quarantine, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(quarantine, name)
	if err := os.WriteFile(path, []byte("quarantined"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPurgeRecoveryInterruptedRestoreIsIdempotent(t *testing.T) {
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty container", true: "link before unlink"}[linked], func(t *testing.T) {
			root := t.TempDir()
			quarantine := filepath.Join(root, recoveryQuarantine)
			if err := os.Mkdir(quarantine, 0o700); err != nil {
				t.Fatal(err)
			}
			if linked {
				source := seedRecoveryFile(t, root, "voice.ogg")
				if err := os.Link(source, filepath.Join(root, "voice.ogg")); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				if err := recoverMediaPurges(root); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Lstat(quarantine); !os.IsNotExist(err) {
				t.Fatalf("quarantine remains after recovery: %v", err)
			}
			if linked {
				body, err := os.ReadFile(filepath.Join(root, "voice.ogg"))
				if err != nil || string(body) != "quarantined" {
					t.Fatalf("restored bytes changed: %q %v", body, err)
				}
			}
		})
	}
}

func TestPurgeRecoveryNeverOverwritesRecreatedDestination(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "regular replacement", true: "symlink replacement"}[symlink], func(t *testing.T) {
			root := t.TempDir()
			source := seedRecoveryFile(t, root, "voice.ogg")
			outside := filepath.Join(t.TempDir(), "outside.ogg")
			if err := os.WriteFile(outside, []byte("fresh"), 0o600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(root, "voice.ogg")
			if symlink {
				if err := os.Symlink(outside, dest); err != nil {
					t.Skip(err)
				}
			} else if err := os.WriteFile(dest, []byte("fresh"), 0o600); err != nil {
				t.Fatal(err)
			}
			s := New(Config{MediaDir: root})
			if _, _, _, err := s.PurgeMediaContext(context.Background(), "", 0, 0, false); err == nil {
				t.Fatal("conflicting recovery did not fail visibly")
			}
			for path, want := range map[string]string{source: "quarantined", dest: "fresh", outside: "fresh"} {
				body, err := os.ReadFile(path)
				if err != nil || string(body) != want {
					t.Errorf("recovery changed %s: %q %v", path, body, err)
				}
			}
		})
	}
}

func TestPurgeRecoveryRejectsSymlinksAndLegacyFlatFiles(t *testing.T) {
	for _, mode := range []string{"root symlink", "quarantine symlink", "payload symlink", "legacy flat file", "multiple entries"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			outsideRoot := t.TempDir()
			outside := seedRecoveryFile(t, outsideRoot, "voice.ogg")
			quarantine := filepath.Join(root, recoveryQuarantine)
			var err error
			switch mode {
			case "root symlink":
				root = filepath.Join(root, "root-link")
				err = os.Symlink(outsideRoot, root)
			case "quarantine symlink":
				err = os.Symlink(filepath.Dir(outside), quarantine)
			case "payload symlink":
				if err := os.Mkdir(quarantine, 0o700); err != nil {
					t.Fatal(err)
				}
				err = os.Symlink(outside, filepath.Join(quarantine, "voice.ogg"))
			case "legacy flat file":
				err = os.WriteFile(quarantine, []byte("legacy"), 0o600)
			case "multiple entries":
				seedRecoveryFile(t, root, "one.ogg")
				seedRecoveryFile(t, root, "two.ogg")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := recoverMediaPurges(root); err == nil {
				t.Fatal("unsafe recovery accepted")
			}
			body, err := os.ReadFile(outside)
			if err != nil || string(body) != "quarantined" {
				t.Fatalf("recovery touched outside bytes: %q %v", body, err)
			}
			if _, err := os.Lstat(quarantine); mode != "root symlink" && err != nil {
				t.Fatalf("unsafe quarantine was removed: %v", err)
			}
		})
	}
}

func TestPurgeRecoveryDoesNotFollowSymlinkedChat(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	path := seedRecoveryFile(t, outside, "voice.ogg")
	if err := os.Symlink(outside, filepath.Join(root, "user_1001")); err != nil {
		t.Skip(err)
	}
	if err := recoverMediaPurges(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("recovery followed symlinked ancestor: %v", err)
	}
}

func TestPurgeRecoveryDryRunAndCancellationDoNotMutate(t *testing.T) {
	root := t.TempDir()
	source := seedRecoveryFile(t, root, "voice.ogg")
	s := New(Config{MediaDir: root})
	if _, _, _, err := s.PurgeMediaContext(context.Background(), "", 0, 0, true); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, _, err := s.PurgeMediaContext(ctx, "", 0, 0, false); err == nil {
		t.Fatal("canceled purge was accepted")
	}
	if _, err := os.Lstat(source); err != nil {
		t.Fatalf("read-only/canceled purge moved bytes: %v", err)
	}
}

func TestPurgeRecoveryConfirmationDoesNotExpandApprovedSet(t *testing.T) {
	root := t.TempDir()
	seedRecoveryFile(t, root, "recovered.ogg")
	approved := filepath.Join(root, "approved.ogg")
	if err := os.WriteFile(approved, []byte("approved"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{MediaDir: root})
	n, _, _, fingerprint, err := s.previewMediaPurge("", 0, 0)
	if err != nil || n != 1 {
		t.Fatalf("preview: files=%d err=%v", n, err)
	}
	n, _, err = s.confirmMediaPurge(context.Background(), "", 0, 0, fingerprint)
	if err != nil || n != 1 {
		t.Fatalf("confirm: files=%d err=%v", n, err)
	}
	body, err := os.ReadFile(filepath.Join(root, "recovered.ogg"))
	if err != nil || string(body) != "quarantined" {
		t.Fatalf("confirmation deleted unapproved recovered bytes: %q %v", body, err)
	}
	if _, err := os.Lstat(approved); !os.IsNotExist(err) {
		t.Fatalf("approved file was not removed: %v", err)
	}
}

func TestPurgeRecoveryPreservesStoreMetadata(t *testing.T) {
	s, _, _ := reviewCanonicalServer(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: fake.PrivatePeer, ID: 4}
	d, err := s.DownloadMedia(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	quarantine := filepath.Join(filepath.Dir(d.Path), recoveryQuarantine)
	if err := os.Mkdir(quarantine, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(d.Path, filepath.Join(quarantine, filepath.Base(d.Path))); err != nil {
		t.Fatal(err)
	}
	if n, _, _, err := s.PurgeMediaContext(ctx, "", 365000, 0, false); err != nil || n != 0 {
		t.Fatalf("recover-only purge: files=%d err=%v", n, err)
	}
	assertFreshMediaReference(t, s, key, d.Path)
	if _, err := os.Lstat(d.Path); err != nil {
		t.Fatal(err)
	}
}

func TestPurgeRecoveryRevalidationFailureRollsBack(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "voice.ogg")
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	err = removeApprovedMedia(root, approvedMediaFile{Path: path, Size: info.Size(), ModTime: info.ModTime().UnixNano(), Digest: "not-the-approved-digest"})
	if err == nil {
		t.Fatal("changed bytes were accepted")
	}
	body, err := os.ReadFile(path)
	if err != nil || string(body) != "changed" {
		t.Fatalf("failed revalidation lost original mapping: %q %v", body, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "voice.ogg" {
		t.Fatalf("rollback left hidden bytes: %v %v", entries, err)
	}
}

// Model a process dying immediately after the quarantine rename. The directory
// entry itself records the original basename, without an in-memory rollback.
func TestPurgeRecoveryRestoresInterruptedQuarantineOnNextPurge(t *testing.T) {
	root := t.TempDir()
	chat := filepath.Join(root, "user_1001")
	quarantine := filepath.Join(chat, recoveryQuarantine)
	if err := os.MkdirAll(quarantine, 0o700); err != nil {
		t.Fatal(err)
	}
	body := []byte("interrupted purge")
	if err := os.WriteFile(filepath.Join(quarantine, "voice.ogg"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	s := New(Config{MediaDir: root})
	// A fresh purge's age filter must apply to recovered files, not silently
	// delete an interrupted operation's bytes without a new selection.
	n, _, _, err := s.PurgeMediaContext(context.Background(), "", 365000, 0, false)
	if err != nil || n != 0 {
		t.Fatalf("next purge: removed=%d err=%v", n, err)
	}
	got, err := os.ReadFile(filepath.Join(chat, "voice.ogg"))
	if err != nil || string(got) != string(body) {
		t.Fatalf("original mapping not recovered: body=%q err=%v", got, err)
	}
	if _, err := os.Lstat(quarantine); !os.IsNotExist(err) {
		t.Fatalf("completed recovery left quarantine: %v", err)
	}
	inv, err := s.Inventory(context.Background())
	if err != nil || inv.Files != 1 || inv.Bytes != int64(len(body)) {
		t.Fatalf("recovered bytes not in inventory: %+v err=%v", inv, err)
	}
	// Retention/purge sees recovered bytes on a later pass, and recovery is
	// idempotent even when another Server instance scans the same directory.
	s = New(Config{MediaDir: root})
	n, b, _, err := s.PurgeMediaContext(context.Background(), "", 0, 0, false)
	if err != nil || n != 1 || b != int64(len(body)) {
		t.Fatalf("recovered file not purgeable: removed=%d bytes=%d err=%v", n, b, err)
	}
}
