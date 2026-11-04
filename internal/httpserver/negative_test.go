package httpserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
)

func TestSEC_L01_L02_RedactionAndRecovery(t *testing.T) {
	const marker = "SYNTHETIC_NEVER_LOG_THIS_MARKER"
	var logs bytes.Buffer
	logger := slog.New(Redact(slog.NewJSONHandler(&logs, nil)))
	attrs := []any{"count", 1, "code", marker, "api_hash", marker, "file_reference", marker, "error", errors.New(marker), "port", engine.Secret(marker)}
	logger.With("session", marker).WithGroup(marker).Error(marker, attrs...)
	h := Handler(mcp.New(mcp.Config{}), Options{Token: testToken, Logger: logger, Register: func(mux *http.ServeMux) {
		mux.HandleFunc("GET /api/panic", func(w http.ResponseWriter, r *http.Request) { panic(marker) })
		mux.HandleFunc("GET /api/error", func(w http.ResponseWriter, r *http.Request) { http.Error(w, marker, 400) })
	}})
	for _, path := range []string{"/api/panic", "/api/error"} {
		r := request("GET", path)
		r.Header.Set("Authorization", "Bearer "+testToken)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code < 400 || strings.Contains(w.Body.String(), marker) {
			t.Fatalf("unsafe error: %d %s", w.Code, w.Body)
		}
	}
	r := request("GET", "/api/state?secret="+marker)
	r.Host = marker
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if strings.Contains(logs.String(), marker) || strings.Contains(logs.String(), testToken) {
		t.Fatal("sensitive log value")
	}
	if !strings.Contains(logs.String(), `"count":1`) || !strings.Contains(logs.String(), "daemon event") {
		t.Fatal("vacuous logger")
	}
}

func TestSEC_P01_LoginRateLimitAndAPIRequestLimit(t *testing.T) {
	now := time.Now()
	effects := 0
	h := Handler(mcp.New(mcp.Config{}), Options{Token: testToken, Now: func() time.Time { return now }, Register: func(mux *http.ServeMux) {
		for _, path := range []string{"/api/pair/code", "/api/pair/password"} {
			mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
				effects++
				if _, err := io.ReadAll(r.Body); err != nil {
					http.Error(w, "invalid body", 413)
					return
				}
				w.WriteHeader(204)
			})
		}
	}})
	for _, path := range []string{"/api/pair/code", "/api/pair/password"} {
		for i := 0; i < 6; i++ {
			r := request("POST", path)
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 204
			if i == 5 {
				want = 429
			}
			if w.Code != want {
				t.Fatalf("%s %d: %d", path, i, w.Code)
			}
		}
	}
	if effects != 10 {
		t.Fatal(effects)
	}
	now = now.Add(time.Minute)
	r := request("POST", "/api/pair/code")
	r.Body = io.NopCloser(strings.NewReader(strings.Repeat("x", 65537)))
	r.Header.Set("Authorization", "Bearer "+testToken)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 413 {
		t.Fatal(w.Code)
	}
}

func TestSEC_B02_ExpirationSessionLifetimeLogoutAndAtomicConsumption(t *testing.T) {
	now := time.Now()
	opts := Options{Token: testToken, PanelBootstrapSecret: testBootstrapSecret, Now: func() time.Time { return now }}
	h := Handler(mcp.New(mcp.Config{}), opts)
	c, csrf, _ := bootstrapSession(t, h)
	// Issue a code and let it expire WITHOUT first consuming it.
	r := request("POST", "/api/session/bootstrap")
	r.Header.Set(PanelBootstrapHeader, testBootstrapSecret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	link := strings.Split(strings.Split(w.Body.String(), `"url":"`)[1], `"`)[0]
	link = strings.TrimPrefix(link, "http://127.0.0.1:8765")
	now = now.Add(61 * time.Second)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", link))
	if w.Code != 401 {
		t.Fatal("expired code accepted", w.Code)
	}
	// Logout requires both the cookie and its CSRF proof.
	r = request("POST", "/api/session/logout")
	r.AddCookie(c)
	r.Header.Set("Origin", "http://127.0.0.1:8765")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-CSRF-Token", csrf)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	r = request("GET", "/api/session")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("logout did not revoke session")
	}
	c, _, _ = bootstrapSession(t, h)
	now = now.Add(12 * time.Hour)
	r = request("GET", "/api/session")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("idle session accepted")
	}
	c, _, _ = bootstrapSession(t, h)
	for i := 0; i < 15; i++ {
		now = now.Add(11 * time.Hour)
		r = request("GET", "/api/session")
		r.AddCookie(c)
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("active session expired early: %d", i)
		}
	}
	now = now.Add(4 * time.Hour)
	r = request("GET", "/api/session")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("absolute session lifetime ignored")
	}
	// Parallel redemption has exactly one winner.
	stable := Handler(mcp.New(mcp.Config{}), Options{Token: testToken, PanelBootstrapSecret: testBootstrapSecret})
	r = request("POST", "/api/session/bootstrap")
	r.Header.Set(PanelBootstrapHeader, testBootstrapSecret)
	w = httptest.NewRecorder()
	stable.ServeHTTP(w, r)
	link = strings.Split(strings.Split(w.Body.String(), `"url":"`)[1], `"`)[0]
	link = strings.TrimPrefix(link, "http://127.0.0.1:8765")
	var wg sync.WaitGroup
	var wins atomic.Int32
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			stable.ServeHTTP(w, request("GET", link))
			if w.Code == 303 {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatal("bootstrap redemption not atomic", wins.Load())
	}
}

// Ensure hidden LogValuers are not evaluated while redacting.
type dangerousLogValue struct{}

func (dangerousLogValue) LogValue() slog.Value { panic("SYNTHETIC_LOGVALUER_MARKER") }
func TestRedactorDoesNotEvaluatePayloads(t *testing.T) {
	logger := slog.New(Redact(slog.NewJSONHandler(io.Discard, nil)))
	logger.Log(context.Background(), slog.LevelError, "event", "count", dangerousLogValue{})
}
