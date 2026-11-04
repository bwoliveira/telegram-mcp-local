package mcp

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// Use the actual tools/call dispatcher and SQLite read adapter. The fixture's
// engine is disconnected and checks that no Telegram writes were attempted.
func botTriageSeed(t *testing.T, st *store.Store, now time.Time) {
	t.Helper()
	batch := engine.Batch{Account: "primary", Source: engine.SourceBackfill}
	for i := 1; i <= 5; i++ {
		ref := engine.PeerRef{Type: engine.PeerUser, ID: int64(i)}
		kind := engine.KindUser
		if i <= 2 {
			kind = engine.KindBot
		}
		if i == 5 {
			ref = engine.PeerRef{Type: engine.PeerChat, ID: 1}
			kind = engine.KindGroup
		}
		// No peer metadata is supplied for user:3: Apply creates only the
		// placeholder required by the message/dialog foreign keys.
		if i != 3 {
			batch.Peers = append(batch.Peers, engine.PeerUpsert{Peer: engine.Peer{Ref: ref, Kind: kind}})
		}
		batch.Dialogs = append(batch.Dialogs, engine.Dialog{Peer: ref, UnreadCount: i})
		msg := engine.Message{Key: engine.MessageKey{Account: "primary", Peer: ref, ID: 1}, Date: now.Add(-time.Duration(i) * time.Minute), Text: "Can you review this question?"}
		if i == 5 {
			bot := engine.PeerRef{Type: engine.PeerUser, ID: 1}
			msg.Sender = &bot
			batch.Participants = append(batch.Participants, engine.ParticipantsSnapshot{Group: ref, Participants: []engine.Participant{{Peer: bot, Role: "member"}}, Size: 1, At: now})
		}
		batch.Messages = append(batch.Messages, msg)
	}
	if _, err := st.Apply(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

func botTriageExpect(t *testing.T, s *Server, tool string, args map[string]any, wantTruncated bool, wantIDs ...string) map[string]any {
	t.Helper()
	if tool == "list_unread" {
		exact := make(map[string]any, len(args))
		for key, value := range args {
			if key != "include_groups" {
				exact[key] = value
			}
		}
		args = exact
	}
	body := paritySuccess(t, toolCall(t, s, tool, args))
	rows := body["chats"].([]any)
	if len(rows) != len(wantIDs) || body["count"] != float64(len(wantIDs)) || body["truncated"] != wantTruncated {
		t.Fatalf("%s: want chats %v truncated=%v, got %v", tool, wantIDs, wantTruncated, body)
	}
	for i, id := range wantIDs {
		if rows[i].(map[string]any)["chat_id"] != id {
			t.Fatalf("%s: want chats %v, got %v", tool, wantIDs, rows)
		}
	}
	return body
}

func TestBotTriageDefaultExclusionSQLite(t *testing.T) {
	for _, tool := range []string{"list_unread", "list_unanswered"} {
		t.Run(tool, func(t *testing.T) {
			s, st, now, _ := unreadDispatchSQLite(t)
			botTriageSeed(t, st, *now)
			body := botTriageExpect(t, s, tool, map[string]any{"include_groups": true}, false, "user:3", "user:4", "chat:1")
			if tool == "list_unread" && body["unread_messages"] != float64(12) {
				t.Fatalf("bot counters leaked: %v", body)
			}
			// Newer bots must not consume a conversation slot, nor make the
			// last eligible page look truncated when only bots were removed.
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true, "limit": 2}, true, "user:3", "user:4")
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true, "limit": 3}, false, "user:3", "user:4", "chat:1")
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true, "include_bots": false}, false, "user:3", "user:4", "chat:1")
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true, "include_bots": true}, false, "user:1", "user:2", "user:3", "user:4", "chat:1")
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true, "include_bots": true, "limit": 2}, true, "user:1", "user:2")
		})
	}
}

func TestBotTriagePreservesOtherFiltersSQLite(t *testing.T) {
	for _, tool := range []string{"list_unread", "list_unanswered"} {
		t.Run(tool, func(t *testing.T) {
			s, st, now, _ := unreadDispatchSQLite(t)
			botTriageSeed(t, st, *now)
			until := now.Add(time.Hour)
			if _, err := st.Apply(context.Background(), engine.Batch{Account: "primary", Dialogs: []engine.Dialog{
				{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 1}, UnreadCount: 1, MuteUntil: &until},
				{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 2}, UnreadCount: 2, FolderID: 1},
			}}); err != nil {
				t.Fatal(err)
			}
			unreadDispatchMark(t, s, "mark_handled", engine.PeerRef{Type: engine.PeerUser, ID: 4}, *now)
			unreadDispatchMark(t, s, "snooze_chat", engine.PeerRef{Type: engine.PeerUser, ID: 3}, *now)
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true, "include_bots": true}, false, "user:1", "chat:1")
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true, "include_bots": true, "include_muted": false}, false, "chat:1")
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true, "include_bots": true, "include_muted": false, "include_archived": true}, false, "user:2", "chat:1")
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true, "include_bots": false, "include_archived": true}, false, "chat:1")
		})
	}
}

func TestBotTriageMetadataAndAccountIsolationSQLite(t *testing.T) {
	for _, tool := range []string{"list_unread", "list_unanswered"} {
		t.Run(tool, func(t *testing.T) {
			s, st, now, path := unreadDispatchSQLite(t)
			botTriageSeed(t, st, *now)
			// Same typed IDs in a foreign namespace must neither classify an
			// active human as a bot nor override an active bot's metadata.
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			if _, err := raw.Exec(`INSERT INTO peers(account,peer_type,peer_id,kind,updated_at) VALUES('secondary','user',3,'bot',1),('secondary','user',1,'user',1)`); err != nil {
				t.Fatal(err)
			}
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true}, false, "user:3", "user:4", "chat:1")
			// Bot classification follows persisted metadata as it arrives; it
			// must not guess from names or remember the placeholder forever.
			peer := engine.PeerRef{Type: engine.PeerUser, ID: 3}
			applyKind := func(kind engine.PeerKind) {
				t.Helper()
				if _, err := st.Apply(context.Background(), engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Kind: kind, Username: "helperbot", FirstName: "Robot"}}}}); err != nil {
					t.Fatal(err)
				}
			}
			applyKind(engine.KindBot)
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true}, false, "user:4", "chat:1")
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true, "include_bots": true}, false, "user:1", "user:2", "user:3", "user:4", "chat:1")
			applyKind(engine.KindUser)
			botTriageExpect(t, s, tool, map[string]any{"include_groups": true}, false, "user:3", "user:4", "chat:1")
		})
	}
}

func TestBotTriageOptInSchema(t *testing.T) {
	seen := map[string]bool{}
	for _, raw := range toolDefinitions() {
		def := raw.(map[string]any)
		name := def["name"].(string)
		if name != "list_unread" && name != "list_unanswered" {
			continue
		}
		seen[name] = true
		schema := def["inputSchema"].(map[string]any)
		prop, ok := schema["properties"].(map[string]any)["include_bots"].(map[string]any)
		if !ok || prop["type"] != "boolean" || prop["default"] != false {
			t.Errorf("%s must expose optional include_bots:boolean default:false: %v", name, prop)
		}
		if required, ok := schema["required"].([]string); ok {
			for _, field := range required {
				if field == "include_bots" {
					t.Errorf("%s requires opt-in parameter", name)
				}
			}
		}
	}
	if len(seen) != 2 {
		t.Fatalf("missing tools: %v", seen)
	}
}
