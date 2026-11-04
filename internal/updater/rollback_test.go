package updater

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppImageRelaunchFailureRestoresOldVersion(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Telegram-MCP.AppImage")
	downloaded := filepath.Join(dir, "candidate")
	mustWriteFile(t, target, "healthy-v1", 0o755)
	mustWriteFile(t, downloaded, "candidate-v2", 0o755)

	old := relaunch
	relaunch = func(string, map[string]string) error { return fmt.Errorf("synthetic helper start failure") }
	t.Cleanup(func() { relaunch = old })

	err := installAppImage(downloaded, target, filepath.Join(dir, "updates"), "1.0.0", "2.0.0")
	if err == nil || !strings.Contains(err.Error(), "synthetic helper start failure") {
		t.Fatalf("install error = %v", err)
	}
	if got, readErr := os.ReadFile(target); readErr != nil || string(got) != "healthy-v1" {
		t.Fatalf("installed target = %q, err=%v; want restored v1", got, readErr)
	}
	if _, statErr := os.Stat(target + ".old"); !os.IsNotExist(statErr) {
		t.Fatalf("rollback backup remained after synchronous restoration: %v", statErr)
	}
}

func TestMacRelaunchFailureRestoresOldBundle(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Telegram MCP.app")
	backup := target + ".old"
	mustWriteFile(t, filepath.Join(target, "version"), "healthy-v1", 0o644)
	mustWriteFile(t, filepath.Join(backup, "version"), "candidate-v2", 0o644)

	old := relaunch
	relaunchReached := false
	relaunch = func(string, map[string]string) error {
		relaunchReached = true
		return fmt.Errorf("synthetic open failure")
	}
	t.Cleanup(func() { relaunch = old })

	// Production stages the candidate at .old and passes the same path as the
	// next and backup arguments so Darwin exercises RENAME_SWAP.
	err := activateReplacement("dmg", backup, target, backup, filepath.Join(dir, "updates"), "1.0.0", "2.0.0",
		target, target, target, `open "$TARGET"`)
	if !relaunchReached {
		t.Fatal("activation failed before reaching the injected relaunch failure")
	}
	if err == nil || !strings.Contains(err.Error(), "synthetic open failure") {
		t.Fatalf("activate error = %v", err)
	}
	got, readErr := os.ReadFile(filepath.Join(target, "version"))
	if readErr != nil || string(got) != "healthy-v1" {
		t.Fatalf("restored bundle version = %q, err=%v", got, readErr)
	}
}

func TestBundleCandidateRemovedWhenPendingStateCannotBeWritten(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Telegram MCP.app")
	backup := target + ".old"
	mustWriteFile(t, filepath.Join(target, "version"), "healthy-v1", 0o644)
	mustWriteFile(t, filepath.Join(backup, "version"), "candidate-v2", 0o644)
	blockedStateDir := filepath.Join(dir, "not-a-directory")
	mustWriteFile(t, blockedStateDir, "blocked", 0o600)

	err := activateReplacement("dmg", backup, target, backup, blockedStateDir, "1.0.0", "2.0.0",
		target, target, backup, `true`)
	if err == nil {
		t.Fatal("activation unexpectedly succeeded with an unusable state directory")
	}
	if _, statErr := os.Stat(backup); !os.IsNotExist(statErr) {
		t.Fatalf("unactivated bundle candidate was not removed: %v", statErr)
	}
	if got, readErr := os.ReadFile(filepath.Join(target, "version")); readErr != nil || string(got) != "healthy-v1" {
		t.Fatalf("healthy target changed: %q, %v", got, readErr)
	}
}

func TestAppImageHelperRestoresWhenHealthMarkerIsMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("AppImage helper requires /bin/sh")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "Telegram-MCP.AppImage")
	backup := target + ".old"
	marker := filepath.Join(dir, "health")
	state := filepath.Join(dir, pendingUpdateName)
	mustWriteFile(t, target, "#!/bin/sh\ntrap '' TERM\nwhile :; do :; done\n", 0o755)
	mustWriteFile(t, backup, "#!/bin/sh\nprintf healthy-v1 > \"$RESTORED\"\n", 0o755)

	cmd := exec.Command("/bin/sh", "-c", appImageRelaunchScript)
	cmd.Env = append(os.Environ(),
		"TARGET="+target, "EXECUTABLE="+target, "OLD="+backup, "MARKER="+marker,
		"STATE="+state, "VERSION=2.0.0", "DEADLINE_SECONDS=0",
		"RESTORED="+filepath.Join(dir, "restored"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rollback helper: %v: %s", err, out)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "restored")); err != nil || string(got) != "healthy-v1" {
		t.Fatalf("restored executable result = %q, err=%v", got, err)
	}
	if got, err := os.ReadFile(target); err != nil || !strings.Contains(string(got), "healthy-v1") {
		t.Fatalf("installed target was not restored: %q, err=%v", got, err)
	}
}

func TestMissingHealthMarkerPastDeadlineRestoresBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("AppImage relaunch fixture requires a Unix executable")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "Telegram-MCP.AppImage")
	backup := target + ".old"
	mustWriteFile(t, target, "#!/bin/sh\nexit 2\n", 0o755)
	mustWriteFile(t, backup, "#!/bin/sh\nexit 0\n", 0o755)
	deadline := time.Date(2026, 11, 3, 20, 0, 0, 0, time.UTC)
	candidateIdentity, _ := identityForFile(target, "2.0.0")
	previousIdentity, _ := identityForFile(backup, "1.0.0")
	pending := pendingUpdate{Kind: "appimage", Target: target, Backup: backup, Executable: target, Version: "2.0.0", Marker: filepath.Join(dir, "health"), Deadline: deadline, CandidateIdentity: candidateIdentity, PreviousIdentity: previousIdentity}
	if err := writePendingUpdate(dir, pending); err != nil {
		t.Fatal(err)
	}

	if err := reconcileStartupAt(dir, "2.0.0", deadline.Add(time.Second)); err == nil || !strings.Contains(err.Error(), "rolled back") {
		t.Fatalf("expired pending update error = %v", err)
	}
	if got, err := os.ReadFile(target); err != nil || !strings.Contains(string(got), "exit 0") {
		t.Fatalf("target after timeout = %q, err=%v", got, err)
	}
}

func TestHealthyUpdateKeepsBackupUntilFollowingStart(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Telegram-MCP.AppImage")
	backup := target + ".old"
	mustWriteFile(t, target, "candidate-v2", 0o755)
	mustWriteFile(t, backup, "healthy-v1", 0o755)
	candidateIdentity, _ := identityForFile(target, "2.0.0")
	previousIdentity, _ := identityForFile(backup, "1.0.0")
	pending := pendingUpdate{Kind: "appimage", Target: target, Backup: backup, Version: "2.0.0", Marker: filepath.Join(dir, "health"), Deadline: time.Now().Add(time.Minute), CandidateIdentity: candidateIdentity, PreviousIdentity: previousIdentity}
	if err := writePendingUpdate(dir, pending); err != nil {
		t.Fatal(err)
	}
	if err := MarkHealthy(dir, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("health marker removed backup in the same start: %v", err)
	}
	if err := ReconcileStartup(dir, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup was not removed on the following start: %v", err)
	}
}

func TestPreparedUpdateThatNeverActivatedLeavesHealthyCurrentVersion(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Telegram-MCP.AppImage")
	candidate := target + ".new"
	backup := target + ".old"
	mustWriteFile(t, target, "healthy-v1", 0o755)
	mustWriteFile(t, candidate, "candidate-v2", 0o755)
	candidateIdentity, _ := identityForFile(candidate, "2.0.0")
	previousIdentity, _ := identityForFile(target, "1.0.0")
	pending := pendingUpdate{
		Kind: "appimage", Target: target, Backup: backup, Candidate: candidate,
		Version: "2.0.0", Marker: filepath.Join(dir, "health"), Deadline: time.Now().Add(-time.Minute),
		CandidateIdentity: candidateIdentity, PreviousIdentity: previousIdentity,
	}
	if err := writePendingUpdate(dir, pending); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileStartup(dir, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "healthy-v1" {
		t.Fatalf("current version changed during prepared-state recovery: %q, %v", got, err)
	}
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatalf("unactivated candidate remained: %v", err)
	}
}

func TestLegacyPendingCandidateUsesCompatibleHealthMarker(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Telegram-MCP.AppImage")
	backup := target + ".old"
	mustWriteFile(t, target, "candidate-v2", 0o755)
	mustWriteFile(t, backup, "healthy-v1", 0o755)
	pending := pendingUpdate{
		Kind: "appimage", Target: target, Backup: backup, Candidate: target + ".new",
		Executable: target, Version: "2.0.0", Marker: filepath.Join(dir, "health"), Deadline: time.Now().Add(time.Minute),
	}
	if err := writePendingUpdate(dir, pending); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileStartup(dir, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := MarkHealthy(dir, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(pending.Marker); err != nil || strings.TrimSpace(string(got)) != "2.0.0" {
		t.Fatalf("legacy monitor marker = %q, %v", got, err)
	}
	identity, err := readHealthMarker(pending.Marker + ".identity")
	if err != nil || identity.Version != "2.0.0" {
		t.Fatalf("identity health marker = %+v, %v", identity, err)
	}
	if err := ReconcileStartup(dir, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("confirmed legacy transition did not clean backup: %v", err)
	}
}

func TestReviewActivatedMisstampedCandidateDropsRollback(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Telegram-MCP.AppImage")
	backup := target + ".old"
	mustWriteFile(t, target, "candidate-built-with-dev-version", 0o755)
	mustWriteFile(t, backup, "healthy-v1", 0o755)
	candidateIdentity, err := identityForFile(target, "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	previousIdentity, err := identityForFile(backup, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	pending := pendingUpdate{
		Kind: "appimage", Target: target, Backup: backup, Candidate: target + ".new",
		Executable: target, Version: "2.0.0", Marker: filepath.Join(dir, "health"),
		Deadline: time.Now().Add(time.Minute), CandidateIdentity: candidateIdentity,
		PreviousIdentity: previousIdentity,
	}
	if err := writePendingUpdate(dir, pending); err != nil {
		t.Fatal(err)
	}

	if err := ReconcileStartup(dir, "dev"); err == nil || !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("reconcile error = %v; want recoverable ambiguity", err)
	}
	if got, err := os.ReadFile(target); err != nil || string(got) != "candidate-built-with-dev-version" {
		t.Fatalf("target=%q err=%v; candidate unexpectedly changed", got, err)
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("healthy rollback backup was not preserved: %v", err)
	}
	if _, err := os.Stat(pendingUpdatePath(dir)); err != nil {
		t.Fatalf("pending recovery state was not preserved: %v", err)
	}
}

func TestReviewSecondNSISUpdateBeforeNextStartupLosesFirstHealthyInstaller(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows monitor coverage is in rollback_windows_test.go")
	}
	dir := t.TempDir()
	tools := filepath.Join(dir, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	fakePowerShell := filepath.Join(tools, "powershell.exe")
	if err := os.WriteFile(fakePowerShell, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))

	setupV2 := filepath.Join(dir, "setup-v2.exe")
	setupV3 := filepath.Join(dir, "setup-v3.exe")
	mustWriteFile(t, setupV2, "authenticated-installer-v2", 0o700)
	mustWriteFile(t, setupV3, "authenticated-installer-v3", 0o700)
	stateDir := filepath.Join(dir, "updates")
	target := filepath.Join(dir, "telegram-mcp.exe")
	mustWriteFile(t, target, "installed-executable-v1", 0o700)

	if err := installSetup(setupV2, target, stateDir, "1.0.0", "2.0.0", fileSHA256(t, setupV2)); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, target, "installed-executable-v2", 0o700)
	if err := MarkHealthy(stateDir, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	healthyV2, err := readPendingUpdate(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if healthyV2.LegacyProtocol || !validIdentity(healthyV2.CandidateIdentity) {
		t.Fatalf("fresh Windows transaction used legacy health protocol: %+v", healthyV2)
	}
	if err := installSetup(setupV3, target, stateDir, "2.0.0", "3.0.0", fileSHA256(t, setupV3)); err != nil {
		t.Fatal(err)
	}

	pending, err := readPendingUpdate(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Version != "3.0.0" {
		t.Fatalf("pending version = %q; want 3.0.0", pending.Version)
	}
	if pending.Backup == "" {
		t.Fatal("healthy v2 installer was not promoted before staging v3")
	}
	if got, err := os.ReadFile(pending.Backup); err != nil || string(got) != "authenticated-installer-v2" {
		t.Fatalf("rollback installer = %q err=%v; want authenticated v2", got, err)
	}
	if pending.Candidate == pending.Backup {
		t.Fatal("v3 candidate overwrote the retained v2 installer slot")
	}
}

func TestSetupRejectsConflictingPendingTransaction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a Unix fake powershell launcher")
	}
	dir := t.TempDir()
	tools := filepath.Join(dir, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(tools, "powershell.exe"), "#!/bin/sh\nexit 0\n", 0o755)
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	setupV2 := filepath.Join(dir, "setup-v2.exe")
	setupV3 := filepath.Join(dir, "setup-v3.exe")
	mustWriteFile(t, setupV2, "authenticated-installer-v2", 0o700)
	mustWriteFile(t, setupV3, "authenticated-installer-v3", 0o700)
	stateDir := filepath.Join(dir, "updates")
	target := filepath.Join(dir, "telegram-mcp.exe")
	mustWriteFile(t, target, "installed-executable-v1", 0o700)
	if err := installSetup(setupV2, target, stateDir, "1.0.0", "2.0.0", fileSHA256(t, setupV2)); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(pendingUpdatePath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	pending, err := readPendingUpdate(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	candidateBefore, err := os.ReadFile(pending.Candidate)
	if err != nil {
		t.Fatal(err)
	}

	err = installSetup(setupV3, target, stateDir, "1.0.0", "3.0.0", fileSHA256(t, setupV3))
	if !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("second install error = %v; want ErrRecoveryPending", err)
	}
	after, err := os.ReadFile(pendingUpdatePath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("conflicting update rewrote pending transaction")
	}
	candidateAfter, err := os.ReadFile(pending.Candidate)
	if err != nil || string(candidateAfter) != string(candidateBefore) {
		t.Fatalf("conflicting update changed original candidate: %q, %v", candidateAfter, err)
	}
}

func TestMarkHealthyWaitsForTransactionOwnershipLock(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "Telegram-MCP.AppImage")
	mustWriteFile(t, target, "candidate-v2", 0o755)
	candidateIdentity, err := identityForFile(target, "2.0.0")
	if err != nil {
		t.Fatal(err)
	}
	pending := pendingUpdate{
		Kind: "appimage", Target: target, Version: "2.0.0", Marker: filepath.Join(dir, "health"),
		Deadline: time.Now().Add(time.Minute), CandidateIdentity: candidateIdentity, ProtocolVersion: 2,
	}
	if err := writePendingUpdate(dir, pending); err != nil {
		t.Fatal(err)
	}
	lock, err := os.OpenFile(transactionLockPath(dir), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = os.Remove(transactionLockPath(dir))
		close(released)
	}()
	if err := MarkHealthy(dir, "2.0.0"); err != nil {
		t.Fatal(err)
	}
	<-released
	if _, err := readHealthMarker(pending.Marker); err != nil {
		t.Fatalf("health marker was lost while waiting for ownership lock: %v", err)
	}
}

func TestWindowsRollbackStateClearsOnlyForPreviousExecutableIdentity(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "telegram-mcp.exe")
	mustWriteFile(t, target, "installed-v1", 0o700)
	previousIdentity, err := identityForFile(target, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	pending := pendingUpdate{
		Kind: "setup", Target: target, Version: "2.0.0", PreviousIdentity: previousIdentity,
		Marker: filepath.Join(dir, "health"), Deadline: time.Now().Add(time.Minute),
	}
	body, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeAtomicFile(rollbackUpdatePath(dir), body, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReconcileStartup(dir, "2.0.0"); !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("candidate version cleared rollback state: %v", err)
	}
	if _, err := os.Stat(rollbackUpdatePath(dir)); err != nil {
		t.Fatalf("rollback state was not preserved: %v", err)
	}
	if err := ReconcileStartup(dir, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(rollbackUpdatePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("verified restored version did not clear rollback state: %v", err)
	}
}

func TestAuthenticatedHighWaterMarkRejectsOlderSignedRelease(t *testing.T) {
	dir := t.TempDir()
	target := Target{Kind: "appimage", Path: filepath.Join(dir, "Telegram-MCP.AppImage")}
	name := target.assetName()
	var version atomic.Int32
	version.Store(3)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := version.Load()
		tag := fmt.Sprintf("v%d.0.0", v)
		body := []byte("candidate-" + tag)
		manifest := testManifest(strings.TrimPrefix(tag, "v"), name, body)
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			_ = json.NewEncoder(w).Encode(release{Tag: tag, Assets: []asset{
				{Name: name, URL: srv.URL + "/file", Size: int64(len(body))},
				{Name: "checksums.txt", URL: srv.URL + "/sums"},
				{Name: "checksums.txt.minisig", URL: srv.URL + "/sums.minisig"},
			}})
		case "/file":
			_, _ = w.Write(body)
		case "/sums":
			_, _ = w.Write(manifest)
		case "/sums.minisig":
			_, _ = w.Write(signTestManifest(manifest))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	u := &Updater{Repo: "o/r", Version: "1.0.0", API: srv.URL, Client: srv.Client(), PublicKey: testPublicKey(), Dir: filepath.Join(dir, "updates"), Target: target}
	if found, err := u.Check(context.Background()); err != nil || !found {
		t.Fatalf("v3 check: found=%v err=%v", found, err)
	}
	if _, err := u.Download(context.Background()); err != nil {
		t.Fatalf("authenticated v3 download: %v", err)
	}
	version.Store(2)
	if found, err := u.Check(context.Background()); err != nil || !found {
		t.Fatalf("v2 check: found=%v err=%v", found, err)
	}
	if _, err := u.Download(context.Background()); err == nil || !strings.Contains(err.Error(), "authenticated version 3.0.0") {
		t.Fatalf("older signed release error = %v", err)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	digest, err := sha256File(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func mustWriteFile(t *testing.T, path, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}
