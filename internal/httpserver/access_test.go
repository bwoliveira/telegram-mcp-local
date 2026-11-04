package httpserver

import (
	"crypto/sha256"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func accessRequest(t *testing.T, h http.Handler, method, path, language string) *httptest.ResponseRecorder {
	t.Helper()
	r := request(method, path)
	if language != "" {
		r.Header.Set("Accept-Language", language)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// The public page follows the browser's language, as the panel does, and
// says the same thing in each: open it with telegram-mcp open, or print the
// one-use link over SSH and forward the loopback port.
func TestAccessPageLocalizedByAcceptLanguage(t *testing.T) {
	h := testHandler(nil, testToken)
	for _, tc := range []struct {
		header, lang, phrase string
	}{
		{"pt-BR,pt;q=0.9,en;q=0.8", "pt-BR", "Abra o painel"},
		{"pt-PT", "pt-BR", "Abra o painel"},
		{"pt", "pt-BR", "Abra o painel"},
		{"es-AR,es;q=0.9", "es", "Abra el panel"},
		{"en-US,en;q=0.9", "en", "Open the panel"},
		{"fr-FR,de;q=0.8", "en", "Open the panel"},
		{"", "en", "Open the panel"},
		{"en;q=0.5, es;q=0.9", "es", "Abra el panel"},
		{"pt;q=0, es;q=0.4", "es", "Abra el panel"},
		{"pt;q=0", "en", "Open the panel"},
		{"*;q=1, es;q=0.2", "es", "Abra el panel"},
		{"pt;q=bogus, es", "es", "Abra el panel"},
		{strings.Repeat("x-a,", 4000) + "es", "en", "Open the panel"},
	} {
		w := accessRequest(t, h, "GET", "/access", tc.header)
		body := w.Body.String()
		if w.Code != 200 {
			t.Fatalf("%q: want 200, got %d", tc.header, w.Code)
		}
		if !strings.Contains(body, `<html lang="`+tc.lang+`"`) || w.Header().Get("Content-Language") != tc.lang {
			t.Fatalf("%q: want lang %s, got header %q body %.200s", tc.header, tc.lang, w.Header().Get("Content-Language"), body)
		}
		if !strings.Contains(body, tc.phrase) {
			t.Fatalf("%q: missing %q", tc.header, tc.phrase)
		}
		for _, want := range []string{"telegram-mcp open", "telegram-mcp open --print", "ssh -L 8765:127.0.0.1:8765"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%q: missing %q", tc.header, want)
			}
		}
		if !strings.Contains(w.Header().Get("Vary"), "Accept-Language") {
			t.Fatalf("%q: language-dependent page without Vary", tc.header)
		}
		if w.Header().Get("Content-Type") != "text/html; charset=utf-8" {
			t.Fatalf("content type %q", w.Header().Get("Content-Type"))
		}
	}
}

// Every language explains why a browser session is needed, so nobody looks
// for a token field to paste the bearer into.
func TestAccessPageExplainsAuthorization(t *testing.T) {
	h := testHandler(nil, testToken)
	for lang, words := range map[string][]string{
		"pt-BR": {"Por que", "link de uso único", "túnel SSH", "não cole o token"},
		"es":    {"Por qué", "enlace de un solo uso", "túnel SSH", "no pegue el token"},
		"en":    {"Why", "one-use link", "SSH tunnel", "do not paste the token"},
	} {
		body := accessRequest(t, h, "GET", "/access", lang).Body.String()
		for _, want := range words {
			if !strings.Contains(body, want) {
				t.Fatalf("%s: missing %q", lang, want)
			}
		}
	}
}

// The page is the same static text for everyone: no account data, no field
// that takes a secret, nothing echoed from the request, no script.
func TestAccessPageStaticWithoutSecretsOrInputs(t *testing.T) {
	h := testHandler(func(mux *http.ServeMux) {
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, secretMarker) })
		mux.HandleFunc("GET /access", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, secretMarker) })
	}, testToken)
	base := accessRequest(t, h, "GET", "/access", "en").Body.String()
	r := request("GET", "/access?c=LEAKED_QUERY_CODE&token=LEAKED_QUERY_TOKEN")
	r.Header.Set("Accept-Language", "en")
	r.Header.Set("Cookie", "tgmcp_session=LEAKED_COOKIE")
	r.Header.Set("User-Agent", "LEAKED_UA<script>")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	if w.Code != 200 || body != base {
		t.Fatalf("access page depends on the request: %d", w.Code)
	}
	lower := strings.ToLower(body)
	for _, bad := range []string{secretMarker, testToken, "LEAKED", "<input", "<form", "<textarea", "<script", "<iframe", "<img", "<link", "javascript:", `class="shell"`, "/assets/", "style=\"", "http://", "https://"} {
		if strings.Contains(lower, strings.ToLower(bad)) {
			t.Fatalf("access page contains %q", bad)
		}
	}
	if regexp.MustCompile(`\son[a-z]+\s*=`).MatchString(lower) {
		t.Fatal("access page has an event handler")
	}
	if w.Header().Get("Set-Cookie") != "" {
		t.Fatal("access page set a cookie")
	}
	r = request("GET", "/access")
	r.Header.Set("Authorization", "Bearer "+testToken)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || strings.Contains(w.Body.String(), secretMarker) {
		t.Fatalf("bearer reached HTML route: %d %s", w.Code, w.Body)
	}
}

// The style allowance is a hash of the one inline stylesheet: no
// 'unsafe-inline', no scripts, no other source, framing still denied.
func TestAccessPageCSPHashesItsOnlyStylesheet(t *testing.T) {
	h := testHandler(nil, testToken)
	for _, lang := range []string{"pt-BR", "es", "en"} {
		w := accessRequest(t, h, "GET", "/access", lang)
		csp := w.Header().Get("Content-Security-Policy")
		styles := regexp.MustCompile(`(?s)<style>(.*?)</style>`).FindAllStringSubmatch(w.Body.String(), -1)
		if len(styles) != 1 {
			t.Fatalf("%s: want one stylesheet, got %d", lang, len(styles))
		}
		sum := sha256.Sum256([]byte(styles[0][1]))
		hash := "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
		want := "default-src 'none'; style-src " + hash + "; frame-ancestors 'none'; form-action 'none'; base-uri 'none'"
		if csp != want {
			t.Fatalf("%s: CSP\n got %q\nwant %q", lang, csp, want)
		}
	}
	// The redirect to the page keeps the strict policy, with no style at all.
	w := accessRequest(t, h, "GET", "/", "en")
	if w.Code != 303 || w.Header().Get("Location") != "/access" || w.Body.Len() != 0 || strings.Contains(w.Header().Get("Content-Security-Policy"), "style-src") {
		t.Fatalf("redirect changed: %d %q %q", w.Code, w.Header().Get("Location"), w.Header().Get("Content-Security-Policy"))
	}
}

// The page sits behind the same guards as before: foreign Host and Origin,
// cross-site fetches and preflight are refused, HEAD has no body, and a
// write to it is not a public page.
func TestAccessPageKeepsGuards(t *testing.T) {
	h := testHandler(nil, testToken)
	w := accessRequest(t, h, "HEAD", "/access", "es")
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Language") != "es" {
		t.Fatalf("HEAD: %d %d %q", w.Code, w.Body.Len(), w.Header().Get("Content-Language"))
	}
	for name, mutate := range map[string]func(*http.Request){
		"foreign host":   func(r *http.Request) { r.Host = "attacker.example:8765" },
		"other port":     func(r *http.Request) { r.Host = "127.0.0.1:1" },
		"foreign origin": func(r *http.Request) { r.Header.Set("Origin", "http://attacker.example") },
		"cross-site":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
	} {
		r := request("GET", "/access")
		mutate(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 || strings.Contains(w.Body.String(), "telegram-mcp open") {
			t.Fatalf("%s: want 403 without page, got %d", name, w.Code)
		}
	}
	for _, method := range []string{"POST", "PUT", "DELETE", "OPTIONS"} {
		w := accessRequest(t, h, method, "/access", "en")
		if w.Code != 401 && w.Code != 403 || strings.Contains(w.Body.String(), "telegram-mcp open") {
			t.Fatalf("%s /access: %d", method, w.Code)
		}
	}
	w = accessRequest(t, h, "GET", "/access", "en")
	for header, want := range map[string]string{"Cache-Control": "no-store", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff", "X-Frame-Options": "DENY"} {
		if got := w.Header().Get(header); got != want {
			t.Fatalf("%s: %q", header, got)
		}
	}
	// Without a token the server fails closed, and the page still says how
	// to get in rather than offering a way around the token.
	w = accessRequest(t, testHandler(nil, ""), "GET", "/access", "en")
	if w.Code != 200 || strings.Contains(strings.ToLower(w.Body.String()), "<input") {
		t.Fatalf("tokenless access page: %d", w.Code)
	}
}

// The tunnel command names the port the server actually listens on.
func TestAccessPageNamesListeningPort(t *testing.T) {
	srv := httptest.NewServer(testHandler(nil, testToken))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/access")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	p := strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
	if want := "ssh -L " + p + ":127.0.0.1:" + p; resp.StatusCode != 200 || !strings.Contains(string(body), want) {
		t.Fatalf("want %q, got %d", want, resp.StatusCode)
	}
}
