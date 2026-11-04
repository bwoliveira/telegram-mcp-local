//go:build windows

package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_DETACH_PARENT") == "1" {
		detachParent()
	}
	if os.Getenv("GO_WANT_WINDOWS_INSTALLER_HELPER") == "1" {
		for _, arg := range os.Args[1:] {
			if strings.EqualFold(arg, "/UPDATE=0") {
				_ = os.WriteFile(os.Getenv("WINDOWS_ROLLBACK_PROOF"), []byte("rollback"), 0o600)
			}
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWindowsMonitorCannotClaimAnotherTransaction(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "monitor-update.ps1")
	if err := os.WriteFile(helper, []byte(windowsMonitorScript), 0o600); err != nil {
		t.Fatal(err)
	}
	installer, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, pendingUpdateName)
	rollbackState := filepath.Join(dir, rollbackUpdateName)
	stateBody := []byte(`{"transaction_id":"new-transaction"}`)
	if err := os.WriteFile(state, stateBody, 0o600); err != nil {
		t.Fatal(err)
	}
	proof := filepath.Join(dir, "rollback-ran")
	t.Setenv("GO_WANT_WINDOWS_INSTALLER_HELPER", "1")
	t.Setenv("WINDOWS_ROLLBACK_PROOF", proof)
	digest, err := sha256File(installer)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", helper,
		"-Setup", installer, "-ParentPID", "12345", "-Target", "", "-Marker", filepath.Join(dir, "health"),
		"-Version", "2.0.0", "-TransactionID", "old-transaction", "-State", state,
		"-RollbackState", rollbackState, "-Lock", filepath.Join(dir, transactionLockName), "-Backup", installer,
		"-BackupSHA256", digest, "-TimeoutSeconds", "0")
	if output, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("stale monitor unexpectedly claimed new transaction: %s", output)
	} else if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 5 {
		t.Fatalf("stale monitor exit = %v, output=%s; want 5", err, output)
	}
	if got, err := os.ReadFile(state); err != nil || string(got) != string(stateBody) {
		t.Fatalf("new transaction was not restored: %q, %v", got, err)
	}
	if _, err := os.Stat(rollbackState); !os.IsNotExist(err) {
		t.Fatalf("stale monitor left rollback claim: %v", err)
	}
	if _, err := os.Stat(proof); !os.IsNotExist(err) {
		t.Fatalf("stale monitor launched old rollback installer: %v", err)
	}
}

func TestWindowsMonitorExecutesProductionRollbackPath(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "monitor-update.ps1")
	if err := os.WriteFile(helper, []byte(windowsMonitorScript), 0o600); err != nil {
		t.Fatal(err)
	}
	installer, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, pendingUpdateName)
	marker := filepath.Join(dir, "health")
	proof := filepath.Join(dir, "rollback-ran")
	transactionID := "test-transaction"
	stateBody := []byte("{\"transaction_id\":\"" + transactionID + "\"}")
	if err := os.WriteFile(state, stateBody, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GO_WANT_WINDOWS_INSTALLER_HELPER", "1")
	t.Setenv("WINDOWS_ROLLBACK_PROOF", proof)

	digest, err := sha256File(installer)
	if err != nil {
		t.Fatal(err)
	}
	rollbackState := filepath.Join(dir, rollbackUpdateName)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", helper,
		"-Setup", installer, "-ParentPID", "12345", "-Target", "", "-Marker", marker,
		"-Version", "2.0.0", "-TransactionID", transactionID, "-State", state,
		"-RollbackState", rollbackState, "-Lock", filepath.Join(dir, transactionLockName), "-Backup", installer,
		"-BackupSHA256", digest, "-TimeoutSeconds", "0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("production monitor failed: %v: %s", err, output)
	}
	if got, err := os.ReadFile(proof); err != nil || string(got) != "rollback" {
		t.Fatalf("rollback installer was not executed: %q, %v", got, err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("successful rollback left active pending state: %v", err)
	}
	if got, err := os.ReadFile(rollbackState); err != nil || string(got) != string(stateBody) {
		t.Fatalf("successful rollback did not preserve reconciliation state: %q, %v", got, err)
	}
}
