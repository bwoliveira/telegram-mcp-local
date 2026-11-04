package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Compare human-visible content, never opaque hashes. Each case mutates just
// one effective material parameter (or one coupled input selecting an action).
func TestCanonicalPreviewEveryMaterialParameter(t *testing.T) {
	media := filepath.Join(t.TempDir(), "upload.txt")
	other := filepath.Join(t.TempDir(), "upload.txt")
	for path, data := range map[string]string{media: "first bytes", other: "different bytes"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		base    map[string]any
		changes map[string]map[string]any
	}{
		{"send_text_message", map[string]any{"to": "channel:2001", "text": "hello"}, map[string]map[string]any{
			"to": {"to": "user:1001"}, "text": {"text": "changed"}, "reply_to": {"reply_to": "1"}, "topic_id": {"topic_id": 1}, "mentions": {"mentions": []string{"user:1001"}}, "idempotency_key": {"idempotency_key": "another send"},
		}},
		{"send_media_message", map[string]any{"to": "channel:2001", "type": "document", "url": media}, map[string]map[string]any{
			"to": {"to": "user:1001"}, "type": {"type": "audio"}, "bytes": {"url": other}, "caption": {"caption": "changed"}, "filename": {"filename": "renamed.txt"}, "reply_to": {"reply_to": "1"}, "topic_id": {"topic_id": 1}, "idempotency_key": {"idempotency_key": "another upload"},
		}},
		{"send_location", map[string]any{"to": "user:1001", "latitude": 0, "longitude": 0}, map[string]map[string]any{
			"to": {"to": "user:1000"}, "latitude": {"latitude": 1}, "longitude": {"longitude": 1}, "name": {"name": "Venue"}, "address": {"address": "Address"}, "idempotency_key": {"idempotency_key": "another location"},
		}},
		{"send_poll", map[string]any{"to": "chat:1001", "question": "Q?", "options": []string{"A", "B"}}, map[string]map[string]any{
			"to": {"to": "channel:2001"}, "question": {"question": "New?"}, "options": {"options": []string{"A", "C"}}, "options_order": {"options": []string{"B", "A"}}, "multiple_choice": {"multiple_choice": true}, "idempotency_key": {"idempotency_key": "another poll"},
		}},
		{"edit_message", map[string]any{"chat_id": "user:1000", "message_id": "8", "text": "edited"}, map[string]map[string]any{
			"peer": {"chat_id": "user:1001", "message_id": "10"}, "message_id": {"message_id": "10"}, "text": {"text": "new edit"}, "mentions": {"mentions": []string{"user:1001"}},
		}},
		{"delete_message", map[string]any{"chat_id": "user:1001", "message_id": "1"}, map[string]map[string]any{
			"peer": {"chat_id": "user:1000", "message_id": "8"}, "message_id": {"message_id": "2"}, "for_me": {"for_me": true},
		}},
		{"react_to_message", map[string]any{"chat_id": "user:1001", "message_id": "1"}, map[string]map[string]any{
			"peer": {"chat_id": "user:1000", "message_id": "8"}, "message_id": {"message_id": "2"}, "emoji": {"emoji": "🔥"}, "remove": {"emoji": ""},
		}},
		{"forward_message", map[string]any{"chat_id": "user:1001", "message_id": "1", "to": "channel:2001"}, map[string]map[string]any{
			"from": {"chat_id": "user:1000", "message_id": "8"}, "message_id": {"message_id": "2"}, "to": {"to": "user:1000"}, "topic_id": {"topic_id": 1}, "idempotency_key": {"idempotency_key": "another forward"},
		}},
		{"mark_chat_read", map[string]any{"chat_id": "user:1001"}, map[string]map[string]any{
			"peer": {"chat_id": "user:1000"}, "max_id": {"max_id": 2},
		}},
		{"send_typing", map[string]any{"to": "channel:2001", "typing": false}, map[string]map[string]any{
			"peer": {"to": "user:1001"}, "typing": {"typing": true}, "audio_action": {"typing": true, "audio": true}, "topic_id": {"topic_id": 1},
		}},
		{"organise_chat", map[string]any{"chat_id": "user:1001", "action": "mute"}, map[string]map[string]any{
			"peer": {"chat_id": "user:1000"}, "archive": {"action": "archive"}, "unarchive": {"action": "unarchive"}, "pin": {"action": "pin"}, "unpin": {"action": "unpin"}, "unmute": {"action": "unmute"}, "until": {"until": "2027-01-01T00:00:00Z"},
		}},
		{"manage_group_participants", map[string]any{"group_id": "chat:1001", "action": "add", "participants": []string{"user:1001"}}, map[string]map[string]any{
			"group": {"group_id": "channel:2001"}, "users": {"participants": []string{"user:1002"}}, "remove": {"action": "remove"}, "ban": {"action": "ban"}, "promote": {"action": "promote"}, "demote": {"action": "demote"},
		}},
		{"update_group", map[string]any{"group_id": "chat:1001", "name": "new name"}, map[string]map[string]any{
			"group": {"group_id": "channel:2001"}, "title": {"name": "different title"}, "about": {"description": "new about"}, "clear_about": {"description": ""},
		}},
		{"get_group_invite_link", map[string]any{"group_id": "chat:1001", "revoke": true}, map[string]map[string]any{
			"group": {"group_id": "channel:2001"},
		}},
		{"leave_group", map[string]any{"group_id": "chat:1001"}, map[string]map[string]any{
			"group": {"group_id": "channel:2001"},
		}},
	}
	if len(cases) != 15 {
		t.Fatalf("catalog has %d tools, want 15", len(cases))
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		if seen[tc.name] {
			t.Fatalf("duplicate tool %s", tc.name)
		}
		seen[tc.name] = true
		t.Run(tc.name, func(t *testing.T) {
			for field, change := range tc.changes {
				t.Run(field, func(t *testing.T) {
					s, f := newFakeServer(t, engine.ModeReadWrite)
					if err := f.Publish(t.Context(), engine.Batch{Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 1002}, Kind: engine.KindUser, FirstName: "Other", Access: engine.AccessOK}}}, Messages: []engine.Message{
						{Key: engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1000}, ID: 10}, Out: true, Text: "another own message"},
						{Key: engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1001}, ID: 10}, Out: true, Text: "another own message"},
					}}); err != nil {
						t.Fatal(err)
					}
					before := visibleCanonicalPreview(t, s, tc.name, tc.base)
					updated := map[string]any{}
					for k, v := range tc.base {
						updated[k] = v
					}
					for k, v := range change {
						updated[k] = v
					}
					after := visibleCanonicalPreview(t, s, tc.name, updated)
					if reflect.DeepEqual(before, after) {
						t.Errorf("material %s change is invisible without fingerprint: %v", field, before)
					}
					if len(f.Writes()) != 0 {
						t.Fatal("preview caused Telegram write")
					}
				})
			}
		})
	}
	for _, def := range definitions() {
		name := def.(map[string]any)["name"].(string)
		if telegramMutation(name) && !seen[name] {
			t.Errorf("missing mutation %s", name)
		}
	}
}

func visibleCanonicalPreview(t *testing.T, s *Server, name string, args map[string]any) map[string]any {
	t.Helper()
	r := toolCall(t, s, name, args)
	previewFingerprint(t, r)
	var body map[string]any
	decoder := json.NewDecoder(strings.NewReader(r["content"].([]any)[0].(map[string]any)["text"].(string)))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		t.Fatal(err)
	}
	preview := body["preview"].(map[string]any)
	if parameters, ok := preview["parameters"].(map[string]any); ok {
		// Independently encode only the public action and parameters. If approval
		// identity ever falls back to raw arguments or a second hidden intent,
		// this assertion fails for every mutation, not merely selected examples.
		encoded, err := json.Marshal(struct {
			Action     any            `json:"action"`
			Parameters map[string]any `json:"parameters"`
		}{preview["action"], parameters})
		if err != nil {
			t.Fatal(err)
		}
		s.previewMu.Lock()
		approval, found := s.previews[preview["fingerprint"].(string)]
		s.previewMu.Unlock()
		if !found || approval.binding != sha256.Sum256(encoded) {
			t.Fatal("approval is not bound to exactly the visible action and parameters")
		}
	}
	delete(preview, "fingerprint")
	return preview
}

// A canonical preview is a JSON value, not a formatted summary or a hidden
// binding to raw caller aliases. This helper also keeps numeric assertions exact.
func canonicalParameters(t *testing.T, s *Server, name string, args map[string]any) map[string]any {
	t.Helper()
	preview := visibleCanonicalPreview(t, s, name, args)
	params, ok := preview["parameters"].(map[string]any)
	if !ok {
		encoded, _ := json.Marshal(preview)
		t.Fatalf("missing preview.parameters: %s", encoded)
	}
	return params
}

func TestCanonicalPreviewEquivalentEffectiveRequestsCanConfirm(t *testing.T) {
	media := filepath.Join(t.TempDir(), "same.txt")
	copyPath := filepath.Join(t.TempDir(), "same.txt")
	for _, path := range []string{media, copyPath} {
		if err := os.WriteFile(path, []byte("same bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name          string
		before, after map[string]any
	}{
		{"send_text_message", map[string]any{"to": "@fake_ana", "text": "same"}, map[string]any{"to": "user:1001", "text": "same", "mentions": []string{}}},
		{"send_text_message", map[string]any{"to": "channel:2001", "text": "same", "reply_to": "1"}, map[string]any{"to": "channel:2001", "text": "same", "reply_to": "channel:2001/1"}},
		{"send_media_message", map[string]any{"to": "user:1001", "type": "image", "url": media}, map[string]any{"to": "user:1001", "type": "photo", "url": copyPath}},
		{"send_typing", map[string]any{"to": "user:1001"}, map[string]any{"to": "user:1001", "typing": true, "audio": false}},
		{"send_typing", map[string]any{"to": "user:1001", "typing": false, "audio": true}, map[string]any{"to": "user:1001", "typing": false, "audio": false}},
		{"mark_chat_read", map[string]any{"chat_id": "user:1001"}, map[string]any{"chat_id": "user:1001", "max_id": 0, "receipts": true}},
		{"react_to_message", map[string]any{"chat_id": "user:1001", "message_id": "1"}, map[string]any{"message_id": "user:1001/1", "emoji": "👍"}},
		{"get_group_invite_link", map[string]any{"group_id": "chat:1001", "reset": true}, map[string]any{"group_id": "chat:1001", "revoke": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newFakeServer(t, engine.ModeReadWrite)
			fp := previewFingerprint(t, toolCall(t, s, tc.name, tc.before))
			tc.after["confirm"], tc.after["preview_fingerprint"] = true, fp
			if out := toolCall(t, s, tc.name, tc.after); out["isError"] != false || len(f.Writes()) != 1 {
				t.Fatalf("same effective intent rejected because raw inputs differ: %v", out)
			}
			if out := toolCall(t, s, tc.name, tc.after); out["isError"] != true || len(f.Writes()) != 1 {
				t.Fatal("canonical approval replayed")
			}
		})
	}
}

func TestCanonicalPreviewUpdateGroupNullVersusEmpty(t *testing.T) {
	s, f := newFakeServer(t, engine.ModeReadWrite)
	unchanged := canonicalParameters(t, s, "update_group", map[string]any{"group_id": "chat:1001", "name": "title"})
	cleared := canonicalParameters(t, s, "update_group", map[string]any{"group_id": "chat:1001", "name": "title", "description": ""})
	if value, exists := unchanged["about"]; !exists || value != nil {
		t.Fatalf("omitted about must explicitly be null: %v", unchanged)
	}
	if value, exists := cleared["about"]; !exists || value != "" {
		t.Fatalf("cleared about must explicitly be empty: %v", cleared)
	}
	titleUnchanged := canonicalParameters(t, s, "update_group", map[string]any{"group_id": "chat:1001", "description": "about"})
	if value, exists := titleUnchanged["title"]; !exists || value != nil {
		t.Fatalf("omitted title must explicitly be null: %v", titleUnchanged)
	}
	if len(f.Writes()) != 0 {
		t.Fatal("preview wrote to Telegram")
	}
}

func TestCanonicalPreviewPreservesWideTelegramIdentity(t *testing.T) {
	s, f := newFakeServer(t, engine.ModeReadWrite)
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 9007199254740993}
	if err := f.Publish(t.Context(), engine.Batch{Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Kind: engine.KindUser, FirstName: "Wide ID", Access: engine.AccessOK}}}}); err != nil {
		t.Fatal(err)
	}
	params := canonicalParameters(t, s, "send_typing", map[string]any{"to": "user:9007199254740993"})
	if id := params["peer"].(map[string]any)["id"]; id != json.Number("9007199254740993") {
		t.Fatalf("canonical identity rounded: %v", params)
	}
}
