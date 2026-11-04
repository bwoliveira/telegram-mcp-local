package store

import (
	"context"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestListUnreadExcludesBotsBeforePaginationSQLite(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	for i, kind := range []string{"bot", "bot", "user", "user"} {
		queryPeer(t, s, "user", i+1, kind)
		queryMsg(t, s, "user", i+1, 1, 1100-i, "question?")
		querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,unread_count,refreshed_at) VALUES('primary','user',?,1,1000)`, i+1)
	}
	queryPeer(t, s, "chat", 1, "group")
	queryMsg(t, s, "chat", 1, 1, 1000, "bot message in a group")
	querySQL(t, s, `UPDATE messages SET sender_type='user',sender_id=1 WHERE peer_type='chat'`)
	querySQL(t, s, `INSERT INTO participants(account,group_type,group_id,user_type,user_id,role,updated_at) VALUES('primary','chat',1,'user',1,'member',1000)`)
	querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,unread_count,refreshed_at) VALUES('primary','chat',1,1,1000)`)

	page, err := s.ListUnread(ctx, "primary", UnreadOptions{MessageOptions: MessageOptions{Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 2 || page.Messages[0].Key.Peer.ID != 3 || page.Messages[1].Key.Peer.ID != 4 || page.Next == nil {
		t.Fatalf("bots consumed the first page: %+v", page)
	}
	page, err = s.ListUnread(ctx, "primary", UnreadOptions{MessageOptions: MessageOptions{Limit: 2, Before: page.Next}})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Key.Peer.Type != engine.PeerChat || page.Next != nil {
		t.Fatalf("group with bot participant missing or wrong cursor: %+v, %v", page, err)
	}
	page, err = s.ListUnread(ctx, "primary", UnreadOptions{MessageOptions: MessageOptions{Limit: 2}, IncludeBots: true})
	if err != nil || len(page.Messages) != 2 || page.Messages[0].Key.Peer.ID != 1 || page.Messages[1].Key.Peer.ID != 2 || page.Next == nil {
		t.Fatalf("bot opt-in not honored: %+v, %v", page, err)
	}
	bot := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	page, err = s.ListUnread(ctx, "primary", UnreadOptions{Peer: &bot})
	if err != nil || len(page.Messages) != 0 || page.Next != nil {
		t.Fatalf("explicit peer must still honor default exclusion: %+v, %v", page, err)
	}
	page, err = s.ListUnread(ctx, "primary", UnreadOptions{Peer: &bot, IncludeBots: true})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Key.Peer != bot {
		t.Fatalf("explicit bot opt-in not honored: %+v, %v", page, err)
	}
}

func TestListUnreadBotMetadataAndAccountScopeSQLite(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	unknown := engine.PeerRef{Type: engine.PeerUser, ID: 9}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Source: engine.SourceBackfill,
		Dialogs:  []engine.Dialog{{Peer: unknown, UnreadCount: 1}},
		Messages: []engine.Message{{Key: engine.MessageKey{Account: "primary", Peer: unknown, ID: 1}, Date: s.now()}},
	}); err != nil {
		t.Fatal(err)
	}
	querySQL(t, s, `INSERT INTO peers(account,peer_type,peer_id,kind,updated_at) VALUES('secondary','user',9,'bot',1000)`)
	page, err := s.ListUnread(ctx, "primary", UnreadOptions{})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Key.Peer != unknown {
		t.Fatalf("unknown peer or foreign account bot incorrectly excluded: %+v, %v", page, err)
	}
	querySQL(t, s, `UPDATE peers SET kind='bot' WHERE account='primary' AND peer_id=9`)
	page, err = s.ListUnread(ctx, "primary", UnreadOptions{})
	if err != nil || len(page.Messages) != 0 {
		t.Fatalf("new bot metadata ignored: %+v, %v", page, err)
	}
	if _, err := s.ListUnread(ctx, "secondary", UnreadOptions{IncludeBots: true}); err != engine.ErrAccountMismatch {
		t.Fatalf("bot opt-in bypassed account isolation: %v", err)
	}
}
