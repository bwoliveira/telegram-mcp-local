package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const srcDir = "../src"

// built is one generated site, read back into memory for assertions.
type built struct {
	dir   string
	site  Site
	strs  map[string]map[string]string
	files map[string][]byte // slash path relative to dir
	pages map[string]string // HTML output path -> content
}

func build(t *testing.T, opts Options) built {
	t.Helper()
	opts.Src = srcDir
	opts.Out = t.TempDir()
	if _, err := Build(opts); err != nil {
		t.Fatalf("Build(%s): %v", opts.Mode, err)
	}
	b := built{dir: opts.Out, files: map[string][]byte{}, pages: map[string]string{}}
	if err := readJSON(filepath.Join(srcDir, "site.json"), &b.site); err != nil {
		t.Fatal(err)
	}
	var err error
	if b.strs, err = loadStrings(srcDir, b.site); err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(opts.Out, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(opts.Out, p)
		rel = filepath.ToSlash(rel)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		b.files[rel] = data
		if strings.HasSuffix(rel, ".html") {
			b.pages[rel] = string(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func prelaunch(t *testing.T) built { return build(t, Options{Mode: ModePrelaunch}) }

func release(t *testing.T) built {
	return build(t, Options{Mode: ModeRelease, Version: "v1.2.3", Date: "2026-12-01", allowTODO: true})
}

func both(t *testing.T, f func(t *testing.T, b built, mode string)) {
	t.Run(ModePrelaunch, func(t *testing.T) { f(t, prelaunch(t), ModePrelaunch) })
	t.Run(ModeRelease, func(t *testing.T) { f(t, release(t), ModeRelease) })
}

var (
	reTitle     = regexp.MustCompile(`<title>([^<]*)</title>`)
	reMeta      = regexp.MustCompile(`<meta (?:property|name)="([^"]+)" content="([^"]*)">`)
	reCanonical = regexp.MustCompile(`<link rel="canonical" href="([^"]+)">`)
	reAlternate = regexp.MustCompile(`<link rel="alternate" hreflang="([^"]+)" href="([^"]+)">`)
	reAttrURL   = regexp.MustCompile(`\s(?:href|src|poster|srcset|action|formaction|data)="([^"]*)"`)
	reTag       = regexp.MustCompile(`<([a-zA-Z][a-zA-Z0-9-]*)([^>]*)>`)
	reOnAttr    = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	reH1        = regexp.MustCompile(`<h1[\s>]`)
	reCredits   = regexp.MustCompile(`(?s)<div class="credits" data-credits>.*?</div>`)
)

func meta(page, name string) string {
	for _, m := range reMeta.FindAllStringSubmatch(page, -1) {
		if m[1] == name {
			return html.UnescapeString(m[2])
		}
	}
	return ""
}

func TestEveryPageInEveryLanguage(t *testing.T) {
	both(t, func(t *testing.T, b built, _ string) {
		want := 0
		for _, l := range b.site.Languages {
			for _, page := range append(append([]string{}, b.site.Pages...), "404") {
				want++
				if _, ok := b.pages[outFile(l, page)]; !ok {
					t.Errorf("missing %s", outFile(l, page))
				}
			}
		}
		if len(b.pages) != want || want != 12 {
			t.Errorf("got %d HTML files, want %d (=12)", len(b.pages), want)
		}
		for _, top := range []string{"404.html", "en/404.html", "es/404.html"} {
			if _, ok := b.pages[top]; !ok {
				t.Errorf("missing %s: Pages would fall back to SPA mode", top)
			}
		}
	})
}

func TestStringKeysIdentical(t *testing.T) {
	var site Site
	if err := readJSON(filepath.Join(srcDir, "site.json"), &site); err != nil {
		t.Fatal(err)
	}
	if _, err := loadStrings(srcDir, site); err != nil {
		t.Fatal(err)
	}
	// A copy with one key removed from es must fail.
	dir := t.TempDir()
	for _, l := range site.Languages {
		m := map[string]string{}
		if err := readJSON(filepath.Join(srcDir, "strings", l.Code+".json"), &m); err != nil {
			t.Fatal(err)
		}
		if l.Code == "es" {
			delete(m, "nav.faq")
		}
		data, _ := json.Marshal(m)
		os.MkdirAll(filepath.Join(dir, "strings"), 0o755)
		if err := os.WriteFile(filepath.Join(dir, "strings", l.Code+".json"), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loadStrings(dir, site); err == nil {
		t.Fatal("loadStrings accepted a language with a missing key")
	}
}

func TestCanonicalAndHreflang(t *testing.T) {
	both(t, func(t *testing.T, b built, _ string) {
		for _, l := range b.site.Languages {
			for _, page := range b.site.Pages {
				file := outFile(l, page)
				p := b.pages[file]
				self := b.site.BaseURL + pagePath(l, page)
				c := reCanonical.FindAllStringSubmatch(p, -1)
				if len(c) != 1 || c[0][1] != self {
					t.Errorf("%s: canonical %v, want %s", file, c, self)
				}
				if got := meta(p, "og:url"); got != self {
					t.Errorf("%s: og:url %q, want %q", file, got, self)
				}
				alts := map[string]string{}
				for _, m := range reAlternate.FindAllStringSubmatch(p, -1) {
					alts[m[1]] = m[2]
				}
				if len(alts) != len(b.site.Languages)+1 {
					t.Errorf("%s: hreflang set %v", file, alts)
				}
				for _, ol := range b.site.Languages {
					want := b.site.BaseURL + pagePath(ol, page)
					if alts[ol.Code] != want {
						t.Errorf("%s: hreflang %s = %q, want %q", file, ol.Code, alts[ol.Code], want)
					}
					// Reciprocity: the other page points back here.
					back := map[string]string{}
					for _, m := range reAlternate.FindAllStringSubmatch(b.pages[outFile(ol, page)], -1) {
						back[m[1]] = m[2]
					}
					if back[l.Code] != self {
						t.Errorf("%s does not point back to %s", outFile(ol, page), self)
					}
				}
				if alts["x-default"] != b.site.BaseURL+pagePath(Lang{Prefix: "en/"}, page) {
					t.Errorf("%s: x-default %q", file, alts["x-default"])
				}
				if !strings.Contains(p, `<html lang="`+l.Code+`">`) {
					t.Errorf("%s: wrong <html lang>", file)
				}
				if got := meta(p, "og:locale"); got != l.OGLocale {
					t.Errorf("%s: og:locale %q", file, got)
				}
				if n := strings.Count(p, `property="og:locale:alternate"`); n != 2 {
					t.Errorf("%s: %d og:locale:alternate", file, n)
				}
				if !strings.HasPrefix(meta(p, "og:image"), b.site.BaseURL+"/assets/og/og-"+l.Code+".") {
					t.Errorf("%s: og:image %q", file, meta(p, "og:image"))
				}
				for _, name := range []string{"description", "og:type", "og:title", "og:description", "og:site_name", "twitter:card", "theme-color"} {
					if meta(p, name) == "" {
						t.Errorf("%s: missing meta %s", file, name)
					}
				}
				if meta(p, "twitter:card") != "summary_large_image" {
					t.Errorf("%s: twitter:card", file)
				}
			}
		}
	})
}

// TestBrandQualifiedPerLanguage enforces the brand rule (site/DESIGN.md §0):
// the name always carries the localized "unofficial" qualifier, before
// "Telegram" in English and after the name in pt-BR and es.
func TestBrandQualifiedPerLanguage(t *testing.T) {
	order := map[string]string{"pt-BR": "name-first", "en": "qualifier-first", "es": "name-first"}
	wantTitle := map[string]string{"pt-BR": "Telegram MCP (não oficial)", "en": "Unofficial Telegram MCP", "es": "Telegram MCP (no oficial)"}
	wantName := map[string]string{"pt-BR": "Telegram MCP não oficial", "en": "Unofficial Telegram MCP", "es": "Telegram MCP no oficial"}
	both(t, func(t *testing.T, b built, _ string) {
		for _, l := range b.site.Languages {
			s := b.strs[l.Code]
			if s["brand.order"] != order[l.Code] || s["brand.title"] != wantTitle[l.Code] || s["brand.name"] != wantName[l.Code] {
				t.Errorf("%s: brand strings %q/%q/%q", l.Code, s["brand.order"], s["brand.title"], s["brand.name"])
			}
		}
		for file, p := range b.pages {
			lang := langOf(b.site, file)
			s := b.strs[lang]
			q := s["brand.qualifier"]
			titles := []string{html.UnescapeString(reTitle.FindStringSubmatch(p)[1])}
			if !strings.HasSuffix(file, "404.html") {
				titles = append(titles, meta(p, "og:title"), meta(p, "og:site_name"), meta(p, "twitter:title"))
			}
			for _, title := range titles {
				if !strings.HasPrefix(title, s["brand.title"]) || strings.Count(title, b.site.Brand) != 1 {
					t.Errorf("%s: title %q must start with %q", file, title, s["brand.title"])
				}
			}
			// Lockups in header and footer, in the language's order.
			lock := `<span class="lockup-qualifier">` + q + `</span> <span class="lockup-name">` + b.site.Brand + `</span>`
			if s["brand.order"] == "name-first" {
				lock = `<span class="lockup-name">` + b.site.Brand + `</span> <span class="lockup-qualifier">` + q + `</span>`
			}
			if strings.Count(p, lock) != 2 {
				t.Errorf("%s: want 2 lockups %q, got %d", file, lock, strings.Count(p, lock))
			}
			// The old pt-BR/es order ("Não oficial Telegram MCP") must not
			// come back anywhere in the visible text.
			if lang != "en" {
				text := strings.ToLower(reviewContentText(p))
				if strings.Contains(text, strings.ToLower(q)+" "+strings.ToLower(b.site.Brand)) {
					t.Errorf("%s: qualifier placed before the brand in %s", file, lang)
				}
			}
		}
	})
}

func langOf(site Site, file string) string {
	for _, l := range site.Languages {
		if l.Prefix != "" && strings.HasPrefix(file, l.Prefix) {
			return l.Code
		}
	}
	return site.Languages[0].Code
}

func TestNoticeAndCreditsOnEveryPage(t *testing.T) {
	both(t, func(t *testing.T, b built, _ string) {
		for file, p := range b.pages {
			lang := langOf(b.site, file)
			notice := html.EscapeString(b.strs[lang]["notice.affiliation"])
			if !strings.Contains(p, `<p class="notice" data-notice>`+notice+`</p>`) {
				t.Errorf("%s: missing non-affiliation notice", file)
			}
			if !strings.Contains(p, "data-credits") ||
				!strings.Contains(p, "https://github.com/BrOrlandi/whatsapp-mcp-local") ||
				!strings.Contains(p, "https://github.com/gotd/td") ||
				!strings.Contains(p, "Bruno Oliveira") ||
				!strings.Contains(p, "/blob/main/LICENSE") {
				t.Errorf("%s: missing credits", file)
			}
		}
		// The notice appears exactly once per page; the FAQ answers the
		// question and points to it instead of repeating it.
		for file, p := range b.pages {
			lang := langOf(b.site, file)
			if n := strings.Count(reviewContentText(p), b.strs[lang]["notice.affiliation"]); n != 1 {
				t.Errorf("%s: non-affiliation notice appears %d times, want 1", file, n)
			}
		}
		for _, l := range b.site.Languages {
			if !strings.Contains(b.pages[outFile(l, "index")], `id="faq-official"`) {
				t.Errorf("%s: FAQ lacks the non-affiliation entry", l.Code)
			}
		}
	})
}

func TestFrontendRules(t *testing.T) {
	both(t, func(t *testing.T, b built, _ string) {
		for file, p := range b.pages {
			for _, m := range reTag.FindAllStringSubmatch(p, -1) {
				tag, attrs := strings.ToLower(m[1]), m[2]
				switch tag {
				case "iframe", "form", "object", "embed", "frame", "base":
					t.Errorf("%s: forbidden <%s>", file, tag)
				case "script":
					if !regexp.MustCompile(`^ src="/assets/js/site\.[0-9a-f]{8}\.js" defer$`).MatchString(attrs) {
						t.Errorf("%s: script tag %q is not the single deferred site.js", file, attrs)
					}
				}
				if strings.Contains(attrs, " style=") {
					t.Errorf("%s: style attribute on <%s>", file, tag)
				}
				if reOnAttr.MatchString(attrs) {
					t.Errorf("%s: inline handler on <%s>", file, tag)
				}
			}
			if n := strings.Count(p, "<script"); n != 1 {
				t.Errorf("%s: %d script tags, want 1", file, n)
			}
			if n := strings.Count(p, `rel="stylesheet"`); n != 1 {
				t.Errorf("%s: %d stylesheets, want 1", file, n)
			}
			if strings.Contains(p, "<style") {
				t.Errorf("%s: inline <style>", file)
			}
			for _, bad := range []string{"javascript:", "data:", "application/ld+json"} {
				if strings.Contains(strings.ToLower(p), bad) {
					t.Errorf("%s: contains %q", file, bad)
				}
			}
			if n := len(reH1.FindAllString(p, -1)); n != 1 {
				t.Errorf("%s: %d <h1>, want 1", file, n)
			}
			if i := strings.Index(p, "<a "); i < 0 || !strings.HasPrefix(p[i:], `<a class="skip-link" href="#main">`) {
				t.Errorf("%s: the skip link is not the first link", file)
			}
			for _, landmark := range []string{"<header", "<main id=\"main\"", "<footer", "<nav"} {
				if !strings.Contains(p, landmark) {
					t.Errorf("%s: missing %s", file, landmark)
				}
			}
			if !strings.Contains(p, `aria-live="polite"`) {
				t.Errorf("%s: missing aria-live region", file)
			}
		}
		for name, data := range b.files {
			if !strings.HasSuffix(name, ".js") {
				continue
			}
			js := string(data)
			// Forbidden sinks, matched as plain text; the test never runs them.
			for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function", "fetch(", "XMLHttpRequest", "WebSocket", "sendBeacon", "import("} {
				if strings.Contains(js, sink) {
					t.Errorf("%s uses %s", name, sink)
				}
			}
			if regexp.MustCompile(`setTimeout\(\s*["'\x60]`).MatchString(js) {
				t.Errorf("%s: string setTimeout", name)
			}
		}
	})
}

func TestNoThirdPartyRequests(t *testing.T) {
	both(t, func(t *testing.T, b built, mode string) {
		for file, p := range b.pages {
			for _, m := range reTag.FindAllStringSubmatch(p, -1) {
				tag, attrs := strings.ToLower(m[1]), m[2]
				if tag == "a" {
					continue // navigation, not a request
				}
				for _, u := range reAttrURL.FindAllStringSubmatch(attrs, -1) {
					if strings.Contains(u[1], "//") && !(tag == "link" && (strings.Contains(attrs, `rel="canonical"`) || strings.Contains(attrs, `rel="alternate"`))) {
						t.Errorf("%s: <%s> loads %q", file, tag, u[1])
					}
				}
			}
		}
		for name, data := range b.files {
			if strings.HasSuffix(name, ".css") && regexp.MustCompile(`url\(\s*["']?(https?:)?//`).Match(data) {
				t.Errorf("%s: external url()", name)
			}
			if strings.HasSuffix(name, ".css") && bytes.Contains(data, []byte("@import")) {
				t.Errorf("%s: @import", name)
			}
		}
	})
}

func TestForbiddenOutput(t *testing.T) {
	both(t, func(t *testing.T, b built, _ string) {
		for name, data := range b.files {
			if !isText(name) {
				continue
			}
			text := string(data)
			// The credits must name the original project; nothing else may.
			text = reCredits.ReplaceAllString(text, "")
			lower := strings.ToLower(text)
			for _, bad := range []string{"whatsapp", "brorlandi", "wacli", "#2aabee", "#229ed9", "vercel", "donat", "doação", "donación", "ghost mode", "modo invisível", "modo invisible", "end-to-end", "ponta a ponta", "extremo a extremo", "ban-proof"} {
				if strings.Contains(lower, bad) {
					t.Errorf("%s contains %q", name, bad)
				}
			}
		}
	})
}

func isText(name string) bool {
	switch path.Ext(name) {
	case ".html", ".css", ".js", ".txt", ".xml", ".svg", "":
		return !strings.HasPrefix(path.Base(name), "OFL-")
	}
	return false
}

func TestAssetsFingerprintedAndExisting(t *testing.T) {
	reAsset := regexp.MustCompile(`/assets/[^"')\s]+`)
	reFP := regexp.MustCompile(`^/assets/.+\.[0-9a-f]{8}\.[a-z0-9]+$`)
	both(t, func(t *testing.T, b built, _ string) {
		for name, data := range b.files {
			if !(strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".css")) {
				continue
			}
			for _, ref := range reAsset.FindAllString(string(data), -1) {
				if !reFP.MatchString(ref) {
					t.Errorf("%s: %s is not fingerprinted", name, ref)
					continue
				}
				if _, ok := b.files[strings.TrimPrefix(ref, "/")]; !ok {
					t.Errorf("%s: %s does not exist", name, ref)
				}
			}
		}
		for name := range b.files {
			if strings.HasPrefix(name, "assets/") && !reFP.MatchString("/"+name) {
				t.Errorf("unfingerprinted asset in output: %s", name)
			}
			if strings.HasSuffix(name, ".md") {
				t.Errorf("documentation shipped in output: %s", name)
			}
		}
		// OFL condition 2: the license travels with the fonts.
		var fonts, licenses int
		for name := range b.files {
			if strings.HasPrefix(name, "assets/fonts/") && strings.HasSuffix(name, ".woff2") {
				fonts++
			}
			if strings.HasPrefix(name, "assets/fonts/OFL-") {
				licenses++
			}
		}
		if fonts != 2 || licenses != 2 {
			t.Errorf("fonts %d, OFL files %d; want 2 and 2", fonts, licenses)
		}
	})
}

func TestFingerprintChangesWithContent(t *testing.T) {
	a := fingerprint("css/site.css", []byte("a"))
	if a == fingerprint("css/site.css", []byte("b")) || !regexp.MustCompile(`^css/site\.[0-9a-f]{8}\.css$`).MatchString(a) {
		t.Fatalf("bad fingerprint %q", a)
	}
}

// TestInternalLinks resolves every same-site href the way Pages would serve
// it, and requires the canonical trailing-slash form (no redirect, no 404).
func TestInternalLinks(t *testing.T) {
	reHref := regexp.MustCompile(`<a [^>]*href="([^"]+)"`)
	both(t, func(t *testing.T, b built, mode string) {
		for file, p := range b.pages {
			ids := map[string]bool{}
			for _, m := range regexp.MustCompile(`\sid="([^"]+)"`).FindAllStringSubmatch(p, -1) {
				ids[m[1]] = true
			}
			for _, m := range reHref.FindAllStringSubmatch(p, -1) {
				href := html.UnescapeString(m[1])
				if strings.HasPrefix(href, "#") {
					if !ids[href[1:]] {
						t.Errorf("%s: anchor %s has no target", file, href)
					}
					continue
				}
				if strings.Contains(href, "://") {
					if strings.Contains(href, b.site.BaseURL) {
						t.Errorf("%s: absolute self link %s", file, href)
					}
					continue
				}
				target, frag, _ := strings.Cut(href, "#")
				var out string
				switch {
				case strings.HasSuffix(target, "/"):
					out = strings.TrimPrefix(target, "/") + "index.html"
				case strings.HasPrefix(target, "/assets/") || strings.HasPrefix(target, "/prelaunch/"):
					out = strings.TrimPrefix(target, "/")
				default:
					t.Errorf("%s: link %s is not in canonical trailing-slash form", file, href)
					continue
				}
				dest, ok := b.pages[out]
				if !ok {
					if _, ok := b.files[out]; !ok {
						t.Errorf("%s: link %s resolves to nothing", file, href)
					}
					continue
				}
				if frag != "" && !strings.Contains(dest, `id="`+frag+`"`) {
					t.Errorf("%s: link %s: fragment missing in target", file, href)
				}
			}
		}
	})
}

func TestPrelaunchMode(t *testing.T) {
	b := prelaunch(t)
	reRel := regexp.MustCompile(`github\.com/[^"\s]*/releases`)
	for name, data := range b.files {
		if reRel.Match(data) {
			t.Errorf("%s: release URL in pre-launch output", name)
		}
		if bytes.Contains(data, []byte("api.github.com")) {
			t.Errorf("%s: GitHub API reference", name)
		}
	}
	for file, p := range b.pages {
		if !strings.Contains(p, `<meta name="robots" content="noindex, nofollow">`) {
			t.Errorf("%s: missing robots noindex", file)
		}
		for _, m := range regexp.MustCompile(`<a class="dl-link" href="([^"]+)"`).FindAllStringSubmatch(p, -1) {
			if !strings.HasPrefix(m[1], "/prelaunch/") || !strings.HasSuffix(m[1], ".txt") {
				t.Errorf("%s: download link %s", file, m[1])
			}
			if _, ok := b.files[strings.TrimPrefix(m[1], "/")]; !ok {
				t.Errorf("%s: %s missing", file, m[1])
			}
		}
	}
	for _, l := range b.site.Languages {
		p := b.pages[outFile(l, "download")]
		links := strings.Count(p, `class="dl-link"`)
		badges := strings.Count(p, `class="badge badge-test">`+b.strs[l.Code]["dl.badge.test"]+`<`)
		if links != 13 || badges != links {
			t.Errorf("%s: %d download links, %d test badges; want 13 each", l.Code, links, badges)
		}
	}
	if string(b.files["robots.txt"]) != "User-agent: *\nDisallow: /\n" {
		t.Errorf("robots.txt: %q", b.files["robots.txt"])
	}
	if _, ok := b.files["sitemap.xml"]; ok {
		t.Error("sitemap.xml must not exist before launch")
	}
	for name, data := range b.files {
		if strings.HasPrefix(name, "prelaunch/") {
			if len(data) > 1024 || !bytes.Contains(data, []byte("This is not an installer.")) {
				t.Errorf("%s: not a small test file", name)
			}
		}
	}
}

func TestReleaseMode(t *testing.T) {
	b := release(t)
	for name, data := range b.files {
		if bytes.Contains(bytes.ToLower(data), []byte("prelaunch")) && isText(name) {
			t.Errorf("%s contains 'prelaunch'", name)
		}
		if strings.HasPrefix(name, "prelaunch/") {
			t.Errorf("%s shipped in release", name)
		}
	}
	for file, p := range b.pages {
		has := strings.Contains(p, `name="robots"`)
		if is404 := strings.HasSuffix(file, "404.html"); has != is404 {
			t.Errorf("%s: robots meta present=%v", file, has)
		}
	}
	base := "https://github.com/" + b.site.Repo + "/releases/download/v1.2.3/"
	for _, l := range b.site.Languages {
		p := b.pages[outFile(l, "download")]
		links := regexp.MustCompile(`<a class="dl-link" href="([^"]+)"`).FindAllStringSubmatch(p, -1)
		if len(links) != 13 {
			t.Errorf("%s: %d links", l.Code, len(links))
		}
		for _, m := range links {
			if !strings.HasPrefix(m[1], base) {
				t.Errorf("%s: link %s is not versioned", l.Code, m[1])
			}
		}
		for _, want := range []string{base + "telegram-mcp_1.2.3_amd64.deb", base + "checksums.txt", base + "checksums.txt.minisig", "v1.2.3", `datetime="2026-12-01"`} {
			if !strings.Contains(p, want) {
				t.Errorf("%s: missing %s", l.Code, want)
			}
		}
		if strings.Contains(p, "badge-test") {
			t.Errorf("%s: test badge in release", l.Code)
		}
	}
	if !bytes.Contains(b.files["robots.txt"], []byte("Allow: /")) || !bytes.Contains(b.files["robots.txt"], []byte("Sitemap: "+b.site.BaseURL+"/sitemap.xml")) {
		t.Errorf("robots.txt: %q", b.files["robots.txt"])
	}
	sm := string(b.files["sitemap.xml"])
	if n := strings.Count(sm, "<loc>"); n != 9 {
		t.Errorf("sitemap has %d URLs, want 9", n)
	}
	if n := strings.Count(sm, `hreflang="x-default"`); n != 9 {
		t.Errorf("sitemap has %d x-default, want 9", n)
	}
}

// releaseFixture copies sources and resolves markers only in the isolated fixture.
func releaseFixture(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.WalkDir(srcDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, bytes.ReplaceAll(data, []byte("TODO("), []byte("RESOLVED(")), 0644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestReleaseRefusesTODO(t *testing.T) {
	src := releaseFixture(t)
	// A negative fixture must not rely on unfinished production content.
	if err := os.WriteFile(filepath.Join(src, "pages", "release-blocker.txt"), []byte("TODO(test): unresolved fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Build(Options{Src: src, Out: t.TempDir(), Mode: ModeRelease, Version: "v1.0.0"})
	if err == nil || !strings.Contains(err.Error(), "TODO(") {
		t.Fatalf("release build with injected TODO: err = %v", err)
	}
}

func TestReleaseAcceptsResolvedFixture(t *testing.T) {
	_, err := Build(Options{Src: releaseFixture(t), Out: t.TempDir(), Mode: ModeRelease, Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("release build without markers: %v", err)
	}
}

func TestOptionValidation(t *testing.T) {
	cases := []Options{
		{Mode: "draft"},
		{Mode: ModeRelease},
		{Mode: ModeRelease, Version: "1.0.0"},
		{Mode: ModePrelaunch, Version: "v1.0.0"},
		{Mode: ModeRelease, Version: "v1.0.0", Date: "01/12/2026", allowTODO: true},
		{Mode: ModePrelaunch, BaseURL: "http://example.com"},
	}
	for i, o := range cases {
		o.Src, o.Out = srcDir, t.TempDir()
		if _, err := Build(o); err == nil {
			t.Errorf("case %d (%+v): accepted", i, o)
		}
	}
}

func TestHeadersGolden(t *testing.T) {
	for _, mode := range []string{ModePrelaunch, ModeRelease} {
		want, err := os.ReadFile(filepath.Join("testdata", "headers."+mode+".golden"))
		if err != nil {
			t.Fatal(err)
		}
		if got := Headers(mode); got != string(want) {
			t.Errorf("%s _headers differ from golden:\n%s", mode, got)
		}
		b := build(t, func() Options {
			if mode == ModeRelease {
				return Options{Mode: mode, Version: "v1.0.0", allowTODO: true}
			}
			return Options{Mode: mode}
		}())
		if string(b.files["_headers"]) != string(want) {
			t.Errorf("%s: dist/_headers differs from golden", mode)
		}
		for i, line := range strings.Split(string(want), "\n") {
			if len(line) > 2000 {
				t.Errorf("%s _headers line %d exceeds 2000 chars", mode, i+1)
			}
		}
	}
}

func TestBudgets(t *testing.T) {
	b := prelaunch(t)
	var fonts int
	for name, data := range b.files {
		n := len(data)
		switch {
		case strings.HasSuffix(name, ".html") && n > 60*1024,
			strings.HasSuffix(name, ".css") && n > 40*1024,
			strings.HasSuffix(name, ".js") && n > 15*1024:
			t.Errorf("%s is %d bytes, over budget", name, n)
		}
		if strings.HasSuffix(name, ".woff2") {
			fonts += n
		}
		for _, ext := range []string{".jpg", ".jpeg", ".gif", ".mp4", ".webm"} {
			if strings.HasSuffix(name, ext) {
				t.Errorf("%s: unexpected media (launch without video)", name)
			}
		}
	}
	if fonts > 150*1024 {
		t.Errorf("fonts total %d bytes, over 150 KB", fonts)
	}
}

func TestDownloadCardsMatchRelease(t *testing.T) {
	var spec ReleaseSpec
	if err := readJSON(filepath.Join(srcDir, "release.json"), &spec); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"Telegram-MCP.dmg": true, "Telegram-MCP-Setup.exe": true,
		"Telegram-MCP-x86_64.AppImage": true, "Telegram-MCP-aarch64.AppImage": true,
		"telegram-mcp_{version}_amd64.deb": true, "telegram-mcp_{version}_arm64.deb": true,
		"telegram-mcp_linux_amd64.tar.gz": true, "telegram-mcp_linux_arm64.tar.gz": true,
		"telegram-mcp_darwin_amd64.tar.gz": true, "telegram-mcp_darwin_arm64.tar.gz": true,
		"telegram-mcp_windows_amd64.zip": true, "checksums.txt": true, "checksums.txt.minisig": true,
	}
	got := map[string]bool{}
	for _, g := range spec.Groups {
		for _, f := range g.Files {
			got[f.Name] = true
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("release.json assets %v, want the names from scripts/release.sh %v", got, want)
	}
}
