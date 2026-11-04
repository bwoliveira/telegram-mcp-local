package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckPrivateDataPathTypesAndLinks(t *testing.T) {
	dir := t.TempDir()
	// TempDir follows the process umask on Unix; make this positive fixture
	// private explicitly rather than assuming its initial mode is 0700.
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "private.ndjson")
	if err := os.WriteFile(path, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path  string
		dir   bool
		valid bool
	}{
		{dir, true, true}, {path, false, true}, {path, true, false}, {dir, false, false}, {path + "-missing", false, false},
	} {
		if err := CheckPrivateDataPath(tc.path, tc.dir); (err == nil) != tc.valid {
			t.Errorf("CheckPrivateDataPath(%q, %v): %v, valid=%v", tc.path, tc.dir, err, tc.valid)
		}
	}
	link := filepath.Join(dir, "link.ndjson")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := CheckPrivateDataPath(link, false); err == nil {
		t.Fatal("accepted symlink")
	}
}
