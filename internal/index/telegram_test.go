package index

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestTelegramPeerTypes(t *testing.T) {
	for input, want := range map[string]string{
		"user:1": "user", "chat:1": "chat", "channel:1": "channel",
		"user:9223372036854775807": "user",
		"user:9223372036854775808": "", "user:0": "", "user:01": "",
		"user:-1": "", "user:+1": "", "user:": "", "user:1:2": "",
		"chat:1x": "", "channel:1@elsewhere.invalid": "", "1": "", "Alice": "",
	} {
		if got := peerType(input); got != want {
			t.Errorf("peerType(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestIsCollectivePeer(t *testing.T) {
	for _, tc := range []struct {
		peer string
		want bool
	}{
		{"chat:1", true}, {"channel:1", true},
		{"chat:9223372036854775807", true}, {"channel:9223372036854775807", true},
		{"user:1", false}, {"", false}, {"42", false}, {"group:42", false},
		{"chat:0", false}, {"channel:0", false}, {"chat:01", false}, {"channel:01", false},
		{"chat:-1", false}, {"channel:+1", false}, {"chat:", false}, {"channel:1:2", false},
		{"chat:1x", false}, {"channel:9223372036854775808", false},
		{"chat:9223372036854775808", false}, {" chat:1", false}, {"channel:1 ", false},
	} {
		t.Run(tc.peer, func(t *testing.T) {
			if got := IsCollectivePeer(tc.peer); got != tc.want {
				t.Errorf("IsCollectivePeer(%q) = %v, want %v", tc.peer, got, tc.want)
			}
		})
	}
}

func TestTelegramConversationKinds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "index.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, peer := range []struct{ id, kind, name string }{
		{"user:42", "dm", "Alice"},
		{"chat:42", "group", "Team"},
		{"channel:42", "channel", "News"},
	} {
		if _, err := db.Exec(`INSERT INTO chats (jid, kind, name, unread_count, last_message_ts) VALUES (?, ?, ?, 1, 100)`, peer.id, peer.kind, peer.name); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO messages (chat_jid, msg_id, sender_jid, ts, from_me, text) VALUES (?, '1', 'user:42', 100, 0, 'Hello')`, peer.id); err != nil {
			t.Fatal(err)
		}
	}
	x, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	ctx := context.Background()
	chats, err := x.RecentChats(ctx, 10)
	if err != nil || len(chats) != 3 {
		t.Fatalf("RecentChats = %+v, %v", chats, err)
	}
	for _, chat := range chats {
		wantGroup := chat.JID != "user:42"
		if chat.Group != wantGroup {
			t.Errorf("%s group = %v, want %v", chat.JID, chat.Group, wantGroup)
		}
		if chat.Group && chat.LastSender != "Alice" {
			t.Errorf("%s sender = %q, want Alice", chat.JID, chat.LastSender)
		}
		bubbles, err := x.Conversation(ctx, chat.JID, 10)
		if err != nil || len(bubbles) != 1 || bubbles[0].Group != wantGroup {
			t.Errorf("Conversation(%s) = %+v, %v", chat.JID, bubbles, err)
		}
	}
	if count, err := x.CountMessages(ctx, Filter{ExcludeGroups: true}); err != nil || count != 1 {
		t.Errorf("direct message count = %d, %v; want 1", count, err)
	}
	if unread, err := x.UnreadChats(ctx, false, 10); err != nil || len(unread) != 3 {
		t.Errorf("unread Telegram conversations = %+v, %v", unread, err)
	}
}
