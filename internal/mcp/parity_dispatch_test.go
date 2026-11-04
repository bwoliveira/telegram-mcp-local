package mcp

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// A catalogue count alone passes even if all handlers return stubs. Exercise
// every documented Telegram tool through the actual dispatcher instead.
func TestParityAll40ToolsDispatchWithFake(t *testing.T) {
	s, f, st := parityServer(t)
	upload := filepath.Join(t.TempDir(), "example.txt")
	if err := os.WriteFile(upload, []byte("synthetic upload"), 0600); err != nil {
		t.Fatal(err)
	}
	key := engine.MessageKey{Account: "primary", Peer: fake.PrivatePeer, ID: 5}
	if err := st.SaveTranscript(context.Background(), key, store.Transcript{Text: "cached synthetic speech", RawText: "cached synthetic speech", Source: "local"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		args    map[string]any
		preview bool
	}{
		{"health", nil, false},
		{"telegram_status", nil, false},
		{"list_chats", nil, false},
		{"get_chat_messages", map[string]any{"chat_id": "me"}, false},
		{"search_messages", map[string]any{"query": "synthetic"}, false},
		{"list_contacts", nil, false},
		{"list_groups", nil, false},
		{"get_group", map[string]any{"group_id": "chat:1001", "live": true}, false},
		{"send_text_message", map[string]any{"to": "me", "text": "synthetic"}, true},
		{"send_media_message", map[string]any{"to": "me", "type": "document", "url": upload}, true},
		{"download_media", map[string]any{"message_id": "user:1001/4", "link": true}, false},
		{"transcribe_audio", map[string]any{"message_id": "user:1001/5"}, false},
		{"save_transcript", map[string]any{"message_id": "user:1001/5", "text": "corrected synthetic speech"}, false},
		{"sync_history", map[string]any{"chat_id": "me", "count": 10, "rounds": 1}, false},
		{"delete_message", map[string]any{"chat_id": "me", "message_id": "8"}, true},
		{"edit_message", map[string]any{"chat_id": "me", "message_id": "8", "text": "updated"}, true},
		{"react_to_message", map[string]any{"chat_id": "user:1001", "message_id": "1"}, true},
		{"resolve_contact", map[string]any{"queries": []string{"@fake_ana"}}, false},
		{"get_profile_picture", map[string]any{"to": "@fake_ana"}, false},
		{"send_location", map[string]any{"to": "me", "latitude": -23.0, "longitude": -46.0}, true},
		{"send_poll", map[string]any{"to": "me", "question": "Choice?", "options": []string{"A", "B"}}, true},
		{"get_poll_results", map[string]any{"message_id": "chat:1001/7"}, false},
		{"organise_chat", map[string]any{"chat_id": "me", "action": "archive"}, true},
		{"forward_message", map[string]any{"message_id": "user:1001/1", "to": "me"}, true},
		{"mark_chat_read", map[string]any{"chat_id": "me"}, true},
		{"send_typing", map[string]any{"to": "me"}, true},
		{"get_message_context", map[string]any{"chat_id": "me", "message_id": "8"}, false},
		{"message_stats", map[string]any{"chat_id": "me"}, false},
		{"export_messages", map[string]any{"chat_id": "me"}, false},
		{"list_unread", nil, false},
		{"list_unanswered", nil, false},
		{"list_mentions", map[string]any{"chat_id": "me"}, false},
		{"mark_handled", map[string]any{"chat_id": "me"}, false},
		{"snooze_chat", map[string]any{"chat_id": "me", "until": fake.Epoch().Add(time.Hour).Format(time.RFC3339)}, false},
		{"manage_group_participants", map[string]any{"group_id": "chat:1001", "action": "add", "participants": []string{"@fake_ana"}}, true},
		{"update_group", map[string]any{"group_id": "chat:1001", "name": "Updated"}, true},
		{"get_group_invite_link", map[string]any{"group_id": "chat:1001"}, false},
		{"leave_group", map[string]any{"group_id": "chat:1001"}, true},
		{"media_stats", nil, false},
		{"purge_media", nil, false},
	}
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "PARIDADE.md"))
	if err != nil {
		t.Fatal(err)
	}
	rows := regexp.MustCompile("(?m)^\\| T[0-9]{2} \\|.*$").FindAllString(string(doc), -1)
	documented := map[string]bool{}
	for _, row := range rows {
		cells := strings.Split(row, "|")
		documented[strings.Trim(strings.TrimSpace(cells[6]), "`")] = true
	}
	if len(rows) != 40 || len(documented) != 40 || len(cases) != 40 {
		t.Fatalf("expected 40 distinct docs/tools: rows=%d names=%d cases=%d", len(rows), len(documented), len(cases))
	}
	for _, tc := range cases {
		if !documented[tc.name] {
			t.Fatalf("tool absent/duplicated in PARIDADE: %s", tc.name)
		}
		delete(documented, tc.name)
		t.Run(tc.name, func(t *testing.T) {
			r := toolCall(t, s, tc.name, tc.args)
			body := paritySuccess(t, r)
			if tc.preview {
				_ = previewFingerprint(t, r)
			}
			if tc.name == "sync_history" {
				parityWaitHistory(t, s)
				status := paritySuccess(t, toolCall(t, s, "telegram_status", nil))
				if status["history_request"] == nil || status["history_coverage"] == nil {
					t.Fatalf("status omits history evidence: %+v", status)
				}
			}
			if tc.name == "export_messages" && body["retention_registered"] != true {
				t.Fatal("export not durably registered", body)
			}
		})
	}
	if len(documented) != 0 {
		t.Fatal("unexercised tools", documented)
	}
	if len(f.Writes()) != 0 {
		t.Fatal("read/preview wrote Telegram", f.Writes())
	}
}
