package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// mediaLinks serves short-lived file capabilities behind the HTTP transport's
// mandatory bearer/session guard. A random URL is NOT authentication.
type mediaLinks struct {
	mu    sync.Mutex
	links map[string]mediaLink
}

type mediaLink struct {
	path    string
	mime    string
	expires time.Time
	valid   func(context.Context) bool
}

func newMediaLinks() *mediaLinks { return &mediaLinks{links: map[string]mediaLink{}} }

func (l *mediaLinks) add(path, mime string, ttl time.Duration) string {
	return l.addChecked(path, mime, ttl, nil)
}

func (l *mediaLinks) addChecked(path, mime string, ttl time.Duration, valid func(context.Context) bool) string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	token := hex.EncodeToString(b)
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for k, v := range l.links {
		if !now.Before(v.expires) {
			delete(l.links, k)
		}
	}
	l.links[token] = mediaLink{path: path, mime: mime, expires: now.Add(ttl), valid: valid}
	return token
}

// RevokePath is also safe to call from the daemon's unlink hook. Revocation is
// permanent even if a later download recreates the same file path.
func (l *mediaLinks) RevokePath(path string) {
	absolute, _ := filepath.Abs(path)
	l.mu.Lock()
	defer l.mu.Unlock()
	for token, link := range l.links {
		p, _ := filepath.Abs(link.path)
		if p == absolute {
			delete(l.links, token)
		}
	}
}

// RevokeMediaPath lets the daemon invalidate capability links immediately from
// its unlink hook, in addition to the per-request lifecycle validation.
func (s *Server) RevokeMediaPath(path string) { s.links.RevokePath(path) }

// ServeHTTP answers GET/HEAD /media/{token}. The enclosing HTTP server must
// authenticate first. The lifecycle callback is checked for every request,
// including Range/HEAD; a deletion or TTL expiry cannot survive in a URL cache.
func (l *mediaLinks) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/media/")
	l.mu.Lock()
	link, ok := l.links[token]
	l.mu.Unlock()
	if !ok || !time.Now().Before(link.expires) || (link.valid != nil && !link.valid(r.Context())) {
		l.mu.Lock()
		delete(l.links, token)
		l.mu.Unlock()
		http.NotFound(w, r)
		return
	}
	f, err := mediaOpenFile(filepath.Dir(link.path), link.path)
	if err != nil {
		l.RevokePath(link.path)
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if link.mime != "" {
		w.Header().Set("Content-Type", link.mime)
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(link.path)}))
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}
