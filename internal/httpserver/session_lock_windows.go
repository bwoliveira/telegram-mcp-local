//go:build windows

package httpserver

import (
	"os"

	"golang.org/x/sys/windows"
)

// Lock the stable lock file, not the snapshot replaced by atomic rename. These
// handles are synchronous; omitting FAIL_IMMEDIATELY makes LockFileEx block.
func lockSessionFile(f *os.File) error {
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, ^uint32(0), ^uint32(0), &overlapped); err != nil {
		return errSessionStorage
	}
	return nil
}

func unlockSessionFile(f *os.File) error {
	var overlapped windows.Overlapped
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, ^uint32(0), ^uint32(0), &overlapped); err != nil {
		return errSessionStorage
	}
	return nil
}
