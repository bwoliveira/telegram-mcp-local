package gotdengine

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func callDeleteMCP(t *testing.T, server *mcp.Server, arguments map[string]any) (bool, map[string]any) {
	t.Helper()
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "delete_message",
			"arguments": arguments,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := server.Handle(context.Background(), request)
	var reply struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	if len(reply.Result.Content) != 1 {
		t.Fatalf("unexpected MCP response: %s", raw)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &body); err != nil {
		t.Fatal(err)
	}
	return reply.Result.IsError, body
}

func TestMCPDeleteRevokeRequiresExplicitConfirmationWithProductionEngine(t *testing.T) {
	e, _ := actionFixture(t)
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		request, ok := in.(*tg.MessagesDeleteMessagesRequest)
		if !ok || !request.Revoke {
			t.Fatalf("unexpected delete RPC: %#v", in)
		}
		*out.(*tg.MessagesAffectedMessages) = tg.MessagesAffectedMessages{Pts: 1, PtsCount: 1}
		return nil
	}))
	server := mcp.New(mcp.Config{Engine: e})
	args := map[string]any{"chat_id": "user:10", "message_id": "71"}
	failed, preview := callDeleteMCP(t, server, args)
	if failed {
		t.Fatalf("preview failed: %v", preview)
	}
	fingerprint := preview["preview"].(map[string]any)["fingerprint"].(string)
	args["confirm"] = true
	args["preview_fingerprint"] = fingerprint
	if failed, body := callDeleteMCP(t, server, args); !failed || calls != 0 {
		t.Fatalf("generic confirmation revoked message: failed=%v body=%v calls=%d", failed, body, calls)
	}
	delete(args, "confirm")
	delete(args, "preview_fingerprint")
	failed, preview = callDeleteMCP(t, server, args)
	if failed {
		t.Fatalf("second preview failed: %v", preview)
	}
	args["confirm"] = true
	args["preview_fingerprint"] = preview["preview"].(map[string]any)["fingerprint"].(string)
	args["confirm_revoke"] = true
	if failed, body := callDeleteMCP(t, server, args); failed || calls != 1 || body["done"] != true {
		t.Fatalf("explicit revoke confirmation failed: failed=%v body=%v calls=%d", failed, body, calls)
	}
}
