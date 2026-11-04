package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// contentTypes covers every extension the site ships, so the local server
// does not depend on the host's MIME database.
var contentTypes = map[string]string{
	".html":     "text/html; charset=utf-8",
	".css":      "text/css; charset=utf-8",
	".js":       "text/javascript; charset=utf-8",
	".svg":      "image/svg+xml",
	".png":      "image/png",
	".webp":     "image/webp",
	".woff2":    "font/woff2",
	".txt":      "text/plain; charset=utf-8",
	".xml":      "application/xml; charset=utf-8",
	".vtt":      "text/vtt; charset=utf-8",
	".mp4":      "video/mp4",
	".AppImage": "application/octet-stream",
}

// StaticHandler serves a built site the way Cloudflare Pages does, closely
// enough for QA: clean trailing-slash URLs, redirects from /x and .html
// forms, the nearest 404.html with status 404, and the _headers rules
// applied (including "! Name" removals). It is meant for loopback QA only.
type StaticHandler struct {
	root  fs.FS
	rules []headerRule
}

// NewStaticHandler loads dir/_headers and serves dir.
func NewStaticHandler(dir string) (*StaticHandler, error) {
	b, err := os.ReadFile(dir + "/_headers")
	if err != nil {
		return nil, err
	}
	rules := parseHeaders(string(b))
	seen := map[string]bool{}
	for _, rule := range rules {
		if seen[rule.pattern] {
			return nil, fmt.Errorf("duplicate _headers selector %q: Pages discards earlier blocks", rule.pattern)
		}
		seen[rule.pattern] = true
	}
	return &StaticHandler{root: os.DirFS(dir), rules: rules}, nil
}

func (h *StaticHandler) exists(name string) bool {
	st, err := fs.Stat(h.root, name)
	return err == nil && !st.IsDir()
}

func (h *StaticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := path.Clean("/" + r.URL.Path)
	if strings.HasSuffix(r.URL.Path, "/") && p != "/" {
		p += "/"
	}
	// Never serve the headers file itself.
	if p == "/_headers" {
		h.notFound(w, r, p)
		return
	}
	switch {
	case strings.HasSuffix(p, "/index.html"):
		h.redirect(w, r, strings.TrimSuffix(p, "index.html"))
		return
	case strings.HasSuffix(p, ".html") && p != "/404.html" && h.exists(strings.TrimPrefix(strings.TrimSuffix(p, ".html"), "/")+"/index.html"):
		h.redirect(w, r, strings.TrimSuffix(p, ".html")+"/")
		return
	}
	name := strings.TrimPrefix(p, "/")
	if strings.HasSuffix(p, "/") {
		name += "index.html"
	} else if !h.exists(name) && h.exists(name+"/index.html") {
		h.redirect(w, r, p+"/")
		return
	}
	if !h.exists(name) {
		h.notFound(w, r, p)
		return
	}
	h.serveFile(w, r, p, name, http.StatusOK)
}

func (h *StaticHandler) redirect(w http.ResponseWriter, r *http.Request, to string) {
	if r.URL.RawQuery != "" {
		to += "?" + r.URL.RawQuery
	}
	h.applyHeaders(w, r, to)
	http.Redirect(w, r, to, http.StatusPermanentRedirect)
}

// notFound serves the closest 404.html up the tree, as Pages does.
func (h *StaticHandler) notFound(w http.ResponseWriter, r *http.Request, p string) {
	dir := path.Dir(p)
	if strings.HasSuffix(p, "/") {
		dir = strings.TrimSuffix(p, "/")
	}
	for {
		candidate := strings.TrimPrefix(path.Join(dir, "404.html"), "/")
		if h.exists(candidate) {
			h.serveFile(w, r, p, candidate, http.StatusNotFound)
			return
		}
		if dir == "/" || dir == "." || dir == "" {
			break
		}
		dir = path.Dir(dir)
	}
	h.applyHeaders(w, r, p)
	http.Error(w, "not found", http.StatusNotFound)
}

func (h *StaticHandler) serveFile(w http.ResponseWriter, r *http.Request, urlPath, name string, status int) {
	data, err := fs.ReadFile(h.root, name)
	if err != nil {
		http.Error(w, "read error", http.StatusInternalServerError)
		return
	}
	ext := path.Ext(name)
	if ct, ok := contentTypes[ext]; ok {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	if ext == ".html" {
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	}
	h.applyHeaders(w, r, urlPath)
	if status != http.StatusOK {
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			_, _ = w.Write(data)
		}
		return
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
}

// applyHeaders sets every matching rule in file order. A header set by more
// than one rule is joined with ", ", like Pages does.
func (h *StaticHandler) applyHeaders(w http.ResponseWriter, r *http.Request, urlPath string) {
	host := r.Host
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	seen := map[string]bool{}
	for _, rule := range h.rules {
		if !matchPattern(rule.pattern, host, urlPath) {
			continue
		}
		for _, kv := range rule.set {
			key := http.CanonicalHeaderKey(kv[0])
			if seen[key] {
				w.Header().Set(key, w.Header().Get(key)+", "+kv[1])
			} else {
				w.Header().Set(key, kv[1])
				seen[key] = true
			}
		}
		for _, name := range rule.remove {
			w.Header().Del(name)
		}
	}
}
