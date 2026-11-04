//go:build !darwin

package updater

import (
	"errors"
	"os"
)

func restoreSwappedTarget(kind, target, backup string) error {
	if kind == "appimage" {
		// os.Rename replaces a file atomically on the supported Unix AppImage
		// platforms, so the launch path is never absent during rollback.
		return os.Rename(backup, target)
	}
	tmp := target + ".failed"
	_ = os.RemoveAll(tmp)
	if err := os.Rename(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(backup, target); err != nil {
		_ = os.Rename(tmp, target)
		return err
	}
	return os.RemoveAll(tmp)
}

func swapInCandidate(kind, next, target, backup string) error {
	if kind == "appimage" {
		info, err := os.Stat(target)
		if err != nil {
			return err
		}
		if err := copyFile(target, backup, info.Mode().Perm()); err != nil {
			return err
		}
		if err := os.Rename(next, target); err != nil {
			return errors.Join(err, os.Remove(backup))
		}
		return nil
	}

	// This fallback is used only by platform-neutral unit probes. macOS uses
	// renameatx_np(RENAME_SWAP), so the production bundle exchange is atomic.
	tmp := target + ".swap"
	if err := os.Rename(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(next, target); err != nil {
		_ = os.Rename(tmp, target)
		return err
	}
	if err := os.Rename(tmp, backup); err != nil {
		_ = os.Rename(target, next)
		_ = os.Rename(tmp, target)
		return err
	}
	return nil
}
