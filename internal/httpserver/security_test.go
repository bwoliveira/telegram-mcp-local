package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/panel"
)

const testToken = "synthetic-token-32-bytes-for-tests"
const testBootstrapSecret = "synthetic-panel-bootstrap-secret-for-tests"
const secretMarker = "PRIVATE_STATUS_QR_MESSAGE_MARKER"

func request(method, path string) *http.Request {
	r := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, nil)
	return r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8765}))
}

func testHandler(register func(*http.ServeMux), token string) http.Handler {
	return Handler(mcp.New(mcp.Config{}), Options{Token: token, PanelBootstrapSecret: testBootstrapSecret, Register: register, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

// Read the real ServeMux registry rather than maintaining a second route list.
// No unsafe is used; fail loudly if a future Go changes this test-only layout.
func registeredPatterns(t *testing.T, mux *http.ServeMux) []string {
	t.Helper()
	root := reflect.ValueOf(mux).Elem().FieldByName("tree")
	if !root.IsValid() {
		t.Fatal("ServeMux tree layout changed")
	}
	var patterns []string
	var walk func(reflect.Value)
	walk = func(node reflect.Value) {
		if node.Kind() == reflect.Pointer {
			if node.IsNil() {
				return
			}
			node = node.Elem()
		}
		pattern := node.FieldByName("pattern")
		if !pattern.IsNil() {
			patterns = append(patterns, pattern.Elem().FieldByName("str").String())
		}
		for _, key := range []string{"multiChild", "emptyChild"} {
			walk(node.FieldByName(key))
		}
		children := node.FieldByName("children")
		entries := children.FieldByName("s")
		for i := 0; i < entries.Len(); i++ {
			walk(entries.Index(i).FieldByName("value"))
		}
		entriesMap := children.FieldByName("m")
		iter := entriesMap.MapRange()
		for iter.Next() {
			walk(iter.Value())
		}
	}
	walk(root)
	if len(patterns) == 0 {
		t.Fatal("cannot enumerate real ServeMux patterns")
	}
	return patterns
}

func patternRequest(pattern string) (string, string) {
	method, path, found := strings.Cut(pattern, " ")
	if !found {
		method, path = "GET", method
	}
	path = strings.ReplaceAll(path, "{$}", "")
	for strings.Contains(path, "{") {
		start := strings.Index(path, "{")
		end := strings.Index(path[start:], "}") + start
		path = path[:start] + "sample" + path[end+1:]
	}
	return method, path
}

func TestEveryRegisteredRouteFailsClosedWithoutCredentials(t *testing.T) {
	var patterns []string
	h := testHandler(func(mux *http.ServeMux) {
		// A non-nil Host registers desktop-only routes too. Its nil embedded
		// implementation deliberately panics if authentication ever invokes it.
		p := &panel.Panel{Host: &struct{ panel.Host }{}}
		p.Register(mux)
		p.RegisterWebhooks(mux, nil, false)
		mux.HandleFunc("GET /future-page", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, secretMarker) })
		mux.HandleFunc("POST /api/future-write", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, secretMarker) })
		patterns = registeredPatterns(t, mux)
	}, testToken)
	patterns = append(patterns, "POST /mcp", "GET /health", "GET /media/sample", "GET /healthz")
	checked := 0
	for _, pattern := range patterns {
		method, path := patternRequest(pattern)
		if strings.HasPrefix(path, "/assets/") || path == "/favicon.svg" || path == "/favicon.ico" || path == "/apple-touch-icon.png" || path == "/healthz" || path == "/bootstrap" {
			continue
		}
		checked++
		t.Run(pattern, func(t *testing.T) {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(method, path))
			page := method == "GET" && !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/media/") && path != "/health" && path != "/mcp"
			if page {
				if w.Code != 303 || w.Header().Get("Location") != "/access" || w.Body.Len() != 0 {
					t.Fatalf("unsafe page: %d %s", w.Code, w.Body)
				}
			} else {
				want := http.StatusUnauthorized
				if path == "/api/session/bootstrap" {
					want = http.StatusForbidden
				}
				if w.Code != want {
					t.Fatalf("want %d, got %d %s", want, w.Code, w.Body)
				}
			}
			if strings.Contains(w.Body.String(), secretMarker) || strings.Contains(w.Body.String(), "PNG") {
				t.Fatal("private data leaked")
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("private response is cacheable")
			}
			for _, deniedMethod := range []string{method, http.MethodOptions} {
				r := request(deniedMethod, path)
				r.Header.Set("Authorization", "Bearer "+testToken)
				r.Header.Set("Origin", "http://attacker.example")
				r.Header.Set("Access-Control-Request-Method", method)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 403 || strings.Contains(w.Body.String(), secretMarker) || w.Header().Get("Access-Control-Allow-Origin") != "" {
					t.Fatalf("unsafe foreign-origin/preflight response: %d %s", w.Code, w.Body)
				}
			}
		})
	}
	t.Logf("checked %d private route cases from the real registry (%d cases including public exceptions)", checked, len(patterns))
}

func TestMandatoryBearerAndNoCookieOrQueryBypass(t *testing.T) {
	for _, configured := range []string{testToken, ""} {
		h := testHandler(nil, configured)
		for _, auth := range []string{"", "Bearer ", "Bearer wrong", "Bearer " + testToken[:len(testToken)-1], "Basic " + testToken, "Bearer " + testToken + " ", "Bearer  " + testToken} {
			for _, path := range []string{"/mcp?token=" + testToken, "/health", "/media/sample"} {
				r := request("POST", path)
				r.Header.Set("Authorization", auth)
				r.Header.Set("Cookie", "tgmcp_session="+testToken+"; internal=true")
				r.Header.Set("X-Internal", "1")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				if w.Code != 401 {
					t.Fatalf("configured=%q auth=%q path=%s: %d", configured, auth, path, w.Code)
				}
			}
		}
	}
}
