package store

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
)

func TestChatDiscoveryIncludesPeerOnlyGroupsAndMessageConversations(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "chat", 71, "group")
	querySQL(t, s, `UPDATE peers SET title='Telegram MCP teste',search_name='telegram mcp teste' WHERE peer_type='chat'`)
	queryPeer(t, s, "user", 72, "user")
	queryMsg(t, s, "user", 72, 1, 1000, "synthetic")
	// A contact without a conversation is not a chat.
	queryPeer(t, s, "user", 73, "user")
	querySQL(t, s, `UPDATE peers SET is_contact=1 WHERE peer_id=73`)
	got, err := s.ListChats(ctx, "primary", ChatOptions{})
	if err != nil || len(got) != 2 {
		t.Fatalf("expected group peer and message-only conversation, got %d: %v", len(got), err)
	}
	for _, q := range []string{"MCP teste", "teste"} {
		got, err = s.ListChats(ctx, "primary", ChatOptions{PeerOptions: PeerOptions{Query: q}})
		if err != nil || len(got) != 1 || got[0].Peer.Ref != (engine.PeerRef{Type: engine.PeerChat, ID: 71}) || got[0].LastMessage != nil {
			t.Fatalf("peer title %q missing: %v %v", q, got, err)
		}
	}
	page, err := s.ListChats(ctx, "primary", ChatOptions{PeerOptions: PeerOptions{Limit: 1, Offset: 1}})
	if err != nil || len(page) != 1 || page[0].Peer.Ref.Type != engine.PeerChat {
		t.Fatalf("pagination: %v %v", page, err)
	}
}
