//go:build linux || darwin

package httpserver

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// createPrivateSessionDir never repairs existing permissions. Missing ancestors
// are created privately, without changing unrelated existing directories.
func createPrivateSessionDir(path string) error {
	path = filepath.Clean(path)
	if err := os.Mkdir(path, 0700); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			parent := filepath.Dir(path)
			if parent == path {
				return errSessionStorage
			}
			if err := createPrivateSessionDir(parent); err != nil {
				return err
			}
			if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return errSessionStorage
			}
		} else if !errors.Is(err, os.ErrExist) {
			return errSessionStorage
		}
	}
	return checkPrivateSessionPath(path, true)
}

func checkPrivateSessionPath(path string, dir bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.ErrNotExist
	}
	if err != nil {
		return errSessionStorage
	}
	want := os.FileMode(0600)
	if dir {
		want = os.ModeDir | 0700
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode() != want {
		return errSessionStorage
	}
	return nil
}

// protectSessionFile is only for newly created files, never existing storage.
func protectSessionFile(f *os.File) error {
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errSessionStorage
	}
	if err := f.Chmod(0600); err != nil {
		return errSessionStorage
	}
	return nil
}
