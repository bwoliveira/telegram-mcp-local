//go:build windows

package updater

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// createNoWindow gives a console program a console of its own that is never
// shown.
const createNoWindow = 0x08000000

// detachFlags are the creation flags of a helper that must outlive the app.
//
// Windows PowerShell 5.1 started with DETACHED_PROCESS exits with status 0
// almost at once without running its -File script (observed natively on
// Windows 11), so the update monitor never ran. CREATE_NO_WINDOW instead
// gives it a hidden console. CREATE_BREAKAWAY_FROM_JOB takes the helper out
// of a job the app was started in: hosts such as an OpenSSH session put
// their processes in a kill-on-close job, which would otherwise end the
// monitor the moment its host exits.
func detachFlags(breakaway bool) uint32 {
	flags := uint32(createNoWindow | windows.CREATE_NEW_PROCESS_GROUP)
	if breakaway {
		flags |= windows.CREATE_BREAKAWAY_FROM_JOB
	}
	return flags
}

// startDetached starts cmd outside the app's job when the job allows it. A
// job that forbids breakaway makes CreateProcess fail with
// ERROR_ACCESS_DENIED; the helper is then started inside that job, which is
// the most any process in it can do.
func startDetached(cmd *exec.Cmd) (*os.Process, error) {
	setDetachFlags(cmd, true)
	err := cmd.Start()
	if err == nil {
		return cmd.Process, nil
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, err
	}
	retry := exec.Command(cmd.Path, cmd.Args[1:]...)
	retry.Env, retry.Dir = cmd.Env, cmd.Dir
	setDetachFlags(retry, false)
	if err := retry.Start(); err != nil {
		return nil, err
	}
	return retry.Process, nil
}

func setDetachFlags(cmd *exec.Cmd, breakaway bool) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags = detachFlags(breakaway)
}
