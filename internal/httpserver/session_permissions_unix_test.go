//go:build linux || darwin

package httpserver

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSessionPermissionsPrivateDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "new", "sessions")
	if err := createPrivateSessionDir(path); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Dir(path), path} {
		if err := checkPrivateSessionPath(dir, true); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("new directory is not private: %v", err)
		}
	}
	if err := os.Chmod(path, 0755); err != nil {
		t.Fatal(err)
	}
	if err := createPrivateSessionDir(path); err == nil {
		t.Fatal("accepted existing public directory")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0755 {
		t.Fatal("modified existing directory")
	}
}

func TestSessionPermissionsRejectUnsafePaths(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "sessions")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateSessionPath(file, false); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateSessionPath(file, true); err == nil {
		t.Fatal("accepted file as directory")
	}
	if err := checkPrivateSessionPath(root, false); err == nil {
		t.Fatal("accepted directory as file")
	}
	if err := os.Chmod(file, 0644); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateSessionPath(file, false); err == nil {
		t.Fatal("accepted public file")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateSessionPath(link, false); err == nil {
		t.Fatal("accepted symlink")
	}
	if err := createPrivateSessionDir(link); err == nil {
		t.Fatal("accepted symlink directory")
	}
	if err := checkPrivateSessionPath(filepath.Join(root, "missing"), false); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing error = %v", err)
	}
}

func TestSessionPermissionsProtectNewFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "new-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Chmod(0644); err != nil {
		t.Fatal(err)
	}
	if err := protectSessionFile(f); err != nil {
		t.Fatal(err)
	}
	if err := checkPrivateSessionPath(f.Name(), false); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := protectSessionFile(f); err == nil {
		t.Fatal("accepted closed file")
	}
}
