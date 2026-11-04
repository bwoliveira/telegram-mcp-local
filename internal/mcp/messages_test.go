package mcp

import (
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
	"time"
)

func TestMessageToolsRejectInvalidArguments(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"send_media_message", map[string]any{"to": "user:1001", "type": "invalid", "url": "/not-used"}},
		{"send_location", map[string]any{"to": "user:1001", "latitude": 91, "longitude": 0}},
		{"send_poll", map[string]any{"to": "chat:1001", "question": "Question", "options": []string{"only"}}},
		{"get_poll_results", map[string]any{"chat_id": "chat:1001", "message_id": "0"}},
		{"edit_message", map[string]any{"chat_id": "user:1000", "message_id": "8"}},
		{"delete_message", map[string]any{"chat_id": "user:1000", "message_id": "8", "for_me": true, "confirm": true}},
		{"react_to_message", map[string]any{"chat_id": "user:1001", "message_id": "0"}},
		{"forward_message", map[string]any{"chat_id": "user:1001", "message_id": "1"}},
		{"send_typing", map[string]any{}},
		{"organise_chat", map[string]any{"chat_id": "user:1001", "action": "erase"}},
		{"resolve_contact", map[string]any{"queries": []string{}}},
		{"get_profile_picture", map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newFakeServer(t, engine.ModeReadWrite)
			if got := toolCall(t, s, tc.name, tc.args); got["isError"] != true || len(f.Writes()) != 0 {
				t.Fatalf("invalid input accepted: %v", got)
			}
		})
	}
}

func TestMessageApprovalExpiresForEachAction(t *testing.T) {
	cases := []struct {
		name string
		args map[string]any
	}{
		{"send_location", map[string]any{"to": "user:1001", "latitude": 0, "longitude": 0}},
		{"send_poll", map[string]any{"to": "chat:1001", "question": "Q?", "options": []string{"A", "B"}}},
		{"edit_message", map[string]any{"chat_id": "user:1000", "message_id": "8", "text": "replacement"}},
		{"delete_message", map[string]any{"chat_id": "user:1000", "message_id": "8"}},
		{"react_to_message", map[string]any{"chat_id": "user:1001", "message_id": "1"}},
		{"forward_message", map[string]any{"chat_id": "user:1001", "message_id": "1", "to": "user:1000"}},
		{"send_typing", map[string]any{"to": "user:1001"}},
		{"organise_chat", map[string]any{"chat_id": "user:1001", "action": "mute"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newFakeServer(t, engine.ModeReadWrite)
			now := s.clock()
			s.clock = func() time.Time { return now }
			fp := previewFingerprint(t, toolCall(t, s, tc.name, tc.args))
			now = now.Add(10 * time.Minute)
			tc.args["confirm"] = true
			tc.args["preview_fingerprint"] = fp
			if got := toolCall(t, s, tc.name, tc.args); got["isError"] != true || len(f.Writes()) != 0 {
				t.Fatalf("expired approval accepted: %v", got)
			}
		})
	}
}

func TestMessageChannelForMeRejected(t *testing.T) {
	s, f := newFakeServer(t, engine.ModeReadWrite)
	got := toolCall(t, s, "delete_message", map[string]any{"chat_id": "channel:2001", "message_id": "1", "for_me": true})
	if got["isError"] != true || len(f.Writes()) != 0 {
		t.Fatalf("account-only channel scope escalated: %v", got)
	}
}
