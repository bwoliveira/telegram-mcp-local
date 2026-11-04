package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"fmt"
	"github.com/bwoliveira/telegram-mcp-local/internal/httpserver"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func testServerConfig(t *testing.T, s *httptest.Server) config {
	t.Helper()
	u, err := url.Parse(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return config{Port: port, Token: "synthetic-bearer-marker", PanelBootstrapSecret: "synthetic-panel-bootstrap-marker"}
}

func TestOpenUsesPanelBootstrapAuthorityAndOpensOnce(t *testing.T) {
	var requested int
	var target string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requested++
		if r.Method != "POST" || r.URL.Path != "/api/session/bootstrap" || r.Header.Get("Authorization") != "" || r.Header.Get(httpserver.PanelBootstrapHeader) != "synthetic-panel-bootstrap-marker" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("wrong bootstrap request: %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprintf(w, `{"url":%q}`, target)
	}))
	defer s.Close()
	cfg := testServerConfig(t, s)
	target = cfg.baseURL() + "/bootstrap?c=synthetic-one-time-code"
	var opened []string
	err := openPanel(context.Background(), cfg, func(u string) error { opened = append(opened, u); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if requested != 1 || len(opened) != 1 || opened[0] != target {
		t.Fatalf("requests=%d opened=%v", requested, opened)
	}
}

func TestOpenRefusesUntrustedOrFailedBootstrapWithoutLeaking(t *testing.T) {
	for _, body := range []string{`{"url":"https://evil.example/bootstrap?c=secret-code"}`, `{"url":"http://127.0.0.1:1/bootstrap?c=secret-code"}`, `{"url":"/bootstrap?c=secret-code"}`, `{"url":"http://127.0.0.1:1/?token=secret-code"}`, `{"url":`, `{}`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer s.Close()
			opened := 0
			err := openPanel(context.Background(), testServerConfig(t, s), func(string) error { opened++; return nil })
			if err == nil || opened != 0 {
				t.Fatalf("err=%v opened=%d", err, opened)
			}
			if strings.Contains(err.Error(), "secret-code") {
				t.Fatal("error exposed bootstrap")
			}
		})
	}
}

func TestOpenDoesNotFollowRedirectsOrEchoErrorBodies(t *testing.T) {
	var redirected int
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++; t.Error("followed bootstrap redirect") }))
	defer destination.Close()
	for _, status := range []int{http.StatusUnauthorized, http.StatusFound} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", destination.URL)
			w.WriteHeader(status)
			fmt.Fprint(w, "synthetic-secret-error")
		}))
		err := openPanel(context.Background(), testServerConfig(t, s), func(string) error { t.Error("browser opened after error"); return nil })
		s.Close()
		if err == nil || strings.Contains(err.Error(), "synthetic-secret-error") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	if redirected != 0 {
		t.Fatalf("redirect requests=%d", redirected)
	}
}

func TestOpenCommandModes(t *testing.T) {
	i18n.Set("en")
	defer i18n.Set(i18n.Source)
	for _, tc := range []struct {
		name, goos, display, wayland string
		args                         []string
		print                        bool
	}{
		{name: "explicit", goos: "darwin", args: []string{"--print"}, print: true},
		{name: "x11", goos: "linux", display: ":20"},
		{name: "wayland", goos: "linux", wayland: "wayland-0"},
		{name: "mac", goos: "darwin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var target string
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"url":%q}`, target) }))
			defer s.Close()
			cfg := testServerConfig(t, s)
			target = cfg.baseURL() + "/bootstrap?c=synthetic-one-use"
			var out, warnings bytes.Buffer
			opened := 0
			getenv := func(k string) string {
				if k == "DISPLAY" {
					return tc.display
				}
				if k == "WAYLAND_DISPLAY" {
					return tc.wayland
				}
				return ""
			}
			err := runOpen(context.Background(), cfg, tc.args, tc.goos, getenv, &out, &warnings, func(string) error { opened++; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if tc.print {
				if out.String() != target+"\n" || opened != 0 {
					t.Fatalf("print=%q opened=%d", out.String(), opened)
				}
				for _, marker := range []string{"Anyone", "one minute", "ssh -L", fmt.Sprintf("%d:127.0.0.1:%d", cfg.Port, cfg.Port)} {
					if !strings.Contains(warnings.String(), marker) {
						t.Errorf("missing warning %q", marker)
					}
				}
			} else if out.Len() != 0 || warnings.Len() != 0 || opened != 1 {
				t.Fatal("browser mode exposed URL or failed to open")
			}
			if strings.Contains(out.String()+warnings.String(), cfg.Token) {
				t.Fatal("bearer leaked")
			}
		})
	}
}

func TestOpenHeadlessRequiresExplicitPrintWithoutIssuingURL(t *testing.T) {
	defer i18n.Set(i18n.Source)
	for _, tc := range []struct {
		lang, marker string
	}{
		{lang: "pt-BR", marker: "Nenhuma tela"},
		{lang: "en", marker: "No browser display"},
		{lang: "es", marker: "No hay una pantalla"},
	} {
		t.Run(tc.lang, func(t *testing.T) {
			i18n.Set(tc.lang)
			var out, warnings bytes.Buffer
			opened := 0
			err := runOpen(context.Background(), config{}, nil, "linux", func(string) string { return "" }, &out, &warnings, func(string) error { opened++; return nil })
			if err == nil || !strings.Contains(err.Error(), "open --print") || !strings.Contains(err.Error(), tc.marker) {
				t.Fatalf("headless error = %v", err)
			}
			if out.Len() != 0 || warnings.Len() != 0 || opened != 0 {
				t.Fatalf("headless command disclosed or opened a URL: stdout=%q stderr=%q opened=%d", out.String(), warnings.String(), opened)
			}
		})
	}
}

func TestOpenCommandRejectsArgumentsBeforeRequest(t *testing.T) {
	for _, args := range [][]string{{"--unknown"}, {"--print", "unexpected"}, {"--print", "--print"}} {
		var out, warnings bytes.Buffer
		err := runOpen(context.Background(), config{}, args, "linux", func(string) string { return "" }, &out, &warnings, func(string) error { t.Fatal("opened"); return nil })
		if err == nil || !strings.Contains(err.Error(), "usage:") || out.Len() != 0 || warnings.Len() != 0 {
			t.Fatalf("invalid args result: %v", err)
		}
	}
}

func TestPrintedBootstrapSecurity(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	var logs bytes.Buffer
	h := httpserver.Handler(mcp.New(mcp.Config{}), httpserver.Options{Token: "synthetic-bearer-marker", PanelBootstrapSecret: "synthetic-panel-bootstrap-marker", Now: func() time.Time { return time.Unix(0, clock.Load()) }, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	s := httptest.NewServer(h)
	defer s.Close()
	cfg := testServerConfig(t, s)
	printURL := func() string {
		t.Helper()
		var out, warnings bytes.Buffer
		err := runOpen(context.Background(), cfg, []string{"--print"}, "linux", func(string) string { return "" }, &out, &warnings, func(string) error { t.Fatal("opened browser"); return nil })
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out.String())
	}
	client := daemonClient()
	defer client.CloseIdleConnections()
	request := func(target, host, origin string, want int) *http.Response {
		t.Helper()
		req, err := http.NewRequest("GET", target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if host != "" {
			req.Host = host
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("got %d want %d", resp.StatusCode, want)
		}
		return resp
	}
	target := printURL()
	request(target, "evil.example", "", http.StatusForbidden)
	request(target, "", "https://evil.example", http.StatusForbidden)
	resp := request(target, "", "", http.StatusSeeOther)
	if len(resp.Cookies()) != 1 || !resp.Cookies()[0].HttpOnly || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("missing protected browser session")
	}
	req, _ := http.NewRequest("GET", cfg.baseURL()+"/api/session", nil)
	req.AddCookie(resp.Cookies()[0])
	session, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	session.Body.Close()
	if session.StatusCode != http.StatusOK {
		t.Fatal("printed URL did not authenticate panel session")
	}
	request(target, "", "", http.StatusUnauthorized)
	expired := printURL()
	clock.Add(int64(time.Minute))
	request(expired, "", "", http.StatusUnauthorized)
	for _, secret := range []string{cfg.Token, target, expired, strings.Split(target, "?c=")[1]} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("HTTP log exposed credential")
		}
	}
}
