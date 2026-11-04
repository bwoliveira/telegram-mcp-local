package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/service"
	"github.com/bwoliveira/telegram-mcp-local/internal/testutil"
)

func TestBridgeLoadsPersistedTokenWithoutEnvironmentSecret(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	t.Setenv("TELEGRAM_MCP_DATA", dir)
	t.Setenv("TELEGRAM_MCP_TOKEN", "")
	testutil.NativeSecrets(t, dir)
	token, err := service.ResolveToken(dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path != "/mcp" {
			t.Error("missing persisted token")
		}
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer s.Close()
	u, _ := url.Parse(s.URL)
	t.Setenv("TELEGRAM_MCP_PORT", u.Port())
	var out bytes.Buffer
	if err := runBridge(context.Background(), bytes.NewBufferString("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"ping\"}\n"), &out); err != nil {
		t.Fatal(err)
	}
	if called != 1 || !bytes.Contains(out.Bytes(), []byte(`"result":{}`)) {
		t.Fatalf("calls=%d response=%s", called, &out)
	}
	if bytes.Contains(out.Bytes(), []byte(token)) {
		t.Fatal("bridge exposed token")
	}
}

func TestBridgeDoesNotInventCredential(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	t.Setenv("TELEGRAM_MCP_DATA", dir)
	t.Setenv("TELEGRAM_MCP_TOKEN", "")
	testutil.NativeSecrets(t, dir)
	t.Setenv("TELEGRAM_MCP_PORT", "")
	var out bytes.Buffer
	if err := runBridge(context.Background(), bytes.NewBuffer(nil), &out); err == nil {
		t.Fatal("bridge accepted absent credential")
	}
	if _, err := service.ResolveToken(dir, "", false); err == nil {
		t.Fatal("bridge generated a new credential")
	}
}
