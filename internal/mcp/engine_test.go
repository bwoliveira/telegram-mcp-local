package mcp

import (
	"context"
	"encoding/json"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
	"time"
)

// Only the engine methods exercised here are overridden. Calling another
// method on this deliberately narrow double fails rather than faking success.
type toolEngine struct {
	engine.Engine
	mode     engine.Mode
	requests []engine.SendTextRequest
	resolved int
}

func (e *toolEngine) Mode() engine.Mode { return e.mode }
func (e *toolEngine) Account(context.Context) (engine.Account, error) {
	return engine.Account{ID: "test"}, nil
}
func (e *toolEngine) Resolve(context.Context, engine.ResolveRequest) ([]engine.Candidate, error) {
	e.resolved++
	return []engine.Candidate{{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 1}}, Exact: true}}, nil
}
func (e *toolEngine) SendText(_ context.Context, r engine.SendTextRequest) (engine.WriteResult, error) {
	e.requests = append(e.requests, r)
	if r.DryRun {
		return engine.WriteResult{DryRun: true, Preview: &engine.Preview{Action: "send_text", Fingerprint: "test-fingerprint", Target: engine.Peer{Ref: r.Peer}}}, nil
	}
	if e.mode != engine.ModeReadWrite {
		return engine.WriteResult{}, engine.ErrReadOnly
	}
	return engine.WriteResult{Done: true}, nil
}
func toolCall(t *testing.T, s *Server, name string, a map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": a}})
	var reply struct {
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(s.Handle(context.Background(), b), &reply); err != nil {
		t.Fatal(err)
	}
	return reply.Result
}
func toolBody(t *testing.T, r map[string]any) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal([]byte(r["content"].([]any)[0].(map[string]any)["text"].(string)), &body); err != nil {
		t.Fatal(err)
	}
	return body
}
func TestWritePreviewApproval(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	e := &toolEngine{mode: engine.ModeReadWrite}
	s := New(Config{Engine: e, Clock: func() time.Time { return now }})
	a := map[string]any{"to": "user:1", "text": "hello"}
	r := toolCall(t, s, "send_text_message", a)
	if r["isError"] != false || len(e.requests) != 1 || !e.requests[0].DryRun {
		t.Fatalf("default must preview: %v, %v", r, e.requests)
	}
	a["confirm"] = true
	if toolCall(t, s, "send_text_message", a)["isError"] != true || len(e.requests) != 1 {
		t.Fatal("confirmation without fingerprint reached engine")
	}
	a["preview_fingerprint"] = "test-fingerprint"
	a["text"] = "changed"
	if toolCall(t, s, "send_text_message", a)["isError"] != true || len(e.requests) != 1 {
		t.Fatal("changed request reached engine")
	}
	a["text"] = "hello"
	if toolCall(t, s, "send_text_message", a)["isError"] != false || len(e.requests) != 2 || e.requests[1].DryRun {
		t.Fatal("matching approval did not execute")
	}
	if toolCall(t, s, "send_text_message", a)["isError"] != true || len(e.requests) != 2 {
		t.Fatal("replayed approval reached engine")
	}
	delete(a, "confirm")
	delete(a, "preview_fingerprint")
	toolCall(t, s, "send_text_message", a)
	a["confirm"] = true
	a["preview_fingerprint"] = "test-fingerprint"
	now = now.Add(11 * time.Minute)
	if toolCall(t, s, "send_text_message", a)["isError"] != true || len(e.requests) != 3 {
		t.Fatal("expired approval reached engine")
	}
	a["dry_run"] = true
	if toolCall(t, s, "send_text_message", a)["isError"] != false || !e.requests[3].DryRun {
		t.Fatal("dry_run must win over confirm")
	}
}
func TestTypedMessageIdentity(t *testing.T) {
	for _, a := range []arguments{{MessageID: "7"}, {ChatJID: "123@example.invalid", MessageID: "7"}, {ChatJID: "channel:1", MessageID: "channel:2/7"}, {ChatJID: "user:1", MessageID: "-1"}, {ChatJID: "user:01", MessageID: "7"}} {
		if _, err := parseMessageKey(a, "test"); err == nil {
			t.Errorf("accepted invalid identity: %+v", a)
		}
	}
	key, err := parseMessageKey(arguments{MessageID: "channel:123/456"}, "test")
	if err != nil || key.Peer.Type != engine.PeerChannel || key.Peer.ID != 123 || key.ID != 456 {
		t.Fatalf("key=%+v err=%v", key, err)
	}
}
func TestPrivacyMetadataSurvivesCompactContext(t *testing.T) {
	expiry := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	m := engineMessage(engine.Message{Key: engine.MessageKey{Account: "test", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1}, ID: 7}, Text: "temporary", Protected: true, ExpiresAt: &expiry, TTLPeriod: 60})
	sh, err := newShaping(arguments{Fields: []string{"text"}}, messageFields)
	if err != nil {
		t.Fatal(err)
	}
	row := sh.message(m).(map[string]any)
	if row["text"] != "temporary" || row["protected"] != true || row["ephemeral"] != true || row["expires_at"] == nil {
		t.Fatalf("compact response lost privacy metadata: %v", row)
	}
}
