//go:build darwin

package updater

import (
	"os"

	"golang.org/x/sys/unix"
)

func restoreSwappedTarget(kind, target, backup string) error {
	if kind == "dmg" {
		if err := unix.RenameatxNp(unix.AT_FDCWD, target, unix.AT_FDCWD, backup, unix.RENAME_SWAP); err != nil {
			return err
		}
		return os.RemoveAll(backup)
	}
	return os.Rename(backup, target)
}

func swapInCandidate(kind, next, target, backup string) error {
	if kind == "dmg" && next == backup {
		return unix.RenameatxNp(unix.AT_FDCWD, target, unix.AT_FDCWD, backup, unix.RENAME_SWAP)
	}
	if kind == "appimage" {
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		if err := copyFile(target, backup, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return os.Rename(next, target)
}
