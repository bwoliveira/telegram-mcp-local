package httpserver

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
)

func TestGuardStrictAuthorityAndOrigin(t *testing.T) {
	h := Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), nil)
	cases := []struct {
		name, host, origin, path, method, site string
		code                                   int
	}{
		{name: "ipv4", host: "127.0.0.1:8765", code: 204},
		{name: "localhost", host: "localhost:8765", code: 204},
		{name: "ipv6", host: "[::1]:8765", code: 204},
		{name: "host wrong port", host: "127.0.0.1:8766", code: 403},
		{name: "host no port", host: "localhost", code: 403},
		{name: "host padded port", host: "localhost:08765", code: 403},
		{name: "host trailing dot", host: "localhost.:8765", code: 403},
		{name: "host uppercase", host: "LOCALHOST:8765", code: 403},
		{name: "host rebind", host: "127.0.0.1.attacker.test:8765", code: 403},
		{name: "host alternate loopback", host: "127.0.0.2:8765", code: 403},
		{name: "host shortened IP", host: "127.1:8765", code: 403},
		{name: "host numeric IP", host: "2130706433:8765", code: 403},
		{name: "host mapped ipv6", host: "[::ffff:127.0.0.1]:8765", code: 403},
		{name: "same origin", origin: "http://127.0.0.1:8765", code: 204},
		{name: "https", origin: "https://127.0.0.1:8765", code: 403},
		{name: "null", origin: "null", code: 403},
		{name: "foreign", origin: "http://attacker.test", code: 403},
		{name: "wrong origin port", origin: "http://127.0.0.1:8766", code: 403},
		{name: "origin missing port", origin: "http://127.0.0.1", code: 403},
		{name: "origin path", origin: "http://127.0.0.1:8765/", code: 403},
		{name: "origin credentials", origin: "http://user@127.0.0.1:8765", code: 403},
		{name: "origin query", origin: "http://127.0.0.1:8765?q=1", code: 403},
		{name: "origin fragment", origin: "http://127.0.0.1:8765#x", code: 403},
		{name: "origin alternate host", origin: "http://localhost:8765", code: 403},
		{name: "origin multiple", origin: "http://127.0.0.1:8765 http://attacker.test", code: 403},
		{name: "mcp any origin", origin: "http://127.0.0.1:8765", path: "/mcp", method: "POST", code: 403},
		{name: "mcp no origin", path: "/mcp", method: "POST", code: 204},
		{name: "encoded mcp origin", origin: "http://127.0.0.1:8765", path: "/%6dcp", code: 403},
		{name: "options", method: "OPTIONS", code: 403},
		{name: "cross site fetch", site: "cross-site", code: 403},
		{name: "same site other port fetch", site: "same-site", code: 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			method, path := tc.method, tc.path
			if method == "" {
				method = "GET"
			}
			if path == "" {
				path = "/api/probe"
			}
			r := request(method, path)
			if tc.host != "" {
				r.Host = tc.host
			}
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.site != "" {
				r.Header.Set("Sec-Fetch-Site", tc.site)
			}
			r.Header.Set("Authorization", "Bearer "+testToken)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.code {
				t.Fatalf("want %d got %d: %s", tc.code, w.Code, w.Body)
			}
			if w.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Fatal("CORS allowed")
			}
		})
	}
	for _, path := range []string{"/api/probe", "/mcp", "/health", "/media/sample", "/"} {
		for _, origin := range [][]string{{""}, {"http://127.0.0.1:8765", "http://127.0.0.1:8765"}} {
			r := request("GET", path)
			r.Header["Origin"] = origin
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("ambiguous Origin accepted at %s: %d", path, w.Code)
			}
		}
	}
	for _, addr := range []net.Addr{nil, &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 8765}, &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 8766}} {
		r := request("GET", "/healthz")
		r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, addr))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("invalid local listener context %v accepted", addr)
		}
	}
}

type desktopKey struct{}

func TestInProcessTrustCannotBeForgedOnHTTP(t *testing.T) {
	options := Options{Token: testToken, Trusted: func(r *http.Request) bool { return r.Context().Value(desktopKey{}) == true }, Register: func(mux *http.ServeMux) {
		mux.HandleFunc("GET /api/private", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, secretMarker) })
	}}
	routes := Routes(mcp.New(mcp.Config{}), options)
	r := httptest.NewRequest("GET", "http://app.internal/api/private", nil)
	r = r.WithContext(context.WithValue(r.Context(), desktopKey{}, true))
	r.Header.Set("Origin", "wails://localhost")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	routes.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != secretMarker {
		t.Fatalf("internal desktop rejected: %d %s", w.Code, w.Body)
	}
	r = httptest.NewRequest("GET", "http://app.internal/api/private?internal=true", nil)
	r.Header.Set("X-Internal", "true")
	r.Header.Set("Cookie", "trusted=true")
	w = httptest.NewRecorder()
	routes.ServeHTTP(w, r)
	if w.Code != 401 || strings.Contains(w.Body.String(), secretMarker) {
		t.Fatal("forged desktop accepted")
	}
}

func TestRealListenerBearerMCPAndBrowserIsolation(t *testing.T) {
	h := testHandler(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, secretMarker) })
	}, testToken)
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := srv.Client()
	for _, tc := range []struct {
		path, auth, origin, host, method string
		code                             int
		want                             string
	}{
		{path: "/api/status", code: 401},
		{path: "/api/status", auth: testToken, code: 200, want: secretMarker},
		{path: "/api/status", auth: testToken, host: "localhost:1", code: 403},
		{path: "/api/status", auth: testToken, origin: "https://" + srv.Listener.Addr().String(), code: 403},
		{path: "/api/status", auth: testToken, origin: srv.URL, code: 200, want: secretMarker},
		{path: "/api/status", auth: testToken, method: "OPTIONS", code: 403},
		{path: "/healthz", code: 200, want: "ok\n"},
		{path: "/", code: 200, want: "telegram-mcp open"},
		{path: "/mcp", auth: testToken, origin: srv.URL, method: "POST", code: 403},
		{path: "/mcp", auth: testToken, method: "POST", code: 200, want: `"protocolVersion"`},
	} {
		method := tc.method
		if method == "" {
			method = "GET"
		}
		r, err := http.NewRequest(method, srv.URL+tc.path, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		if tc.auth != "" {
			r.Header.Set("Authorization", "Bearer "+tc.auth)
		}
		if tc.origin != "" {
			r.Header.Set("Origin", tc.origin)
		}
		if tc.host != "" {
			r.Host = tc.host
		}
		resp, err := client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != tc.code || (tc.want != "" && !strings.Contains(string(body), tc.want)) {
			t.Fatalf("%s %s: %d %s", method, tc.path, resp.StatusCode, body)
		}
	}
}
