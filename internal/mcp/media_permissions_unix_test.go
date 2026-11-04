//go:build !windows

package mcp

import (
	"os"
	"testing"
)

func assertMediaPrivate(t *testing.T, path string, dir bool) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	want := os.FileMode(0600)
	if dir {
		want = 0700
	}
	if info.Mode().Perm() != want {
		t.Fatalf("media mode=%#o, want %#o", info.Mode().Perm(), want)
	}
}
