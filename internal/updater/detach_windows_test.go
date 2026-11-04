//go:build windows

package updater

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// detachProbeScript stands in for monitor-update.ps1: it proves PowerShell
// really ran the -File script after its parent had exited.
const detachProbeScript = `param([string]$Out)
Start-Sleep -Seconds 2
Set-Content -LiteralPath $Out -Value 'monitor ran' -NoNewline
`

// detachParent plays the app: it waits until the test has put it in a job,
// starts the probe through the production detach, and exits.
func detachParent() {
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-File", os.Getenv("DETACH_PROBE_SCRIPT"), "-Out", os.Getenv("DETACH_PROBE_OUT"))
	if err := detach(cmd); err != nil {
		fmt.Println("detach failed:", err)
		os.Exit(3)
	}
	fmt.Println("detached")
	os.Exit(0)
}

func newTestJob(t *testing.T, flags uint32) windows.Handle {
	t.Helper()
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{LimitFlags: flags},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		t.Fatal(err)
	}
	return job
}

// runDetachParent starts the stand-in app inside job, lets it detach the
// probe and waits for it to exit.
func runDetachParent(t *testing.T, job windows.Handle, out string) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "probe.ps1")
	if err := os.WriteFile(script, []byte(detachProbeScript), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent := exec.Command(self, "-test.run=^$")
	parent.Env = append(os.Environ(), "GO_WANT_DETACH_PARENT=1", "DETACH_PROBE_SCRIPT="+script, "DETACH_PROBE_OUT="+out)
	stdin, err := parent.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var output strings.Builder
	parent.Stdout, parent.Stderr = &output, &output
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(parent.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	err = windows.AssignProcessToJobObject(job, h)
	windows.CloseHandle(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = stdin.Write([]byte("go\n"))
	_ = stdin.Close()
	if err := parent.Wait(); err != nil || !strings.Contains(output.String(), "detached") {
		t.Fatalf("stand-in app: %v: %s", err, output.String())
	}
}

func waitForProbe(t *testing.T, out string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if got, err := os.ReadFile(out); err == nil && string(got) == "monitor ran" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the detached PowerShell monitor did not run its script")
}

// The Windows update monitor must run its script after the app exits, even
// when the app lives in a kill-on-close job that is closed with it (an
// OpenSSH session, a CI runner). With DETACHED_PROCESS, PowerShell 5.1 exits
// at once with status 0 without running the script.
func TestDetachedMonitorOutlivesTheAppAndItsJob(t *testing.T) {
	job := newTestJob(t, windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE|windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK)
	out := filepath.Join(t.TempDir(), "proof")
	runDetachParent(t, job, out)
	// Closing the job kills every process still in it, as Windows does
	// when the app's host goes away.
	windows.CloseHandle(job)
	waitForProbe(t, out)
}

// A job that forbids breakaway still gets a running monitor: inside the job,
// for as long as the job lives.
func TestDetachedMonitorStartsInAJobWithoutBreakaway(t *testing.T) {
	job := newTestJob(t, windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE)
	defer windows.CloseHandle(job)
	out := filepath.Join(t.TempDir(), "proof")
	runDetachParent(t, job, out)
	waitForProbe(t, out)
}

func TestDetachFlagsNeverUseDetachedProcess(t *testing.T) {
	for _, breakaway := range []bool{true, false} {
		flags := detachFlags(breakaway)
		if flags&windows.DETACHED_PROCESS != 0 {
			t.Fatalf("breakaway=%v: DETACHED_PROCESS stops PowerShell from running the monitor", breakaway)
		}
		if flags&createNoWindow == 0 || flags&windows.CREATE_NEW_PROCESS_GROUP == 0 {
			t.Fatalf("breakaway=%v: flags %#x", breakaway, flags)
		}
		if (flags&windows.CREATE_BREAKAWAY_FROM_JOB != 0) != breakaway {
			t.Fatalf("breakaway=%v: flags %#x", breakaway, flags)
		}
	}
}
