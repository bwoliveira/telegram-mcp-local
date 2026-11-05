package main

import (
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The 1.0.0 release is a beta: every page carries the short badge in the
// header, and the home and download pages carry the banner with the issues
// link, in both build modes.
func TestBetaNotice(t *testing.T) {
	reBanner := regexp.MustCompile(`(?s)<div class="beta-banner" role="note" aria-labelledby="beta-lead" data-beta>(.*?)</div>\s*</div>`)
	both(t, func(t *testing.T, b built, mode string) {
		for file, p := range b.pages {
			s := b.strs[langOf(b.site, file)]
			badge := `<span class="badge badge-beta" data-beta>` + s["beta.badge"] + `</span></a>`
			if strings.Count(p, badge) != 1 {
				t.Errorf("%s: want the beta badge in the header lockup once", file)
			}
			isBannerPage := false
			for _, l := range b.site.Languages {
				if file == outFile(l, "index") || file == outFile(l, "download") {
					isBannerPage = true
				}
			}
			m := reBanner.FindAllStringSubmatch(p, -1)
			if !isBannerPage {
				if len(m) != 0 || strings.Contains(p, "beta-banner") {
					t.Errorf("%s: unexpected beta banner", file)
				}
				continue
			}
			if len(m) != 1 {
				t.Errorf("%s: want one beta banner, got %d", file, len(m))
				continue
			}
			text := reviewContentText(m[0][1])
			for _, want := range []string{s["beta.lead"], s["beta.body"], s["beta.link"], "1.0.0"} {
				if !strings.Contains(text, want) {
					t.Errorf("%s: banner lacks %q", file, want)
				}
			}
			if !strings.Contains(m[0][1], `href="https://github.com/`+b.site.Repo+`/issues"`) {
				t.Errorf("%s: banner must link to the issue tracker", file)
			}
			// The banner is the first thing in <main>.
			if i := strings.Index(p, `<main id="main" tabindex="-1">`); i < 0 || !strings.HasPrefix(strings.TrimSpace(p[i+len(`<main id="main" tabindex="-1">`):]), `<div class="beta-banner"`) {
				t.Errorf("%s: banner is not at the top of <main>", file)
			}
		}
		if mode == ModeRelease {
			for _, l := range b.site.Languages {
				if !strings.Contains(b.pages[outFile(l, "download")], `class="release-line"`) || strings.Count(b.pages[outFile(l, "download")], `badge badge-beta`) != 2 {
					t.Errorf("%s: release line must carry the beta badge", l.Code)
				}
			}
		}
	})
}

// The manual verification block must quote the production minisign key that
// docs/security/release-signing.md and the README publish.
func TestManualVerificationKey(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "security", "release-signing.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	const keyID, key = "CF15D44BF3EA0B47", "RWRHC+rzS9QVzybIt8aMwMD9cNnK4ju6/vyLeATd+6FBuoDzRG6pk0yB"
	for name, data := range map[string][]byte{"release-signing.md": doc, "README.md": readme} {
		if !strings.Contains(string(data), keyID) || !strings.Contains(string(data), key) {
			t.Fatalf("%s no longer publishes key %s; update the site with it", name, keyID)
		}
	}
	both(t, func(t *testing.T, b built, _ string) {
		for _, l := range b.site.Languages {
			dl := html.UnescapeString(b.pages[outFile(l, "download")])
			for _, want := range []string{keyID, "minisign -V -P " + key + " -m checksums.txt -x checksums.txt.minisig", "sha256sum --ignore-missing -c checksums.txt", "shasum -a 256 --ignore-missing -c checksums.txt"} {
				if !strings.Contains(dl, want) {
					t.Errorf("%s: verification block lacks %q", l.Code, want)
				}
			}
		}
	})
}
