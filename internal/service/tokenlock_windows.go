//go:build windows

package service

import (
	"os"

	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
	"golang.org/x/sys/windows"
)

func lockToken(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, secrets.ErrStorage
	}
	var overlap windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &overlap); err != nil {
		f.Close()
		return nil, secrets.ErrStorage
	}
	return func() { _ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlap); _ = f.Close() }, nil
}
