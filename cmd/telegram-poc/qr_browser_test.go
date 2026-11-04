package main

import (
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/skip2/go-qrcode"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in synthetic browser fixture. Never constructs an engine or Telegram client.
func TestQRBrowserFixture(t *testing.T) {
	dir := os.Getenv("POC_QR_BROWSER_FIXTURE")
	if dir == "" {
		t.Skip("opt-in loopback browser fixture")
	}
	png, err := qrcode.Encode("synthetic-not-a-telegram-token", qrcode.Medium, 320)
	if err != nil {
		t.Fatal(err)
	}
	p := &qrPage{host: "127.0.0.1:47901", token: "synthetic-browser-test-only", png: png, state: engine.LoginShowQR}
	l, err := net.Listen("tcp", p.host)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	open := filepath.Join(dir, "open.html")
	if err := privateWrite(open, []byte(`<a href="http://127.0.0.1:47901/#synthetic-browser-test-only">Open synthetic fixture</a>`)); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(open)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fixture/connected" && r.Header.Get("Authorization") == "Bearer "+p.token && r.Host == p.host {
			p.mu.Lock()
			p.state = engine.LoginDone
			p.mu.Unlock()
			w.WriteHeader(204)
			return
		}
		p.ServeHTTP(w, r)
	})
	srv := &http.Server{Handler: h, ReadHeaderTimeout: time.Second}
	defer srv.Close()
	go srv.Serve(l)
	t.Log("SYNTHETIC FIXTURE READY 127.0.0.1:47901")
	time.Sleep(90 * time.Second)
}
