//go:build !windows

package testutil

import (
	"os"
	"testing"
)

// LoosenPermissions makes only a test fixture public and returns its restoration function.
func LoosenPermissions(t testing.TB, path string) func() {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := os.FileMode(0644)
	if info.IsDir() {
		mode = 0777
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := os.Chmod(path, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
	}
}
