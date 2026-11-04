package updater

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
)

// installDMG copies the app out of the new .dmg next to the running one,
// checks its signature (and that it is signed by the same developer), and
// swaps the two bundles. The running app keeps its open files; a helper
// opens the new one once it has quit.
func installDMG(ctx context.Context, dmg, bundle, stateDir, previousVersion, version string) error {
	mount, err := os.MkdirTemp("", "wamcp-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(mount)
	if out, err := exec.CommandContext(ctx, "hdiutil", "attach", "-nobrowse", "-readonly", "-noautoopen", "-mountpoint", mount, dmg).CombinedOutput(); err != nil {
		return errors.New(i18n.Tf("não foi possível abrir a imagem da versão nova: %s", strings.TrimSpace(string(out))))
	}
	defer exec.Command("hdiutil", "detach", "-force", mount).Run()

	src := filepath.Join(mount, filepath.Base(bundle))
	if _, err := os.Stat(src); err != nil {
		return errors.New(i18n.Tf("a imagem da versão nova não tem %s", filepath.Base(bundle)))
	}
	currentExecutable, err := bundleExecutable(bundle)
	if err != nil {
		return err
	}
	old := bundle + ".old"
	if _, err := os.Stat(old); err == nil {
		return fmt.Errorf("refusing update while rollback backup %s already exists", old)
	} else if !os.IsNotExist(err) {
		return err
	}
	next := old
	if out, err := exec.CommandContext(ctx, "ditto", src, next).CombinedOutput(); err != nil {
		os.RemoveAll(next)
		return errors.New(i18n.Tf("não foi possível copiar a versão nova: %s", strings.TrimSpace(string(out))))
	}
	if err := sameSigner(bundle, next); err != nil {
		os.RemoveAll(next)
		return err
	}
	candidateExecutable, err := bundleExecutable(next)
	if err != nil {
		os.RemoveAll(next)
		return err
	}
	candidateTargetExecutable := filepath.Join(bundle, "Contents", "MacOS", filepath.Base(candidateExecutable))
	rollbackExecutable := filepath.Join(old, "Contents", "MacOS", filepath.Base(currentExecutable))
	if err := activateReplacement("dmg", next, bundle, old, stateDir, previousVersion, version,
		candidateTargetExecutable, currentExecutable, rollbackExecutable, macRelaunchScript); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("não foi possível trocar o app"), err)
	}
	return nil
}

func bundleExecutable(bundle string) (string, error) {
	plist := filepath.Join(bundle, "Contents", "Info.plist")
	out, err := exec.Command("/usr/libexec/PlistBuddy", "-c", "Print :CFBundleExecutable", plist).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read CFBundleExecutable: %w: %s", err, strings.TrimSpace(string(out)))
	}
	name := strings.TrimSpace(string(out))
	if name == "" || filepath.Base(name) != name {
		return "", fmt.Errorf("invalid CFBundleExecutable %q", name)
	}
	return filepath.Join(bundle, "Contents", "MacOS", name), nil
}

// sameSigner refuses a new bundle whose signature is broken, or made by a
// different developer than the running one.
func sameSigner(current, next string) error {
	if out, err := exec.Command("codesign", "--verify", "--deep", "--strict", next).CombinedOutput(); err != nil {
		return errors.New(i18n.Tf("a assinatura da versão nova não confere: %s", strings.TrimSpace(string(out))))
	}
	have, want := teamID(next), teamID(current)
	if want != "" && have != want {
		return errors.New(i18n.Tf("a versão nova foi assinada por outro desenvolvedor (%q, esperado %q)", have, want))
	}
	return nil
}

func teamID(bundle string) string {
	out, _ := exec.Command("codesign", "-dv", bundle).CombinedOutput()
	for _, line := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "TeamIdentifier="); ok && v != "not set" {
			return v
		}
	}
	return ""
}

// installSetup keeps each authenticated installer in an immutable, versioned
// slot. A confirmed pending installer is promoted before a second transaction
// starts, so a newer update cannot overwrite the last known-good rollback.
func installSetup(setup, target, stateDir, currentVersion, version, digest string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	release, err := acquireTransactionLock(stateDir)
	if err != nil {
		return err
	}
	defer release()
	if err := prepareForInstallLocked(stateDir, currentVersion); err != nil {
		return err
	}
	observedDigest, err := sha256File(setup)
	if err != nil {
		return err
	}
	if observedDigest != digest {
		return errors.New("Windows installer changed after authentication")
	}
	safeVersion := strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(strings.TrimPrefix(version, "v"))
	candidate := filepath.Join(stateDir, "setup-"+safeVersion+"-"+digest+".exe")
	if existingDigest, statErr := sha256File(candidate); statErr == nil {
		if existingDigest != digest {
			return recoveryPending("the immutable Windows installer slot has conflicting contents")
		}
	} else if os.IsNotExist(statErr) {
		if err := copyFile(setup, candidate, 0o700); err != nil {
			return err
		}
	} else {
		return statErr
	}
	candidateInstallerIdentity := artifactIdentity{Version: strings.TrimPrefix(version, "v"), SHA256: digest}
	retained, err := readRetainedSetup(stateDir, currentVersion)
	if err != nil {
		return err
	}
	previousIdentity, err := identityForFile(target, currentVersion)
	if os.IsNotExist(err) {
		previousIdentity = artifactIdentity{}
	} else if err != nil {
		return err
	}
	marker, err := newHealthMarker(stateDir)
	if err != nil {
		return err
	}
	pending := pendingUpdate{
		Kind: "setup", Target: target, Backup: retained.Path, Candidate: candidate,
		Version: version, Marker: marker, Deadline: time.Now().Add(updateHealthTimeout),
		PreviousIdentity: previousIdentity, CandidateInstallerIdentity: candidateInstallerIdentity,
		BackupInstallerIdentity: retained.Identity, ProtocolVersion: 2, TransactionID: filepath.Base(marker),
	}
	helper := filepath.Join(stateDir, "monitor-update.ps1")
	if err := writeAtomicFile(helper, []byte(windowsMonitorScript), 0o600); err != nil {
		return err
	}
	if err := writePendingUpdate(stateDir, pending); err != nil {
		return err
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", helper,
		"-Setup", candidate, "-ParentPID", strconv.Itoa(os.Getpid()), "-Target", target,
		"-Marker", marker, "-Version", version, "-TransactionID", pending.TransactionID,
		"-State", pendingUpdatePath(stateDir), "-RollbackState", rollbackUpdatePath(stateDir), "-Lock", transactionLockPath(stateDir),
		"-Backup", retained.Path, "-BackupSHA256", retained.Identity.SHA256, "-TimeoutSeconds", strconv.Itoa(int(updateHealthTimeout.Seconds())))
	if err := detach(cmd); err != nil {
		_ = os.Remove(pendingUpdatePath(stateDir))
		return err
	}
	return nil
}

const windowsMonitorScript = `param(
  [string]$Setup, [int]$ParentPID, [string]$Target, [string]$Marker,
  [string]$Version, [string]$TransactionID, [string]$State, [string]$RollbackState,
  [string]$Lock, [string]$Backup,
  [string]$BackupSHA256, [int]$TimeoutSeconds
)
$installer = Start-Process -FilePath $Setup -ArgumentList @('/S', "/UPDATE=$ParentPID") -PassThru
$deadline = (Get-Date).AddSeconds($TimeoutSeconds)
while ((Get-Date) -lt $deadline) {
  if (Test-Path -LiteralPath $Marker) {
    try {
      $health = Get-Content -LiteralPath $Marker -Raw | ConvertFrom-Json
      if ($health.version -eq $Version -and $health.sha256 -match '^[0-9a-f]{64}$') { exit 0 }
    } catch {}
  }
  Start-Sleep -Milliseconds 200
}
if (-not $Backup -or -not (Test-Path -LiteralPath $Backup)) { exit 2 }
if (-not $BackupSHA256) { exit 3 }
$observedBackupSHA256 = (Get-FileHash -LiteralPath $Backup -Algorithm SHA256).Hash.ToLowerInvariant()
if ($observedBackupSHA256 -ne $BackupSHA256.ToLowerInvariant()) { exit 3 }
if (-not $TransactionID -or -not $RollbackState -or -not $Lock -or (Test-Path -LiteralPath $RollbackState)) { exit 4 }
$lockStream = $null
try {
  $lockStream = [System.IO.File]::Open($Lock, [System.IO.FileMode]::CreateNew, [System.IO.FileAccess]::Write, [System.IO.FileShare]::None)
  $active = Get-Content -LiteralPath $State -Raw -ErrorAction Stop | ConvertFrom-Json
  if ($active.transaction_id -ne $TransactionID) { throw 'transaction ownership changed' }
  Move-Item -LiteralPath $State -Destination $RollbackState -ErrorAction Stop
  if (-not $installer.HasExited) {
    Stop-Process -Id $installer.Id -Force -ErrorAction SilentlyContinue
    $installer.WaitForExit()
  }
  if ($Target) {
    Get-Process -ErrorAction SilentlyContinue | Where-Object {
      try { $_.Path -eq $Target } catch { $false }
    } | Stop-Process -Force -ErrorAction SilentlyContinue
  }
} catch {
  if ((Test-Path -LiteralPath $RollbackState) -and -not (Test-Path -LiteralPath $State)) {
    Move-Item -LiteralPath $RollbackState -Destination $State -Force -ErrorAction SilentlyContinue
  }
  exit 5
} finally {
  if ($lockStream) { $lockStream.Dispose() }
  Remove-Item -LiteralPath $Lock -Force -ErrorAction SilentlyContinue
}
$rollback = Start-Process -FilePath $Backup -ArgumentList @('/S', '/UPDATE=0') -Wait -PassThru
if ($rollback.ExitCode -ne 0) {
  Move-Item -LiteralPath $RollbackState -Destination $State -Force
  exit $rollback.ExitCode
}
exit 0
`

// installAppImage stages a candidate beside the running AppImage, preserving
// the installed file as a rollback copy until a later healthy startup.
func installAppImage(downloaded, appImage, stateDir, previousVersion, version string) error {
	next := appImage + ".new"
	if err := copyFile(downloaded, next, 0o755); err != nil {
		return err
	}
	if err := activateReplacement("appimage", next, appImage, appImage+".old", stateDir, previousVersion, version,
		appImage, appImage, appImage, appImageRelaunchScript); err != nil {
		_ = os.Remove(next)
		return fmt.Errorf("%s: %w", i18n.T("não foi possível trocar o AppImage"), err)
	}
	return nil
}

const appImageRelaunchScript = `
"$EXECUTABLE" & CHILD=$!
I=0; LIMIT=$((DEADLINE_SECONDS * 5))
while [ "$I" -lt "$LIMIT" ]; do
  [ -f "$MARKER" ] && grep -Fq "\"version\":\"$VERSION\"" "$MARKER" && grep -Eq '"sha256":"[0-9a-f]{64}"' "$MARKER" && exit 0
  kill -0 "$CHILD" 2>/dev/null || break
  sleep 0.2
  I=$((I + 1))
done
kill "$CHILD" 2>/dev/null || true
J=0
while kill -0 "$CHILD" 2>/dev/null && [ "$J" -lt 25 ]; do
  sleep 0.2
  J=$((J + 1))
done
kill -9 "$CHILD" 2>/dev/null || true
wait "$CHILD" 2>/dev/null || true
if mv -f "$OLD" "$TARGET"; then
  rm -f "$MARKER" "$STATE"
  exec "$TARGET"
fi
exit 1
`

const macRelaunchScript = `
"$EXECUTABLE" & CHILD=$!
I=0; LIMIT=$((DEADLINE_SECONDS * 5))
while [ "$I" -lt "$LIMIT" ]; do
  [ -f "$MARKER" ] && grep -Fq "\"version\":\"$VERSION\"" "$MARKER" && grep -Eq '"sha256":"[0-9a-f]{64}"' "$MARKER" && exit 0
  kill -0 "$CHILD" 2>/dev/null || break
  sleep 0.2
  I=$((I + 1))
done
kill "$CHILD" 2>/dev/null || true
J=0
while kill -0 "$CHILD" 2>/dev/null && [ "$J" -lt 25 ]; do
  sleep 0.2
  J=$((J + 1))
done
kill -9 "$CHILD" 2>/dev/null || true
wait "$CHILD" 2>/dev/null || true
"$ROLLBACK_EXECUTABLE" --internal-update-rollback "$STATE_DIR"
`

// relaunch starts a shell that waits for this process to end and then runs
// script, detached so it outlives the app. It is a variable so tests can
// stop short of starting anything.
var relaunch = func(script string, env map[string]string) error {
	wait := `while kill -0 "$PARENT" 2>/dev/null; do sleep 0.2; done; ` + script
	cmd := exec.Command("/bin/sh", "-c", wait)
	cmd.Env = append(os.Environ(), "PARENT="+strconv.Itoa(os.Getpid()))
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	return detach(cmd)
}

func detach(cmd *exec.Cmd) error {
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	process, err := startDetached(cmd)
	if err != nil {
		return err
	}
	return process.Release()
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return errors.Join(err, os.Remove(dst))
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return errors.Join(err, os.Remove(dst))
	}
	return out.Close()
}

func syncDir(path string) error {
	// Windows does not expose directory handles that FlushFileBuffers accepts.
	// The file itself is closed and flushed before its atomic rename.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
