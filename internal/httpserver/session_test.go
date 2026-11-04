package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
)

func bootstrapSession(t *testing.T, h http.Handler) (*http.Cookie, string, string) {
	t.Helper()
	r := request("POST", "/api/session/bootstrap")
	r.Header.Set(PanelBootstrapHeader, testBootstrapSecret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("issue: %d %s", w.Code, w.Body)
	}
	var v struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(v.URL)
	if err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", u.RequestURI()))
	if w.Code != 303 || w.Header().Get("Location") != "/" {
		t.Fatalf("exchange: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), u.Query().Get("c")) {
		t.Fatal("SEC-B04 code leaked")
	}
	cs := w.Result().Cookies()
	if len(cs) != 1 {
		t.Fatal("no cookie")
	}
	c := cs[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.Secure {
		t.Fatalf("unsafe cookie: %v", c)
	}
	r = request("GET", "/api/session")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var s struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil || s.CSRF == "" {
		t.Fatalf("session: %d %s", w.Code, w.Body)
	}
	return c, s.CSRF, u.RequestURI()
}

func TestSEC_B01_B04_C01_C04_T03_R04(t *testing.T) {
	effects := 0
	h := testHandler(func(mux *http.ServeMux) {
		mux.HandleFunc("POST /api/probe", func(w http.ResponseWriter, r *http.Request) { effects++; w.WriteHeader(204) })
		mux.HandleFunc("POST /api/mode", func(w http.ResponseWriter, r *http.Request) { effects++; w.WriteHeader(204) })
	}, testToken)
	c, csrf, link := bootstrapSession(t, h)
	_, other, _ := bootstrapSession(t, h)
	for _, tc := range []struct {
		name, path, csrf, origin, site, ct string
		bearer                             bool
		want                               int
	}{
		{name: "SEC-C01 missing", path: "/api/probe", want: 403},
		{name: "SEC-C02 other session", path: "/api/probe", csrf: other, origin: "http://127.0.0.1:8765", want: 403},
		{name: "SEC-C03 foreign", path: "/api/probe", csrf: csrf, origin: "http://evil.example", site: "cross-site", want: 403},
		{name: "missing browser provenance", path: "/api/probe", csrf: csrf, want: 403},
		{name: "SEC-C04 text", path: "/api/probe", csrf: csrf, origin: "http://127.0.0.1:8765", ct: "text/plain", want: 415},
		{name: "SEC-T03 cookie MCP", path: "/mcp", want: 401},
		{name: "SEC-R04 bearer mode", path: "/api/mode", bearer: true, want: 403},
		{name: "SEC-B01 reuse", path: link, want: 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			method := "POST"
			if tc.path == link {
				method = "GET"
			}
			r := request(method, tc.path)
			r.AddCookie(c)
			r.Header.Set("X-CSRF-Token", tc.csrf)
			r.Header.Set("Origin", tc.origin)
			if tc.origin == "" {
				r.Header.Del("Origin")
			}
			r.Header.Set("Sec-Fetch-Site", tc.site)
			ct := tc.ct
			if ct == "" {
				ct = "application/json"
			}
			r.Header.Set("Content-Type", ct)
			if tc.bearer {
				r.Header.Set("Authorization", "Bearer "+testToken)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d: %s", w.Code, tc.want, w.Body)
			}
		})
	}
	if effects != 0 {
		t.Fatalf("negative requests caused %d effects", effects)
	}
	r := request("POST", "/api/probe")
	r.AddCookie(c)
	r.Header.Set("X-CSRF-Token", csrf)
	r.Header.Set("Origin", "http://127.0.0.1:8765")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || effects != 1 {
		t.Fatalf("legitimate write: %d effects %d", w.Code, effects)
	}
}

func TestSEC_B02_B03_B05_B06(t *testing.T) {
	now := time.Now()
	h := Handler(mcp.New(mcp.Config{}), Options{Token: testToken, PanelBootstrapSecret: testBootstrapSecret, Now: func() time.Time { return now }})
	c, _, _ := bootstrapSession(t, h)
	r := request("POST", "/api/session/bootstrap")
	r.Header.Set(PanelBootstrapHeader, testBootstrapSecret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var v struct {
		URL string `json:"url"`
	}
	json.Unmarshal(w.Body.Bytes(), &v)
	u, _ := url.Parse(v.URL)
	r = request("GET", u.RequestURI())
	r.Host = "evil.example"
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", u.RequestURI()))
	if w.Code != 303 {
		t.Fatal("host consumed code", w.Code)
	}
	now = now.Add(61 * time.Second)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, request("GET", u.RequestURI()))
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	for i := 0; i < 10; i++ {
		w = httptest.NewRecorder()
		h.ServeHTTP(w, request("GET", "/bootstrap?c=altered"))
		want := 401
		if i == 8 || i == 9 {
			want = 429
		}
		if w.Code != want {
			t.Fatalf("attempt %d: %d", i, w.Code)
		}
	}
	restarted := testHandler(nil, testToken)
	r = request("GET", "/api/session")
	r.AddCookie(c)
	w = httptest.NewRecorder()
	restarted.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("restart retained session", w.Code)
	}
}
