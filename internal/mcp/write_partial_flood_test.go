package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

// Preview through the synthetic engine; inject acceptance followed by a remote
// failure only at execution. Every case still crosses the real MCP dispatcher.
type partialFloodEngine struct {
	engine.Engine
	result     engine.WriteResult
	err        error
	executions int
}

func (e *partialFloodEngine) outcome() (engine.WriteResult, error) {
	e.executions++
	return e.result, e.err
}
func (e *partialFloodEngine) SendText(ctx context.Context, r engine.SendTextRequest) (engine.WriteResult, error) {
	if r.DryRun {
		return e.Engine.SendText(ctx, r)
	}
	return e.outcome()
}
func (e *partialFloodEngine) SendMedia(ctx context.Context, r engine.SendMediaRequest) (engine.WriteResult, error) {
	if r.DryRun {
		return e.Engine.SendMedia(ctx, r)
	}
	return e.outcome()
}
func (e *partialFloodEngine) Forward(ctx context.Context, r engine.ForwardRequest) (engine.WriteResult, error) {
	if r.DryRun {
		return e.Engine.Forward(ctx, r)
	}
	return e.outcome()
}
func (e *partialFloodEngine) UpdateGroup(ctx context.Context, r engine.UpdateGroupRequest) (engine.WriteResult, error) {
	if r.DryRun {
		return e.Engine.UpdateGroup(ctx, r)
	}
	return e.outcome()
}
func (e *partialFloodEngine) ManageParticipants(ctx context.Context, r engine.ParticipantsRequest) (engine.ParticipantsResult, error) {
	if r.DryRun {
		return e.Engine.ManageParticipants(ctx, r)
	}
	result, err := e.outcome()
	return engine.ParticipantsResult{WriteResult: result, PerUser: map[string]string{"user:1001": "removed but unban failed"}}, err
}

func TestDispatchPartialFloodWaitPreservesSafeRetryWarning(t *testing.T) {
	upload := filepath.Join(t.TempDir(), "synthetic.txt")
	if err := os.WriteFile(upload, []byte("synthetic document"), 0600); err != nil {
		t.Fatal(err)
	}
	until := fake.Epoch().Add(time.Minute)
	const warning = "Telegram accepted part of the write; inspect affected and per_user; do not blindly retry"
	for _, tc := range []struct {
		name, tool string
		args       map[string]any
		wrapped    bool
	}{
		{"group title accepted description flooded", "update_group", map[string]any{"group_id": "chat:1001", "name": "New title", "description": "New description"}, true},
		{"participant removed unban flooded", "manage_group_participants", map[string]any{"group_id": "channel:2001", "action": "remove", "participants": []string{"user:1001"}}, true},
		{"message accepted before flood", "send_text_message", map[string]any{"to": "me", "text": "synthetic message"}, false},
		{"media accepted before flood", "send_media_message", map[string]any{"to": "me", "type": "document", "url": upload}, false},
		{"forward accepted before next destination flood", "forward_message", map[string]any{"chat_id": "user:1001", "message_id": "1", "to": "me"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, _ := parityServer(t)
			remote := error(&engine.FloodWaitError{Until: until})
			if tc.wrapped {
				remote = fmt.Errorf("%s: %w", warning, remote)
			}
			accepted := engine.MessageKey{Account: "primary", Peer: fake.SelfPeer, ID: 22}
			e := &partialFloodEngine{Engine: f, result: engine.WriteResult{Affected: 1, Messages: []engine.MessageKey{accepted}, Note: "first step accepted"}, err: remote}
			s.eng = e
			fp := previewFingerprint(t, toolCall(t, s, tc.tool, tc.args))
			tc.args["confirm"], tc.args["preview_fingerprint"] = true, fp
			reply := toolCall(t, s, tc.tool, tc.args)
			body := toolBody(t, reply)
			t.Logf("actual MCP response: %#v", body)
			if reply["isError"] != true || body["partial"] != true || body["flood_wait_until"] != until.Format(time.RFC3339) {
				t.Fatalf("lost partial failure metadata: %+v", body)
			}
			text, _ := body["error"].(string)
			for _, want := range []string{"Telegram accepted", "do not blindly retry", "entire operation", "remaining", "flood_wait_until"} {
				if !strings.Contains(text, want) {
					t.Errorf("top-level error missing %q: %q", want, text)
				}
			}
			if tc.wrapped && !strings.Contains(text, warning) {
				t.Errorf("lost engine warning: %q", text)
			}
			result := body["result"].(map[string]any)
			if result["affected"] != float64(1) || result["note"] != "first step accepted" || len(result["messages"].([]any)) != 1 {
				t.Fatalf("lost acceptance evidence: %+v", result)
			}
			if tc.tool == "manage_group_participants" && result["per_user"].(map[string]any)["user:1001"] != "removed but unban failed" {
				t.Fatal(result)
			}
			if toolCall(t, s, tc.tool, tc.args)["isError"] != true || e.executions != 1 || len(f.Writes()) != 0 {
				t.Fatal("approval replayed or fake wrote")
			}
		})
	}
}

func TestDispatchUnacceptedFloodWaitKeepsOrdinaryRateLimit(t *testing.T) {
	s, f, _ := parityServer(t)
	until := fake.Epoch().Add(time.Minute)
	e := &partialFloodEngine{Engine: f, err: &engine.FloodWaitError{Until: until}}
	s.eng = e
	args := map[string]any{"to": "me", "text": "synthetic message"}
	fp := previewFingerprint(t, toolCall(t, s, "send_text_message", args))
	args["confirm"], args["preview_fingerprint"] = true, fp
	reply := toolCall(t, s, "send_text_message", args)
	body := toolBody(t, reply)
	if reply["isError"] != true || body["partial"] != nil || body["result"] != nil || body["error"] != "Telegram rate limit; retry after flood_wait_until" || body["flood_wait_until"] != until.Format(time.RFC3339) {
		t.Fatalf("unexpected unaccepted error: %+v", body)
	}
}
