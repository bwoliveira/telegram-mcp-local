package main

import (
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestQRPageGuardsTokenHostOriginAndMethod(t *testing.T) {
	p := &qrPage{host: "127.0.0.1:47900", token: "synthetic-test-token", png: []byte("synthetic-png"), state: engine.LoginShowQR}
	tests := []struct {
		name, method, path, host, origin, token string
		want                                    int
	}{
		{"no token", "GET", "/qr.png", p.host, "", "", 401}, {"wrong token", "GET", "/status", p.host, "", "bad", 401}, {"rebind", "GET", "/status", "attacker.invalid", "", p.token, 403}, {"cross origin", "GET", "/status", p.host, "https://attacker.invalid", p.token, 403}, {"write", "POST", "/status", p.host, "", p.token, 403}, {"valid", "GET", "/qr.png", p.host, "http://" + p.host, p.token, 200}, {"status", "GET", "/status", p.host, "", p.token, 200}, {"static no token", "GET", "/", p.host, "", "", 200}, {"file bootstrap", "GET", "/", p.host, "null", "", 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.token != "" {
				r.Header.Set("Authorization", "Bearer "+tc.token)
			}
			w := httptest.NewRecorder()
			p.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatal(w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("cacheable QR")
			}
			if tc.path == "/" && strings.Contains(w.Body.String(), p.token) {
				t.Fatal("static HTML leaked token")
			}
		})
	}
}
func TestHelpAndStatusNeverRequireCredentialsOrNetwork(t *testing.T) {
	t.Setenv("TELEGRAM_API_ID", "")
	t.Setenv("TELEGRAM_API_HASH", "")
	if err := run([]string{"help"}); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"status", "--state", t.TempDir() + "/state"}); err != nil {
		t.Fatal(err)
	}
}
