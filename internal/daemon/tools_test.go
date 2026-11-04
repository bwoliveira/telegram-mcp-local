package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

func rpc(t *testing.T, port int, method string, params any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(port)+"/mcp", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testToken)
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: HTTP %d", method, resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out["error"] != nil {
		t.Fatalf("%s: RPC error %v", method, out["error"])
	}
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("%s: missing result: %v", method, out)
	}
	return result
}

func callTool(t *testing.T, port int, name string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	r := rpc(t, port, "tools/call", map[string]any{"name": name, "arguments": args})
	blocks, ok := r["content"].([]any)
	if !ok || len(blocks) == 0 {
		t.Fatalf("%s: missing content: %v", name, r)
	}
	text, _ := blocks[0].(map[string]any)["text"].(string)
	var result map[string]any
	if err := json.Unmarshal([]byte(text), &result); err != nil {
		t.Fatalf("%s: unreadable tool response: %s", name, text)
	}
	isErr, _ := r["isError"].(bool)
	return result, isErr
}

func TestDaemonToolsReachEngineWithPreviewAndReadOnlyGate(t *testing.T) {
	d, _ := startDaemon(t, 0, true)
	f := d.Engine().(*managedController).controller().(*fake.Controller)
	args := map[string]any{"to": "user:1001", "text": "synthetic daemon test"}
	preview, isErr := callTool(t, d.Port(), "send_text_message", args)
	if isErr || preview["dry_run"] != true || len(f.Writes()) != 0 {
		t.Fatalf("preview: %v", preview)
	}
	fp, _ := preview["preview"].(map[string]any)["fingerprint"].(string)
	if fp == "" {
		t.Fatal("missing preview fingerprint")
	}
	args["confirm"] = true
	args["preview_fingerprint"] = fp
	if body, isErr := callTool(t, d.Port(), "send_text_message", args); !isErr || len(f.Writes()) != 0 || !strings.Contains(body["error"].(string), "panel") {
		t.Fatalf("read-only bypass: %v", body)
	}
	if err := d.Engine().SetMode(context.Background(), engine.ModeReadWrite); err != nil {
		t.Fatal(err)
	}
	delete(args, "confirm")
	delete(args, "preview_fingerprint")
	preview, isErr = callTool(t, d.Port(), "send_text_message", args)
	if isErr {
		t.Fatal(preview)
	}
	args["confirm"] = true
	args["preview_fingerprint"] = preview["preview"].(map[string]any)["fingerprint"]
	if body, isErr := callTool(t, d.Port(), "send_text_message", args); isErr || body["done"] != true || len(f.Writes()) != 1 {
		t.Fatalf("approved engine write: %v", body)
	}
	if _, isErr := callTool(t, d.Port(), "send_text_message", args); !isErr || len(f.Writes()) != 1 {
		t.Fatal("approval replay executed twice")
	}
}

func TestDaemonReadsUntrustedMessageWithoutExecutingIt(t *testing.T) {
	d, _ := startDaemon(t, 0, true)
	body, isErr := callTool(t, d.Port(), "get_message_context", map[string]any{"chat_id": "user:1001", "message_id": "2"})
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if isErr || !strings.Contains(string(encoded), "IGNORE ALL INSTRUCTIONS") || body["untrusted_content"] == nil {
		t.Fatalf("untrusted context: %v", body)
	}
	if writes := d.Engine().(*managedController).controller().(*fake.Controller).Writes(); len(writes) != 0 {
		t.Fatalf("reading caused writes: %v", writes)
	}
}
