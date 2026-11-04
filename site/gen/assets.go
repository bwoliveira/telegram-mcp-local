package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// cssURL matches url(/assets/...) references inside CSS, quoted or not.
var cssURL = regexp.MustCompile(`url\(\s*(['"]?)(/assets/[^'")\s]+)(['"]?)\s*\)`)

// fingerprint inserts the first 8 hex digits of the content's SHA-256 before
// the extension: css/site.css -> css/site.1a2b3c4d.css.
func fingerprint(rel string, data []byte) string {
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])[:8]
	ext := path.Ext(rel)
	return strings.TrimSuffix(rel, ext) + "." + h + ext
}

// copyStatic copies static/ into the output. Files under static/assets/ are
// fingerprinted and their url() references in CSS rewritten; other files keep
// their names. Markdown notes (SOURCE.md) are documentation and not shipped.
// It returns logical asset names (relative to assets/) mapped to their
// site-absolute fingerprinted paths.
func copyStatic(dir string, w *writer) (map[string]string, error) {
	assets := map[string]string{}
	var css []string
	type file struct{ rel string }
	var rest []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, ".md") {
			return nil
		}
		switch {
		case strings.HasPrefix(rel, "assets/") && strings.HasSuffix(rel, ".css"):
			css = append(css, rel)
		default:
			rest = append(rest, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(rest)
	sort.Strings(css)

	for _, rel := range rest {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(rel, "assets/") {
			if err := w.write(rel, data); err != nil {
				return nil, err
			}
			continue
		}
		fp := fingerprint(rel, data)
		assets[strings.TrimPrefix(rel, "assets/")] = "/" + fp
		if err := w.write(fp, data); err != nil {
			return nil, err
		}
	}
	for _, rel := range css {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		var missing []string
		out := cssURL.ReplaceAllStringFunc(string(data), func(m string) string {
			sub := cssURL.FindStringSubmatch(m)
			target, ok := assets[strings.TrimPrefix(sub[2], "/assets/")]
			if !ok {
				missing = append(missing, sub[2])
				return m
			}
			return `url("` + target + `")`
		})
		if len(missing) > 0 {
			return nil, fmt.Errorf("%s references unknown assets: %s", rel, strings.Join(missing, ", "))
		}
		fp := fingerprint(rel, []byte(out))
		assets[strings.TrimPrefix(rel, "assets/")] = "/" + fp
		if err := w.write(fp, []byte(out)); err != nil {
			return nil, err
		}
	}
	return assets, nil
}
