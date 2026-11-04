//go:build !windows

package service

import "os"

func protectDataDirectory(path string, info os.FileInfo) (func(), error) {
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return nil, os.ErrPermission
	}
	return func() {}, nil
}

func prepareDataFile(path string) error { return os.Chmod(path, 0600) }

func checkPrivateDataPermissions(_ string, info os.FileInfo, dir bool) error {
	want := os.FileMode(0600)
	if dir {
		want = 0700
	}
	if info.Mode().Perm() != want {
		return os.ErrPermission
	}
	return nil
}
