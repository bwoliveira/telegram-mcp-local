package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func serveDist(t *testing.T) http.Handler {
	t.Helper()
	out := t.TempDir()
	if _, err := Build(Options{Src: srcDir, Out: out}); err != nil {
		t.Fatal(err)
	}
	h, err := NewStaticHandler(out)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(h http.Handler, host, target string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.Host = host
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestServeAppliesHeaders(t *testing.T) {
	h := serveDist(t)
	w := get(h, "127.0.0.1:8088", "/en/download/")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<html lang="en">`) {
		t.Fatalf("GET /en/download/: %d", w.Code)
	}
	hd := w.Header()
	if hd.Get("Content-Security-Policy") != csp {
		t.Errorf("CSP = %q", hd.Get("Content-Security-Policy"))
	}
	for name, want := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"X-Robots-Tag":                 "noindex, nofollow",
		"Content-Type":                 "text/html; charset=utf-8",
	} {
		if got := hd.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if hd.Get("Cache-Control") == "" || strings.Contains(hd.Get("Cache-Control"), "immutable") {
		t.Errorf("HTML Cache-Control = %q", hd.Get("Cache-Control"))
	}
	if hd.Get("Access-Control-Allow-Origin") != "" {
		t.Error("Access-Control-Allow-Origin must be removed")
	}
}

func TestServeAssetsAndPrelaunchFiles(t *testing.T) {
	h := serveDist(t)
	home := get(h, "127.0.0.1", "/").Body.String()
	i := strings.Index(home, "/assets/css/site.")
	css := home[i : i+strings.Index(home[i:], `"`)]
	w := get(h, "127.0.0.1", css)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || w.Header().Get("Content-Type") != "text/css; charset=utf-8" {
		t.Errorf("%s: %d %q %q", css, w.Code, w.Header().Get("Cache-Control"), w.Header().Get("Content-Type"))
	}
	w = get(h, "127.0.0.1", "/prelaunch/checksums.txt.txt")
	if w.Code != 200 || w.Header().Get("Content-Disposition") != "attachment" {
		t.Errorf("prelaunch file: %d %q", w.Code, w.Header().Get("Content-Disposition"))
	}
}

func TestServeRoutingLikePages(t *testing.T) {
	h := serveDist(t)
	for target, want := range map[string]string{
		"/download":            "/download/",
		"/download/index.html": "/download/",
		"/en/headless.html":    "/en/headless/",
		"/index.html":          "/",
	} {
		w := get(h, "127.0.0.1", target)
		if w.Code != http.StatusPermanentRedirect || w.Header().Get("Location") != want {
			t.Errorf("%s: %d -> %q, want 308 -> %q", target, w.Code, w.Header().Get("Location"), want)
		}
	}
	for target, lang := range map[string]string{
		"/nope/":       "pt-BR",
		"/en/missing":  "en",
		"/es/a/b/c":    "es",
		"/_headers":    "pt-BR",
		"/assets/x.js": "pt-BR",
	} {
		w := get(h, "127.0.0.1", target)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `<html lang="`+lang+`">`) {
			t.Errorf("%s: %d, want 404 page in %s", target, w.Code, lang)
		}
		if w.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s: 404 without CSP", target)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", w.Code)
	}
}

func TestPagesDevNoindexOnlyOnPagesDev(t *testing.T) {
	out := t.TempDir()
	if _, err := Build(Options{Src: srcDir, Out: out, Mode: ModeRelease, Version: "v1.0.0", allowTODO: true}); err != nil {
		t.Fatal(err)
	}
	h, err := NewStaticHandler(out)
	if err != nil {
		t.Fatal(err)
	}
	for host, want := range map[string]string{
		"telegram-mcp.bwoliveira.xyz":                   "",
		"bwoliveira-telegram-mcp.pages.dev":             "noindex",
		"0a1b2c3d.bwoliveira-telegram-mcp.pages.dev":    "noindex",
		"main.bwoliveira-telegram-mcp.pages.dev":        "noindex",
		"bwoliveira-telegram-mcp.pages.dev.example.com": "",
	} {
		if got := get(h, host, "/").Header().Get("X-Robots-Tag"); got != want {
			t.Errorf("host %s: X-Robots-Tag %q, want %q", host, got, want)
		}
	}
}

func TestGlobMatch(t *testing.T) {
	for _, c := range []struct {
		p, s string
		ok   bool
	}{
		{"/*", "/", true},
		{"/*", "/a/b", true},
		{"/assets/*", "/assets/x.css", true},
		{"/assets/*", "/assetsx", false},
		{":project.pages.dev/*", "p.pages.dev/x", true},
		{":version.:project.pages.dev/*", "p.pages.dev/x", false},
		{":version.:project.pages.dev/*", "v.p.pages.dev/x", true},
		{":project.pages.dev/*", ".pages.dev/x", false},
	} {
		if got := globMatch(c.p, c.s); got != c.ok {
			t.Errorf("globMatch(%q, %q) = %v", c.p, c.s, got)
		}
	}
}

func TestCheckLoopback(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:8088": true, "[::1]:8088": true, "localhost:8088": true,
		"0.0.0.0:8088": false, ":8088": false, "192.168.1.2:8088": false, "example.com:80": false,
	} {
		if err := checkLoopback(addr); (err == nil) != ok {
			t.Errorf("checkLoopback(%q) = %v", addr, err)
		}
	}
}
