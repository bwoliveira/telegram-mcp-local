package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeaderSelectorsUnique(t *testing.T) {
	for _, mode := range []string{ModePrelaunch, ModeRelease} {
		seen := map[string]bool{}
		for _, line := range strings.Split(Headers(mode), "\n") {
			if line == "" || strings.HasPrefix(line, "#") || line[0] == ' ' || line[0] == '\t' {
				continue
			}
			if seen[line] {
				t.Errorf("%s: duplicate selector %s", mode, line)
			}
			seen[line] = true
		}
	}
}

func TestLocalServerRejectsDuplicateSelectors(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "_headers"), []byte("/*\n  X-Frame-Options: DENY\n/*\n  X-Robots-Tag: noindex\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStaticHandler(dir); err == nil {
		t.Fatal("duplicate selectors accepted; local QA must not merge blocks discarded by Pages")
	}
}
