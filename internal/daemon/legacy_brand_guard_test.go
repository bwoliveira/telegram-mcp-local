// Legacy literals are isolated here to verify that inherited branding and APIs stay absent.
package daemon

import (
	"encoding/json"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonServesAndStopsEngine(t *testing.T) {
	d, store := startDaemon(t, 0, true)
	port := d.Port()
	if port == 0 {
		t.Fatal("Port(0) must publish the actual listener port")
	}
	waitSync(t, d, engine.StateConnected)
	if code, body, err := get(port, "/healthz"); err != nil || code != 200 || strings.TrimSpace(body) != "ok" {
		t.Fatalf("/healthz = %d %q %v", code, body, err)
	}
	code, body, err := get(port, "/health")
	var health struct {
		Checks []struct{ Name, Status string }
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(body), &health); err != nil || len(health.Checks) == 0 {
		t.Fatalf("/health answered %d %q", code, body)
	}
	if st := d.Status(); st.Tone != "ok" || st.Title != "Conectado" {
		t.Fatalf("status of a connected gateway: %+v", st)
	}
	if _, err := os.Stat(filepath.Join(store, "wacli.db")); !os.IsNotExist(err) {
		t.Fatalf("legacy database must not be opened or created: %v", err)
	}
	d.Stop()
	if got := d.Engine().Status().State; got != engine.StateStopped {
		t.Fatalf("engine outlived daemon: %s", got)
	}
	if _, _, err := get(port, "/healthz"); err == nil {
		t.Fatal("MCP still answers after Stop")
	}
}

func TestAuthenticatedMCPInitializeAndListsFortyTelegramTools(t *testing.T) {
	d, _ := startDaemon(t, 0, true)
	init := rpc(t, d.Port(), "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "daemon-test", "version": "test"}})
	if init["serverInfo"] == nil || init["capabilities"] == nil {
		t.Fatalf("initialize: %v", init)
	}
	result := rpc(t, d.Port(), "tools/list", map[string]any{})
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) != 40 {
		t.Fatalf("want 40 tools: %v", result)
	}
	names := make(map[string]bool)
	for _, item := range tools {
		tool := item.(map[string]any)
		name, _ := tool["name"].(string)
		if name == "" || names[name] {
			t.Fatalf("empty or duplicate tool name: %q", name)
		}
		if strings.Contains(name, "whatsapp") || name == "check_numbers" {
			t.Fatalf("legacy tool name: %s", name)
		}
		names[name] = true
	}
	for _, name := range []string{"telegram_status", "resolve_contact", "send_text_message", "mark_chat_read", "get_message_context", "health"} {
		if !names[name] {
			t.Errorf("missing tool %s", name)
		}
	}
	req, err := http.NewRequest("POST", d.MCPURL(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated MCP answered %d", resp.StatusCode)
	}
	// The bearer remains valid for MCP, but must not authenticate a human panel.
	if code, body, err := get(d.Port(), "/"); err != nil || code != http.StatusForbidden || !strings.Contains(body, "panel session required") {
		t.Fatalf("bearer panel boundary: %d %.200s %v", code, body, err)
	}
}

func TestDaemonLocalReadsUseTelegramStore(t *testing.T) {
	d, _ := startDaemon(t, 0, true)
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{"list_chats", nil}, {"get_chat_messages", map[string]any{"chat_id": "user:1001"}}, {"search_messages", map[string]any{"query": "IGNORE"}}, {"get_group", map[string]any{"group_id": "chat:1001"}}, {"list_unread", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, isError := callTool(t, d.Port(), tc.name, tc.args)
			if isError {
				t.Fatalf("local read unavailable: %v", result)
			}
			if strings.Contains(strings.ToLower(resultString(result)), "pair whatsapp") {
				t.Fatal("legacy instructions")
			}
		})
	}
}
