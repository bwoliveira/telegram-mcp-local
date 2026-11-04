package mcp

import (
	"context"
	"fmt"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestChatDiscoveryPaginationAndPeerTitleWithoutMessages(t *testing.T) {
	ctx := context.Background()
	const acc engine.AccountID = "discovery"
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"), acc)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := fake.New(engine.Config{Account: acc})
	if err != nil {
		t.Fatal(err)
	}
	s := New(Config{Engine: f, ReadStore: db})
	b := engine.Batch{Account: acc}
	for i := int64(1); i <= 108; i++ {
		ref := engine.PeerRef{Type: engine.PeerChat, ID: i}
		b.Peers = append(b.Peers, engine.PeerUpsert{Peer: engine.Peer{Ref: ref, Kind: engine.KindGroup, Title: fmt.Sprintf("Synthetic group %03d", i), Access: engine.AccessOK}})
		if i < 108 {
			b.Dialogs = append(b.Dialogs, engine.Dialog{Peer: ref})
		}
	}
	b.Peers[107].Peer.Title = "Telegram MCP teste"
	// Also retain a direct conversation observed only through a message.
	user := engine.PeerRef{Type: engine.PeerUser, ID: 222}
	b.Messages = []engine.Message{{Key: engine.MessageKey{Account: acc, Peer: user, ID: 1}, Date: time.Unix(1000, 0)}}
	if _, err := db.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	offset := 0
	for {
		body := messagesBody(t, s, "list_chats", map[string]any{"limit": 100, "offset": offset, "fields": []string{"chat_id", "name"}})
		rows := body["chats"].([]any)
		for _, v := range rows {
			id := v.(map[string]any)["chat_id"].(string)
			if seen[id] {
				t.Fatalf("duplicate at offset %d: %s", offset, id)
			}
			seen[id] = true
		}
		if body["total"] != float64(109) {
			t.Fatalf("wrong total: %v", body)
		}
		if body["has_more"] == false {
			if body["next_offset"] != nil {
				t.Fatal("terminal page has a next offset")
			}
			break
		}
		next, ok := body["next_offset"].(float64)
		if !ok || int(next) <= offset {
			t.Fatalf("missing advancing offset: %v", body)
		}
		offset = int(next)
	}
	if len(seen) != 109 || !seen["chat:108"] || !seen["user:222"] {
		t.Fatalf("incomplete chat listing: %d", len(seen))
	}
	for _, key := range []string{"search", "query"} {
		for _, q := range []string{"MCP teste", "teste"} {
			body := messagesBody(t, s, "list_chats", map[string]any{key: q})
			rows := body["chats"].([]any)
			if len(rows) != 1 || rows[0].(map[string]any)["chat_id"] != "chat:108" {
				t.Fatalf("peer-only title query failed: %v", body)
			}
			p, err := s.resolvePeer(ctx, q)
			if err != nil || p != (engine.PeerRef{Type: engine.PeerChat, ID: 108}) {
				t.Fatalf("resolver missed peer title: %v %v", p, err)
			}
		}
	}
	last := messagesBody(t, s, "list_chats", map[string]any{"offset": 1000})
	if last["count"] != float64(0) || last["has_more"] != false {
		t.Fatalf("past last page: %v", last)
	}
	if peopleCall(t, s, "list_chats", map[string]any{"offset": -1})["isError"] != true {
		t.Fatal("negative offset accepted")
	}
}

func TestChatDiscoverySchemaExposesPagination(t *testing.T) {
	for _, raw := range toolDefinitions() {
		tool := raw.(map[string]any)
		if tool["name"] != "list_chats" {
			continue
		}
		props := tool["inputSchema"].(map[string]any)["properties"].(map[string]any)
		if props["offset"] == nil || props["query"] == nil {
			t.Fatalf("pagination or query alias missing from schema: %v", props)
		}
		return
	}
	t.Fatal("list_chats schema missing")
}
