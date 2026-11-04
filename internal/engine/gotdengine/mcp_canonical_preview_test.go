package gotdengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// These tests intentionally use the real gotd Engine, not an embedded fake or
// a request-capturing Engine implementation. Fixtures populate only a temporary
// store; any attempted Telegram RPC (including reads/uploads) fails the test.
func canonicalMCPFixture(t *testing.T) (*mcp.Server, *Engine, engine.MessageKey, string, []byte) {
	t.Helper()
	e, key := actionFixture(t)
	now := time.Date(2026, time.November, 3, 12, 0, 0, 0, time.UTC)
	e.cfg.Clock = func() time.Time { return now }
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
		calls++
		t.Fatalf("offline MCP preview attempted Telegram RPC %T", in)
		return fmt.Errorf("offline preview forbids RPC %T", in)
	}))
	t.Cleanup(func() {
		if calls != 0 {
			t.Errorf("offline preview invoked %d Telegram RPCs; want zero", calls)
		}
	})
	root := t.TempDir()
	path := filepath.Join(root, "synthetic.txt")
	data := []byte("R3 real gotdengine canonical preview; no Telegram credentials\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	server := mcp.New(mcp.Config{Engine: e, MediaDir: filepath.Join(root, "media"), Clock: e.cfg.Clock})
	return server, e, key, path, data
}

func canonicalMCPCall(t *testing.T, s *mcp.Server, tool string, args map[string]any) (map[string]any, bool) {
	t.Helper()
	request, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw := s.Handle(context.Background(), request)
	var reply struct {
		Error  json.RawMessage `json:"error"`
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatalf("decode %s JSON-RPC response: %v; %s", tool, err, raw)
	}
	if len(reply.Error) != 0 || len(reply.Result.Content) != 1 || reply.Result.Content[0].Type != "text" {
		t.Fatalf("unexpected %s MCP envelope: %s", tool, raw)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &body); err != nil {
		t.Fatalf("decode %s MCP result: %v; %s", tool, err, raw)
	}
	return body, reply.Result.IsError
}

func canonicalMCPPreview(t *testing.T, s *mcp.Server, tool string, args map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	body, failed := canonicalMCPCall(t, s, tool, args)
	if failed || body["dry_run"] != true || body["done"] != false {
		t.Fatalf("%s did not return an unexecuted preview: error=%v body=%v", tool, failed, body)
	}
	preview, ok := body["preview"].(map[string]any)
	if !ok {
		t.Fatalf("%s missing preview: %v", tool, body)
	}
	fingerprint, ok := preview["fingerprint"].(string)
	if !ok {
		t.Fatalf("%s missing preview fingerprint: %v", tool, preview)
	}
	if digest, err := hex.DecodeString(fingerprint); err != nil || len(digest) != sha256.Size {
		t.Errorf("%s fingerprint is not SHA-256: %q", tool, fingerprint)
	}
	parameters, ok := preview["parameters"].(map[string]any)
	if !ok || len(parameters) == 0 {
		t.Fatalf("%s missing canonical preview.parameters from real gotdengine: %v", tool, preview)
	}
	// Confirmation and local staging metadata must never masquerade as action
	// parameters. Identity is the resolved account/peer, not an input alias.
	for _, forbidden := range []string{"confirm", "confirm_revoke", "dry_run", "preview_fingerprint", "url", "path"} {
		if _, exists := parameters[forbidden]; exists {
			t.Errorf("%s canonical parameters leaked transport-only field %q: %v", tool, forbidden, parameters)
		}
	}
	return preview, parameters
}

func canonicalMCPWant(t *testing.T, got map[string]any, want map[string]any) {
	t.Helper()
	// Normalize expected DTOs through their real JSON contracts too (peer IDs
	// and complete message keys must not be approximated by bare message IDs).
	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var normalized map[string]any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatal(err)
	}
	for field, value := range normalized {
		actual, exists := got[field]
		if !exists || !reflect.DeepEqual(actual, value) {
			t.Errorf("preview.parameters.%s = %#v (present=%v), want %#v; parameters=%v", field, actual, exists, value, got)
		}
	}
}

func TestMCPCanonicalPreviewRealGotdCatalog(t *testing.T) {
	user := engine.PeerRef{Type: engine.PeerUser, ID: 10}
	group := engine.PeerRef{Type: engine.PeerChat, ID: 20}
	forum := engine.PeerRef{Type: engine.PeerChannel, ID: 30}
	key := engine.MessageKey{Account: "primary", Peer: user, ID: 71}
	until := time.Date(2026, time.November, 4, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		tool   string
		action string
		peer   engine.PeerRef
		args   map[string]any
		want   map[string]any
	}{
		{"send_text_message", "send_text", user,
			map[string]any{"to": "user:10", "text": "canonical text", "reply_to": "71"},
			map[string]any{"text": "canonical text", "reply_to": 71, "topic_id": 0}},
		{"send_media_message", "send_media", user,
			map[string]any{"to": "user:10", "type": "document", "filename": "approved.txt", "caption": "canonical caption", "reply_to": "71"},
			map[string]any{"type": "document", "caption": "canonical caption", "filename": "approved.txt", "reply_to": 71, "topic_id": 0}},
		{"send_location", "send_location", user,
			map[string]any{"to": "user:10", "latitude": -23.5, "longitude": -46.5, "name": "Fixture venue", "address": "Synthetic address"},
			map[string]any{"location": engine.Location{Latitude: -23.5, Longitude: -46.5, Title: "Fixture venue", Address: "Synthetic address"}}},
		{"send_poll", "send_poll", group,
			map[string]any{"to": "chat:20", "question": "Canonical choice?", "options": []string{"First", "Second", "Third"}, "multiple_choice": true},
			map[string]any{"question": "Canonical choice?", "options": []string{"First", "Second", "Third"}, "multiple_choice": true, "anonymous": true, "quiz": false}},
		{"edit_message", "edit", user,
			map[string]any{"chat_id": "user:10", "message_id": "71", "text": "canonical edit"},
			map[string]any{"key": key, "text": "canonical edit"}},
		{"delete_message", "delete", user,
			map[string]any{"chat_id": "user:10", "message_id": "71", "for_me": true},
			map[string]any{"key": key, "ids": []int{71}, "revoke": false}},
		{"react_to_message", "react", user,
			map[string]any{"chat_id": "user:10", "message_id": "71", "emoji": "🔥"},
			map[string]any{"key": key, "emoji": "🔥"}},
		{"forward_message", "forward", forum,
			map[string]any{"chat_id": "user:10", "message_id": "71", "to": "channel:30", "topic_id": 60},
			map[string]any{"key": key, "from": user, "to": forum, "ids": []int{71}, "topic_id": 60}},
		{"mark_chat_read", "mark_read", user,
			map[string]any{"chat_id": "user:10", "max_id": 71, "receipts": true},
			map[string]any{"max_id": 71, "receipts": true}},
		{"send_typing", "typing", forum,
			map[string]any{"to": "channel:30", "audio": true, "topic_id": 60},
			map[string]any{"action": "record_audio", "topic_id": 60}},
		{"organise_chat", "organise", user,
			map[string]any{"chat_id": "user:10", "action": "mute", "until": until.Format(time.RFC3339)},
			map[string]any{"action": "mute", "mute_until": until}},
		{"manage_group_participants", "participants", group,
			map[string]any{"group_id": "chat:20", "action": "add", "participants": []string{"user:10", "user:11"}},
			map[string]any{"group": group, "action": "add", "users": []engine.PeerRef{user, {Type: engine.PeerUser, ID: 11}}}},
		{"update_group", "update_group", group,
			map[string]any{"group_id": "chat:20", "name": "Canonical title", "description": "Canonical about"},
			map[string]any{"group": group, "title": "Canonical title", "about": "Canonical about"}},
		{"get_group_invite_link", "invite_link", group,
			map[string]any{"group_id": "chat:20", "reset": true},
			map[string]any{"group": group, "revoke": true}},
		{"leave_group", "leave", group,
			map[string]any{"group_id": "chat:20"},
			map[string]any{"group": group}},
	}
	if len(cases) != 15 {
		t.Fatalf("canonical write catalog contains %d tools; want 15", len(cases))
	}
	seen := make(map[string]bool)
	for _, tc := range cases {
		if seen[tc.tool] {
			t.Fatalf("duplicate catalog entry %s", tc.tool)
		}
		seen[tc.tool] = true
		t.Run(tc.tool, func(t *testing.T) {
			s, _, _, path, data := canonicalMCPFixture(t)
			if tc.tool == "send_media_message" {
				tc.args["url"] = path
				digest := sha256.Sum256(data)
				tc.want["sha256"] = hex.EncodeToString(digest[:])
				tc.want["size"] = len(data)
			}
			// Omitted confirm/dry_run must still produce a real engine preview.
			preview, parameters := canonicalMCPPreview(t, s, tc.tool, tc.args)
			if preview["action"] != tc.action {
				t.Errorf("preview.action = %v, want %s", preview["action"], tc.action)
			}
			canonicalMCPWant(t, parameters, map[string]any{"account": "primary", "peer": tc.peer})
			canonicalMCPWant(t, parameters, tc.want)
		})
	}
}

func TestMCPCanonicalPreviewRealGotdEffectiveDefaults(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
		want map[string]any
	}{
		{"typing_default", "send_typing", map[string]any{"to": "channel:30"}, map[string]any{"action": "typing", "topic_id": 0}},
		{"typing_cancel_overrides_audio", "send_typing", map[string]any{"to": "channel:30", "typing": false, "audio": true, "topic_id": 60}, map[string]any{"action": "cancel", "topic_id": 60}},
		{"poll_single_answer", "send_poll", map[string]any{"to": "chat:20", "question": "Single?", "options": []string{"Yes", "No"}}, map[string]any{"question": "Single?", "options": []string{"Yes", "No"}, "multiple_choice": false, "anonymous": true, "quiz": false}},
		{"mark_read_all_still_sends_receipts", "mark_chat_read", map[string]any{"chat_id": "user:10"}, map[string]any{"max_id": 0, "receipts": true}},
		{"reaction_default", "react_to_message", map[string]any{"chat_id": "user:10", "message_id": "71"}, map[string]any{"emoji": "👍"}},
		{"reaction_remove", "react_to_message", map[string]any{"chat_id": "user:10", "message_id": "71", "emoji": ""}, map[string]any{"emoji": ""}},
		{"delete_default_revokes", "delete_message", map[string]any{"chat_id": "user:10", "message_id": "71"}, map[string]any{"revoke": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _, _ := canonicalMCPFixture(t)
			_, parameters := canonicalMCPPreview(t, s, tc.tool, tc.args)
			canonicalMCPWant(t, parameters, tc.want)
		})
	}
	t.Run("receipts_false_rejected_without_RPC", func(t *testing.T) {
		s, _, _, _, _ := canonicalMCPFixture(t)
		body, failed := canonicalMCPCall(t, s, "mark_chat_read", map[string]any{"chat_id": "user:10", "receipts": false})
		if !failed || body["error"] == nil || body["preview"] != nil {
			t.Fatalf("receipts:false must fail rather than preview a fictitious no-receipt write: %v", body)
		}
	})
}

func TestMCPCanonicalPreviewRealGotdMediaEffectiveFilenameAndBytes(t *testing.T) {
	for _, advertised := range []string{"image", "photo", "video", "audio", "voice", "document", "animation"} {
		t.Run(advertised, func(t *testing.T) {
			s, _, _, path, data := canonicalMCPFixture(t)
			args := map[string]any{"to": "user:10", "type": advertised, "url": path, "caption": "exact caption", "reply_to": "71", "dry_run": true}
			preview, parameters := canonicalMCPPreview(t, s, "send_media_message", args)
			digest := sha256.Sum256(data)
			hash := hex.EncodeToString(digest[:])
			kind := advertised
			if kind == "image" {
				kind = "photo"
			}
			canonicalMCPWant(t, parameters, map[string]any{
				"account": "primary", "peer": engine.PeerRef{Type: engine.PeerUser, ID: 10},
				"type": kind, "caption": "exact caption", "filename": hash + filepath.Ext(path),
				"sha256": hash, "size": len(data), "reply_to": 71, "topic_id": 0,
			})
			// The fallback must name the file actually passed to gotd, not the
			// caller's pre-staging basename or a made-up original filename.
			staged := filepath.Join(filepath.Dir(path), "media", "send-staging", hash+filepath.Ext(path))
			stagedBytes, err := os.ReadFile(staged)
			if err != nil || !reflect.DeepEqual(stagedBytes, data) {
				t.Fatalf("preview does not describe the real staged bytes: err=%v bytes=%q", err, stagedBytes)
			}
			args["filename"] = "explicit.txt"
			renamedPreview, renamed := canonicalMCPPreview(t, s, "send_media_message", args)
			canonicalMCPWant(t, renamed, map[string]any{"filename": "explicit.txt", "sha256": hash, "size": len(data)})
			if renamedPreview["fingerprint"] == preview["fingerprint"] {
				t.Error("effective filename change did not change the real gotd fingerprint")
			}
		})
	}
}

func TestMCPCanonicalPreviewRealGotdChangedIntentCannotConfirm(t *testing.T) {
	cases := []struct {
		name    string
		tool    string
		args    map[string]any
		changed map[string]any
	}{
		{"typing_action", "send_typing", map[string]any{"to": "channel:30", "topic_id": 60}, map[string]any{"audio": true}},
		{"typing_topic", "send_typing", map[string]any{"to": "channel:30", "topic_id": 60}, map[string]any{"topic_id": 61}},
		{"organise_action", "organise_chat", map[string]any{"chat_id": "user:10", "action": "archive"}, map[string]any{"action": "pin"}},
		{"organise_until", "organise_chat", map[string]any{"chat_id": "user:10", "action": "mute", "until": "2026-11-04T12:00:00Z"}, map[string]any{"until": "2026-11-05T12:00:00Z"}},
		{"poll_options", "send_poll", map[string]any{"to": "chat:20", "question": "Q?", "options": []string{"A", "B"}}, map[string]any{"options": []string{"A", "C"}}},
		{"poll_multiple", "send_poll", map[string]any{"to": "chat:20", "question": "Q?", "options": []string{"A", "B"}}, map[string]any{"multiple_choice": true}},
		{"read_boundary", "mark_chat_read", map[string]any{"chat_id": "user:10", "max_id": 71}, map[string]any{"max_id": 72}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _, _ := canonicalMCPFixture(t)
			before, original := canonicalMCPPreview(t, s, tc.tool, tc.args)
			changed := make(map[string]any, len(tc.args)+len(tc.changed)+2)
			for field, value := range tc.args {
				changed[field] = value
			}
			for field, value := range tc.changed {
				changed[field] = value
			}
			after, updated := canonicalMCPPreview(t, s, tc.tool, changed)
			if reflect.DeepEqual(original, updated) || before["fingerprint"] == after["fingerprint"] {
				t.Fatalf("changed effective intent kept canonical parameters or fingerprint: before=%v after=%v", before, after)
			}
			changed["confirm"] = true
			changed["preview_fingerprint"] = before["fingerprint"]
			body, failed := canonicalMCPCall(t, s, tc.tool, changed)
			if !failed || body["done"] == true {
				t.Fatalf("changed canonical intent accepted the old approval: %v", body)
			}
		})
	}
}
