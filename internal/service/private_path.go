package service

import "os"

// CheckPrivateDataPath validates a local data directory or regular file without
// changing its permissions. Unix requires 0700/0600; Windows checks the actual
// DACL (current user, SYSTEM and Administrators), not synthetic POSIX mode bits.
// Symlinks and other non-regular objects are never accepted.
func CheckPrivateDataPath(path string, dir bool) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || (dir && !info.IsDir()) || (!dir && !info.Mode().IsRegular()) {
		return os.ErrPermission
	}
	return checkPrivateDataPermissions(path, info, dir)
}
