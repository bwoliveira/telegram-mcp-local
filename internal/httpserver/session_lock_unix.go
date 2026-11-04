//go:build linux || darwin

package httpserver

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Lock the stable lock file, not the snapshot replaced by atomic rename.
func lockSessionFile(f *os.File) error {
	return sessionFlock(f, unix.LOCK_EX)
}

func unlockSessionFile(f *os.File) error {
	return sessionFlock(f, unix.LOCK_UN)
}

func sessionFlock(f *os.File, operation int) error {
	for {
		err := unix.Flock(int(f.Fd()), operation)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return errSessionStorage
		}
		return nil
	}
}
