package updater

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	pendingUpdateName   = "pending-update.json"
	rollbackUpdateName  = "pending-update.rollback.json"
	highWaterName       = "authenticated-version"
	setupRecordName     = "last-known-good-setup.json"
	transactionLockName = "update-transaction.lock"
)

var ErrRecoveryPending = errors.New("an update recovery transaction is still pending")

var updateHealthTimeout = 2 * time.Minute

type artifactIdentity struct {
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

type pendingUpdate struct {
	Kind                       string           `json:"kind"`
	Target                     string           `json:"target,omitempty"`
	Backup                     string           `json:"backup,omitempty"`
	Candidate                  string           `json:"candidate,omitempty"`
	Executable                 string           `json:"executable,omitempty"`
	Version                    string           `json:"version"`
	Marker                     string           `json:"marker"`
	Deadline                   time.Time        `json:"deadline"`
	CandidateIdentity          artifactIdentity `json:"candidate_identity"`
	PreviousIdentity           artifactIdentity `json:"previous_identity"`
	CandidateInstallerIdentity artifactIdentity `json:"candidate_installer_identity,omitempty"`
	BackupInstallerIdentity    artifactIdentity `json:"backup_installer_identity,omitempty"`
	LegacyProtocol             bool             `json:"legacy_protocol,omitempty"`
	ProtocolVersion            int              `json:"protocol_version,omitempty"`
	TransactionID              string           `json:"transaction_id,omitempty"`
}

type retainedSetup struct {
	Path     string           `json:"path"`
	Identity artifactIdentity `json:"identity"`
}

func pendingUpdatePath(dir string) string   { return filepath.Join(dir, pendingUpdateName) }
func rollbackUpdatePath(dir string) string  { return filepath.Join(dir, rollbackUpdateName) }
func transactionLockPath(dir string) string { return filepath.Join(dir, transactionLockName) }

func acquireTransactionLock(stateDir string) (func(), error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	path := transactionLockPath(stateDir)
	deadline := time.Now().Add(5 * time.Second)
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, writeErr := fmt.Fprintf(file, "%d\n", os.Getpid())
			closeErr := file.Close()
			if err := errors.Join(writeErr, closeErr, syncDir(stateDir)); err != nil {
				_ = os.Remove(path)
				return nil, err
			}
			return func() {
				_ = os.Remove(path)
				_ = syncDir(stateDir)
			}, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, recoveryPending("another updater recovery operation holds the transaction lock")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func newHealthMarker(dir string) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return filepath.Join(dir, "update-health-"+hex.EncodeToString(nonce[:])), nil
}

func writePendingUpdate(dir string, pending pendingUpdate) error {
	if dir == "" {
		return errors.New("update state directory is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	body, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return writeAtomicFile(pendingUpdatePath(dir), append(body, '\n'), 0o600)
}

func readPendingUpdate(dir string) (pendingUpdate, error) {
	return readUpdateState(pendingUpdatePath(dir))
}

func readUpdateState(path string) (pendingUpdate, error) {
	var pending pendingUpdate
	body, err := os.ReadFile(path)
	if err != nil {
		return pending, err
	}
	if err := json.Unmarshal(body, &pending); err != nil {
		return pending, fmt.Errorf("invalid pending update state: %w", err)
	}
	if pending.Version == "" || pending.Marker == "" || pending.Deadline.IsZero() {
		return pending, errors.New("invalid pending update state: required field missing")
	}
	return pending, nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func identityForFile(path, version string) (artifactIdentity, error) {
	digest, err := sha256File(path)
	if err != nil {
		return artifactIdentity{}, err
	}
	return artifactIdentity{Version: strings.TrimPrefix(version, "v"), SHA256: digest}, nil
}

func identityForArtifact(kind, path, version string) (artifactIdentity, error) {
	if kind != "dmg" {
		return identityForFile(path, version)
	}
	h := sha256.New()
	err := filepath.Walk(path, func(item string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(path, item)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(relative), info.Mode()); err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(item)
			if err != nil {
				return err
			}
			_, err = io.WriteString(h, target)
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(item)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(h, file)
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
	if err != nil {
		return artifactIdentity{}, err
	}
	return artifactIdentity{Version: strings.TrimPrefix(version, "v"), SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

func validIdentity(identity artifactIdentity) bool {
	return identity.Version != "" && len(identity.SHA256) == sha256.Size*2
}

func artifactMatches(kind, path string, identity artifactIdentity) (bool, error) {
	if !validIdentity(identity) {
		return false, nil
	}
	observed, err := identityForArtifact(kind, path, identity.Version)
	if err != nil {
		return false, err
	}
	return observed.SHA256 == identity.SHA256, nil
}

func recoveryPending(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRecoveryPending, fmt.Sprintf(format, args...))
}

func writeHealthMarker(path string, identity artifactIdentity) error {
	body, err := json.Marshal(identity)
	if err != nil {
		return err
	}
	return writeAtomicFile(path, append(body, '\n'), 0o600)
}

func readHealthMarker(path string) (artifactIdentity, error) {
	var identity artifactIdentity
	body, err := os.ReadFile(path)
	if err != nil {
		return identity, err
	}
	if err := json.Unmarshal(body, &identity); err != nil {
		return identity, err
	}
	if !validIdentity(identity) {
		return identity, errors.New("health marker has an invalid identity")
	}
	return identity, nil
}

func identitiesEqual(left, right artifactIdentity) bool {
	return strings.TrimPrefix(left.Version, "v") == strings.TrimPrefix(right.Version, "v") && left.SHA256 == right.SHA256
}

func readCandidateHealth(pending pendingUpdate) (artifactIdentity, error) {
	if !pending.LegacyProtocol {
		return readHealthMarker(pending.Marker)
	}
	version, err := os.ReadFile(pending.Marker)
	if err != nil {
		return artifactIdentity{}, err
	}
	if strings.TrimSpace(string(version)) != pending.Version {
		return artifactIdentity{}, errors.New("legacy health marker version does not match")
	}
	return readHealthMarker(pending.Marker + ".identity")
}

func removeHealthMarkers(pending pendingUpdate) {
	_ = os.Remove(pending.Marker)
	if pending.LegacyProtocol {
		_ = os.Remove(pending.Marker + ".identity")
	}
}

func writeAtomicFile(path string, body []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// activateReplacement keeps the last known-good file or bundle until a later
// healthy startup has been observed. A helper-launch failure is synchronous, so
// the old version is restored before this function returns.
func activateReplacement(kind, next, target, backup, stateDir, previousVersion, version, candidateExecutable, restoredExecutable, rollbackExecutable, script string) error {
	if next != backup {
		if _, err := os.Stat(backup); err == nil {
			return fmt.Errorf("refusing update while rollback backup %s already exists", backup)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	previousIdentity, err := identityForArtifact(kind, target, previousVersion)
	if err != nil {
		return fmt.Errorf("identify installed version: %w", err)
	}
	candidateIdentity, err := identityForArtifact(kind, next, version)
	if err != nil {
		return fmt.Errorf("identify update candidate: %w", err)
	}
	marker, err := newHealthMarker(stateDir)
	if err != nil {
		return err
	}
	pending := pendingUpdate{
		Kind: kind, Target: target, Backup: backup, Candidate: next, Executable: restoredExecutable, Version: version,
		Marker: marker, Deadline: time.Now().Add(updateHealthTimeout), CandidateIdentity: candidateIdentity,
		PreviousIdentity: previousIdentity, ProtocolVersion: 2, TransactionID: filepath.Base(marker),
	}
	if err := writePendingUpdate(stateDir, pending); err != nil {
		_ = os.Remove(pendingUpdatePath(stateDir))
		if next == backup {
			_ = os.RemoveAll(next)
		}
		return err
	}
	if err := swapInCandidate(kind, next, target, backup); err != nil {
		_ = os.Remove(pendingUpdatePath(stateDir))
		if next == backup {
			_ = os.RemoveAll(next)
		}
		return err
	}
	restore := func(cause error) error {
		rollbackErr := restorePendingUpdate(stateDir, pending)
		if rollbackErr != nil {
			return errors.Join(cause, fmt.Errorf("update rollback failed: %w", rollbackErr))
		}
		return cause
	}
	if err := syncDir(filepath.Dir(target)); err != nil {
		return restore(err)
	}
	env := map[string]string{
		"TARGET": target, "OLD": backup, "MARKER": marker, "VERSION": version,
		"EXECUTABLE": candidateExecutable, "ROLLBACK_EXECUTABLE": rollbackExecutable,
		"STATE": pendingUpdatePath(stateDir), "STATE_DIR": stateDir,
		"DEADLINE_SECONDS": fmt.Sprint(int(updateHealthTimeout.Seconds())),
	}
	if err := relaunch(script, env); err != nil {
		return restore(err)
	}
	return nil
}

func restorePendingUpdate(stateDir string, pending pendingUpdate) error {
	if pending.Backup == "" {
		if pending.Kind == "setup" {
			return errors.New("Windows rollback installer is unavailable; reinstall the previous signed release manually")
		}
		return errors.New("pending update has no rollback target")
	}
	rollbackIdentity := pending.PreviousIdentity
	if pending.Kind == "setup" {
		rollbackIdentity = pending.BackupInstallerIdentity
	}
	if !validIdentity(rollbackIdentity) {
		return recoveryPending("rollback identity is unavailable; recovery material was preserved")
	}
	matches, err := artifactMatches(pending.Kind, pending.Backup, rollbackIdentity)
	if err != nil {
		return recoveryPending("cannot verify rollback artifact: %v", err)
	}
	if !matches {
		return recoveryPending("rollback artifact identity does not match the recorded previous version")
	}
	if pending.Kind == "setup" {
		pendingPath := pendingUpdatePath(stateDir)
		rollbackPath := rollbackUpdatePath(stateDir)
		if _, err := os.Stat(rollbackPath); err == nil {
			return recoveryPending("a previous Windows rollback is still being reconciled")
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.Rename(pendingPath, rollbackPath); err != nil {
			return fmt.Errorf("preserve Windows rollback transaction: %w", err)
		}
		if err := syncDir(stateDir); err != nil {
			_ = os.Rename(rollbackPath, pendingPath)
			return err
		}
		cmd := exec.Command(pending.Backup, "/S", "/UPDATE="+strconv.Itoa(os.Getpid()))
		if err := detach(cmd); err != nil {
			_ = os.Rename(rollbackPath, pendingPath)
			_ = syncDir(stateDir)
			return fmt.Errorf("start Windows rollback installer: %w", err)
		}
		return nil
	}
	if pending.Target == "" || pending.Backup == "" {
		return errors.New("pending update has no rollback target")
	}
	if err := restoreSwappedTarget(pending.Kind, pending.Target, pending.Backup); err != nil {
		return err
	}
	removeHealthMarkers(pending)
	_ = os.Remove(pendingUpdatePath(stateDir))
	return syncDir(filepath.Dir(pending.Target))
}

func launchRestored(pending pendingUpdate) error {
	if pending.Kind == "setup" {
		return nil // The retained NSIS installer relaunches via /UPDATE.
	}
	if pending.Executable == "" {
		return errors.New("pending update has no executable to relaunch")
	}
	if err := detach(exec.Command(pending.Executable)); err != nil {
		return fmt.Errorf("relaunch restored version: %w", err)
	}
	return nil
}

// RollbackAndRelaunch is used by the macOS monitor after it has stopped the
// exact candidate PID. It reads trusted paths from the private pending record,
// atomically swaps the old bundle back, and starts its executable.
func RollbackAndRelaunch(stateDir string) error {
	pending, err := readPendingUpdate(stateDir)
	if err != nil {
		return err
	}
	if err := restorePendingUpdate(stateDir, pending); err != nil {
		return err
	}
	return launchRestored(pending)
}

// MarkHealthy records that the candidate brought up the daemon and panel. It
// deliberately leaves the rollback copy in place; ReconcileStartup removes it
// on the following process start.
func MarkHealthy(stateDir, version string) error {
	release, err := acquireTransactionLock(stateDir)
	if err != nil {
		return err
	}
	defer release()
	return markHealthyLocked(stateDir, version)
}

func markHealthyLocked(stateDir, version string) error {
	pending, err := readPendingUpdate(stateDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimPrefix(version, "v") != strings.TrimPrefix(pending.Version, "v") {
		return nil
	}
	if err := adoptLegacyPending(stateDir, &pending); err != nil {
		return err
	}
	if pending.Kind == "setup" {
		matches, matchErr := artifactMatches("setup", pending.Candidate, pending.CandidateInstallerIdentity)
		if matchErr != nil || !matches {
			return recoveryPending("cannot confirm the Windows candidate installer identity")
		}
		pending.CandidateIdentity, err = identityForFile(pending.Target, pending.Version)
		if err != nil {
			return recoveryPending("cannot identify the active Windows candidate: %v", err)
		}
		if err := writePendingUpdate(stateDir, pending); err != nil {
			return err
		}
	} else {
		matches, matchErr := artifactMatches(pending.Kind, pending.Target, pending.CandidateIdentity)
		if matchErr != nil || !matches {
			return recoveryPending("the running candidate does not match the pending update identity")
		}
	}
	if pending.LegacyProtocol {
		if err := writeHealthMarker(pending.Marker+".identity", pending.CandidateIdentity); err != nil {
			return err
		}
		return writeAtomicFile(pending.Marker, []byte(pending.Version+"\n"), 0o600)
	}
	return writeHealthMarker(pending.Marker, pending.CandidateIdentity)
}

// ReconcileStartup cleans a confirmed update on the start after confirmation,
// or restores an update whose health deadline elapsed without a marker.
func ReconcileStartup(stateDir, currentVersion string) error {
	release, err := acquireTransactionLock(stateDir)
	if err != nil {
		return err
	}
	defer release()
	return reconcileStartupAt(stateDir, currentVersion, time.Now())
}

func adoptLegacyPending(stateDir string, pending *pendingUpdate) error {
	if pending.ProtocolVersion >= 2 || validIdentity(pending.CandidateIdentity) {
		return nil
	}
	candidateIdentity, err := identityForArtifact(pending.Kind, pending.Target, pending.Version)
	if err != nil {
		return recoveryPending("cannot identify the candidate started by the previous updater: %v", err)
	}
	pending.CandidateIdentity = candidateIdentity
	if pending.Kind == "setup" {
		installerIdentity, err := identityForFile(pending.Candidate, pending.Version)
		if err != nil {
			return recoveryPending("cannot identify the Windows installer retained by the previous updater: %v", err)
		}
		pending.CandidateInstallerIdentity = installerIdentity
	}
	pending.LegacyProtocol = true
	return writePendingUpdate(stateDir, *pending)
}

func reconcileRollbackState(stateDir, currentVersion string) error {
	pending, err := readUpdateState(rollbackUpdatePath(stateDir))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return recoveryPending("cannot read the Windows rollback transaction: %v", err)
	}
	if strings.TrimPrefix(currentVersion, "v") != strings.TrimPrefix(pending.PreviousIdentity.Version, "v") {
		return recoveryPending("Windows rollback to %s has not completed", pending.PreviousIdentity.Version)
	}
	matches, matchErr := artifactMatches("setup", pending.Target, pending.PreviousIdentity)
	if matchErr != nil || !matches {
		return recoveryPending("the restored Windows executable identity cannot be verified")
	}
	removeHealthMarkers(pending)
	if err := os.Remove(rollbackUpdatePath(stateDir)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncDir(stateDir)
}

func reconcileStartupAt(stateDir, currentVersion string, now time.Time) error {
	if err := reconcileRollbackState(stateDir, currentVersion); err != nil {
		return err
	}
	pending, err := readPendingUpdate(stateDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimPrefix(currentVersion, "v") != strings.TrimPrefix(pending.Version, "v") {
		matchesPrevious, matchErr := artifactMatches(pending.Kind, pending.Target, pending.PreviousIdentity)
		if matchErr != nil {
			return recoveryPending("cannot prove which installed version is active: %v", matchErr)
		}
		if !matchesPrevious {
			return recoveryPending("the active artifact is neither a confirmed candidate nor the recorded previous version")
		}
		if now.Before(pending.Deadline) {
			return recoveryPending("update %s is still within its activation window", pending.Version)
		}
		// Only a byte-for-byte match with the recorded previous artifact proves
		// that the candidate never activated or rollback already completed.
		if pending.Candidate != "" && pending.Candidate != pending.Target {
			_ = os.RemoveAll(pending.Candidate)
		}
		if pending.Backup != "" && pending.Kind != "setup" && pending.Backup != pending.Candidate {
			_ = os.RemoveAll(pending.Backup)
		}
		removeHealthMarkers(pending)
		if err := os.Remove(pendingUpdatePath(stateDir)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return syncDir(stateDir)
	}
	if err := adoptLegacyPending(stateDir, &pending); err != nil {
		return err
	}
	markerIdentity, markerErr := readCandidateHealth(pending)
	if markerErr == nil && identitiesEqual(markerIdentity, pending.CandidateIdentity) {
		matchesCandidate, matchErr := artifactMatches(pending.Kind, pending.Target, pending.CandidateIdentity)
		if matchErr != nil {
			return recoveryPending("cannot verify the confirmed candidate identity: %v", matchErr)
		}
		if !matchesCandidate {
			return recoveryPending("the health marker does not belong to the active candidate identity")
		}
		if pending.Kind == "setup" {
			if err := promoteHealthySetup(stateDir, pending); err != nil {
				return err
			}
		} else if err := os.RemoveAll(pending.Backup); err != nil {
			return err
		}
		removeHealthMarkers(pending)
		if err := os.Remove(pendingUpdatePath(stateDir)); err != nil && !os.IsNotExist(err) {
			return err
		}
		return syncDir(stateDir)
	}
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return markerErr
	}
	if now.Before(pending.Deadline) {
		return nil
	}
	if err := restorePendingUpdate(stateDir, pending); err != nil {
		return err
	}
	if err := launchRestored(pending); err != nil {
		return err
	}
	return fmt.Errorf("update %s did not become healthy before its deadline and was rolled back", pending.Version)
}

func prepareForInstall(stateDir, currentVersion string) error {
	release, err := acquireTransactionLock(stateDir)
	if err != nil {
		return err
	}
	defer release()
	return prepareForInstallLocked(stateDir, currentVersion)
}

func prepareForInstallLocked(stateDir, currentVersion string) error {
	if err := reconcileRollbackState(stateDir, currentVersion); err != nil {
		return err
	}
	pending, err := readPendingUpdate(stateDir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return recoveryPending("cannot read the existing recovery transaction: %v", err)
	}
	markerIdentity, markerErr := readCandidateHealth(pending)
	if markerErr == nil && identitiesEqual(markerIdentity, pending.CandidateIdentity) &&
		strings.TrimPrefix(currentVersion, "v") == strings.TrimPrefix(pending.Version, "v") {
		return reconcileStartupAt(stateDir, currentVersion, time.Now())
	}
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return recoveryPending("cannot read the existing health marker: %v", markerErr)
	}
	return recoveryPending("update %s must be confirmed or recovered before another update", pending.Version)
}

func promoteHealthySetup(stateDir string, pending pendingUpdate) error {
	if pending.Candidate == "" || !validIdentity(pending.CandidateInstallerIdentity) {
		return errors.New("healthy Windows update has no identified retained installer")
	}
	record := retainedSetup{Path: pending.Candidate, Identity: pending.CandidateInstallerIdentity}
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return writeAtomicFile(filepath.Join(stateDir, setupRecordName), append(body, '\n'), 0o600)
}

func readRetainedSetup(stateDir, currentVersion string) (retainedSetup, error) {
	var record retainedSetup
	body, err := os.ReadFile(filepath.Join(stateDir, setupRecordName))
	if err == nil {
		if err := json.Unmarshal(body, &record); err != nil {
			return record, fmt.Errorf("invalid retained Windows installer record: %w", err)
		}
		if record.Path == "" || !validIdentity(record.Identity) {
			return record, errors.New("invalid retained Windows installer record: required field missing")
		}
		if strings.TrimPrefix(record.Identity.Version, "v") != strings.TrimPrefix(currentVersion, "v") {
			return record, recoveryPending("retained Windows installer is for %s, not active version %s", record.Identity.Version, currentVersion)
		}
		matches, err := artifactMatches("setup", record.Path, record.Identity)
		if err != nil || !matches {
			return record, recoveryPending("retained Windows installer identity cannot be verified")
		}
		return record, nil
	}
	if !os.IsNotExist(err) {
		return record, err
	}
	// The previous updater's fixed filename has no durable version binding.
	// Treat it as unavailable rather than inventing an identity for currentVersion.
	return record, nil
}

func enforceAuthenticatedHighWater(stateDir, version string) error {
	if _, ok := parseVersion(version); !ok {
		return fmt.Errorf("invalid authenticated release version %q", version)
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(stateDir, highWaterName)
	body, err := os.ReadFile(path)
	if err == nil {
		high := strings.TrimSpace(string(body))
		if _, ok := parseVersion(high); !ok {
			return errors.New("authenticated version high-water mark is invalid")
		}
		if newer(high, version) {
			return fmt.Errorf("refusing authenticated release %s after authenticated version %s", version, high)
		}
		if !newer(version, high) {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeAtomicFile(path, []byte(strings.TrimPrefix(version, "v")+"\n"), 0o600)
}
