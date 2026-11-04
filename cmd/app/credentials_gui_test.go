package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// TestCredentialSetupGUI is an opt-in native WebKit fixture. It can only use
// fakeengine and a private data folder; normal test runs never open a window.
func TestCredentialSetupGUI(t *testing.T) {
	if os.Getenv("TELEGRAM_MCP_CREDENTIAL_GUI") != "1" {
		t.Skip("opt-in native fixture")
	}
	dir := os.Getenv("TELEGRAM_MCP_DATA")
	if os.Getenv("TELEGRAM_MCP_ENGINE") != "fake" || !strings.Contains(dir, "e4-onboarding-evidence") || os.Getenv("TELEGRAM_API_ID") != "" || os.Getenv("TELEGRAM_API_HASH") != "" {
		t.Fatal("unsafe GUI fixture environment")
	}
	a, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	if a.config().Port == 47831 {
		t.Fatal("GUI fixture must not use the installed service port")
	}
	if a.credentialsReady() {
		t.Fatal("fixture must start with an empty vault")
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A normal fakeengine starts authenticated. Reset only this fixture to
		// demonstrate the next screen without ever contacting Telegram.
		if r.Method == "POST" && r.URL.Path == "/api/credentials" {
			out := httptest.NewRecorder()
			a.handler.ServeHTTP(out, r)
			if d := a.current(); d != nil {
				_ = d.Engine().LogOut(context.Background())
			}
			for k, values := range out.Header() {
				for _, v := range values {
					w.Header().Add(k, v)
				}
			}
			w.WriteHeader(out.Code)
			_, _ = w.Write(out.Body.Bytes())
			return
		}
		a.handler.ServeHTTP(w, r)
	})
	a.wails = application.New(application.Options{Name: "Telegram MCP Credentials QA", Assets: application.AssetOptions{Handler: handler, DisableLogging: true}, OnShutdown: a.shutdown})
	a.wails.Window.NewWithOptions(application.WebviewWindowOptions{Title: "Telegram MCP Credentials QA", Width: 1000, Height: 760, URL: "/"})
	if err := a.wails.Run(); err != nil {
		t.Fatal(err)
	}
}
