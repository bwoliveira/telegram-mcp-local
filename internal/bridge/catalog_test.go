package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type catalogLines chan []byte

func (c catalogLines) Write(p []byte) (int, error) {
	c <- append([]byte(nil), p...)
	return len(p), nil
}

// Model two daemon binaries with the same release version and tool names but
// changed descriptions/schemas. The stdio client keeps running through restarts,
// sends only initialize + ping, and never asks for tools/list to discover change.
func TestRunNotifiesCatalogChangeOnPingAfterDaemonRestart(t *testing.T) {
	var listed atomic.Int32
	newDaemon := func(description, address string) *httptest.Server {
		tools := []any{map[string]any{"name": "health", "description": description, "inputSchema": map[string]any{"type": "object"}}}
		canonical, _ := json.Marshal(tools)
		version := fmt.Sprintf("sha256:%x", sha256.Sum256(canonical))
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-catalog-token" {
				t.Error("missing bearer")
			}
			if r.Header.Get("X-MCP-Client") != "bridge:catalog-test" {
				t.Errorf("hint = %s", r.Header.Get("X-MCP-Client"))
			}
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
				return
			}
			result := map[string]any{"_meta": map[string]any{"catalogVersion": version}}
			if req.Method == "initialize" {
				result["capabilities"] = map[string]any{"tools": map[string]any{"listChanged": true}}
				result["serverInfo"] = map[string]any{"name": "telegram-mcp-local", "version": "same-release"}
			}
			if req.Method == "tools/list" {
				listed.Add(1)
				result["tools"] = tools
				result["ttlMs"] = 600000
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}))
		// Rebind the exact address to model an app upgrade, not a URL/config
		// change. NewUnstartedServer's original unused listener is closed first.
		_ = server.Listener.Close()
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		server.Listener = listener
		server.Start()
		return server
	}
	old := newDaemon("old description", "127.0.0.1:0")
	defer old.Close()
	address := old.Listener.Addr().String()
	input, send := io.Pipe()
	defer input.Close()
	defer send.Close()
	lines := make(catalogLines, 32)
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		done <- Run(ctx, Options{URL: func() string { return old.URL }, Token: "test-catalog-token", NotRunning: "not open"}, input, lines)
	}()
	read := func() map[string]any {
		t.Helper()
		select {
		case line := <-lines:
			var msg map[string]any
			if err := json.Unmarshal(line, &msg); err != nil {
				t.Fatalf("invalid JSON-RPC: %s", line)
			}
			return msg
		case <-time.After(time.Second):
			t.Fatal("bridge did not emit the expected JSON-RPC message")
			return nil
		}
	}
	request := func(id int, method string) {
		t.Helper()
		_, err := fmt.Fprintf(send, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":%q,\"params\":{\"clientInfo\":{\"name\":\"catalog-test\"}}}\n", id, method)
		if err != nil {
			t.Fatal(err)
		}
	}
	request(1, "initialize")
	if msg := read(); msg["id"] != float64(1) {
		t.Fatalf("initial catalog emitted a notification: %v", msg)
	}
	old.Close()
	request(2, "ping")
	if msg := read(); msg["error"] == nil {
		t.Fatalf("stopped daemon should fail: %v", msg)
	}
	// Restart without a catalog change: no cache invalidation.
	same := newDaemon("old description", address)
	defer same.Close()
	request(3, "ping")
	if msg := read(); msg["id"] != float64(3) || msg["error"] != nil {
		t.Fatalf("unchanged restart emitted a notification/error: %v", msg)
	}
	same.Close()
	changed := newDaemon("new description", address)
	defer changed.Close()
	request(4, "ping")
	messages := []map[string]any{read(), read()}
	notifications, responses := 0, 0
	for _, msg := range messages {
		if msg["method"] == "notifications/tools/list_changed" {
			notifications++
			if _, hasID := msg["id"]; hasID {
				t.Fatalf("notification has an id: %v", msg)
			}
		} else if msg["id"] == float64(4) && msg["error"] == nil {
			responses++
		} else {
			t.Fatalf("unexpected JSON-RPC message: %v", msg)
		}
	}
	if notifications != 1 || responses != 1 {
		t.Fatalf("notifications=%d ping responses=%d", notifications, responses)
	}
	request(5, "ping")
	if msg := read(); msg["id"] != float64(5) {
		t.Fatalf("duplicate catalog change: %v", msg)
	}
	if listed.Load() != 0 {
		t.Fatalf("change required tools/list: %d requests", listed.Load())
	}
	_ = send.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge did not stop at EOF")
	}
	if len(lines) != 0 {
		t.Fatalf("unexpected extra notifications: %d", len(lines))
	}
}
