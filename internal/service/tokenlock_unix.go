//go:build linux || darwin

package service

import (
	"os"

	"github.com/bwoliveira/telegram-mcp-local/internal/secrets"
	"golang.org/x/sys/unix"
)

// Hold the read/create/write transaction across processes, not just goroutines.
func lockToken(path string) (func(), error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, secrets.ErrStorage
	}
	f := os.NewFile(uintptr(fd), "http-token-lock")
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		f.Close()
		return nil, secrets.ErrStorage
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0077 != 0 || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		f.Close()
		return nil, secrets.ErrPermission
	}
	if err := unix.Flock(fd, unix.LOCK_EX); err != nil {
		f.Close()
		return nil, secrets.ErrStorage
	}
	return func() { _ = unix.Flock(fd, unix.LOCK_UN); _ = f.Close() }, nil
}
