package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

var _ interface {
	PanelChats(context.Context, engine.AccountID, PanelChatsQuery) (PanelChatsPage, error)
	PanelConversation(context.Context, engine.AccountID, engine.Peer, PanelMessagesQuery) (PanelMessagesPage, error)
	PanelSearch(context.Context, engine.AccountID, PanelMessagesQuery) (PanelMessagesPage, error)
} = (*Store)(nil)

func panelTestPeer(typ engine.PeerType, id int64) engine.Peer {
	return engine.Peer{Ref: engine.PeerRef{Type: typ, ID: id}}
}

func TestPanelChatsUnicodeNameSearch(t *testing.T) {
	s := queryStore(t)
	queryPeer(t, s, "user", 1, "user")
	queryMsg(t, s, "user", 1, 1, 1000, "cached")
	querySQL(t, s, `UPDATE peers SET title='Álvaro' WHERE peer_type='user' AND peer_id=1`)
	for _, term := range []string{"Álvaro", "álvaro", "ÁLVARO"} {
		page, err := s.PanelChats(context.Background(), "primary", PanelChatsQuery{Query: term})
		if err != nil || len(page.Chats) != 1 || page.Chats[0].Name != "Álvaro" {
			t.Fatalf("Unicode query %q: %+v %v", term, page, err)
		}
	}
}

func TestPanelChatsDiscoveryFiltersAndPagination(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	for i := 1; i <= 37; i++ {
		queryPeer(t, s, "user", i, "user")
		queryMsg(t, s, "user", i, 1, 1000+i, "cached")
		querySQL(t, s, `UPDATE peers SET title=? WHERE peer_type='user' AND peer_id=?`, fmt.Sprintf("Person %02d", i), i)
	}
	// Contact-only users are not chats. Known groups/channels and dialogs are.
	queryPeer(t, s, "user", 100, "user")
	queryPeer(t, s, "chat", 100, "group")
	queryPeer(t, s, "channel", 100, "channel")
	querySQL(t, s, `UPDATE peers SET is_contact=1 WHERE peer_type='user' AND peer_id=100`)
	querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,unread_count,unread_mark,last_message_date,refreshed_at) VALUES('primary','user',1,8,0,9000,1000),('primary','user',2,0,1,9000,1000),('primary','channel',100,0,0,9000,1000)`)
	page, err := s.PanelChats(ctx, "primary", PanelChatsQuery{})
	if err != nil || len(page.Chats) != 30 || !page.HasMore || page.NextOffset != 30 || page.Chats[0].ID != "user:37" {
		t.Fatalf("default page: %+v %v", page, err)
	}
	next, err := s.PanelChats(ctx, "primary", PanelChatsQuery{Offset: page.NextOffset})
	if err != nil || len(next.Chats) != 9 || next.HasMore || next.NextOffset != 0 {
		t.Fatalf("last page: %+v %v", next, err)
	}
	seen := map[string]bool{}
	for _, chat := range append(page.Chats, next.Chats...) {
		if seen[chat.ID] || chat.ID == "user:100" {
			t.Fatalf("duplicate or contact-only chat: %+v", chat)
		}
		seen[chat.ID] = true
	}
	if len(seen) != 39 || !seen["chat:100"] || !seen["channel:100"] {
		t.Fatalf("discovery: %+v", seen)
	}
	unread := true
	page, err = s.PanelChats(ctx, "primary", PanelChatsQuery{Unread: &unread})
	if err != nil || len(page.Chats) != 2 || page.Chats[0].ID != "user:2" || page.Chats[0].UnreadCount != 0 || page.Chats[1].UnreadCount != 8 {
		t.Fatalf("server unread counters/mark: %+v %v", page, err)
	}
	unread = false
	page, err = s.PanelChats(ctx, "primary", PanelChatsQuery{Unread: &unread, Type: engine.PeerUser, Query: "PERSON 0", Limit: 2, Offset: 1})
	if err != nil || len(page.Chats) != 2 || page.Chats[0].ID != "user:8" || page.Chats[1].ID != "user:7" || !page.HasMore || page.NextOffset != 3 {
		t.Fatalf("combined filters before SQL pagination: %+v %v", page, err)
	}
	page, err = s.PanelChats(ctx, "primary", PanelChatsQuery{Type: engine.PeerChannel})
	if err != nil || len(page.Chats) != 1 || page.Chats[0].ID != "channel:100" || page.Chats[0].LastMessageAt != nil {
		t.Fatalf("dialog date must not substitute visible message date: %+v %v", page, err)
	}
	page, err = s.PanelChats(ctx, "primary", PanelChatsQuery{Offset: 500})
	if err != nil || page.Chats == nil || len(page.Chats) != 0 || page.HasMore || page.NextOffset != 0 {
		t.Fatalf("empty page: %+v %v", page, err)
	}
}

func TestPanelChatsExcludeMigratedPeersButRetainHistory(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "chat", 10, "group")
	queryPeer(t, s, "channel", 20, "supergroup")
	queryMsg(t, s, "chat", 10, 1, 1000, "migrationneedle old history")
	queryMsg(t, s, "channel", 20, 1, 1100, "migrationneedle new history")
	querySQL(t, s, `UPDATE peers SET migrated_to_type='channel',migrated_to_id=20 WHERE peer_type='chat' AND peer_id=10`)
	querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,unread_count,refreshed_at) VALUES('primary','chat',10,8,1000),('primary','channel',20,0,1000)`)
	canonical, err := s.ListChats(ctx, "primary", ChatOptions{})
	if err != nil || len(canonical) != 1 || canonical[0].Peer.Ref != (engine.PeerRef{Type: engine.PeerChannel, ID: 20}) {
		t.Fatalf("canonical migration discovery: %+v err=%v", canonical, err)
	}
	for _, q := range []PanelChatsQuery{{}, {Type: engine.PeerChat}, {Query: "chat:10"}} {
		page, err := s.PanelChats(ctx, "primary", q)
		if err != nil {
			t.Fatal(err)
		}
		for _, chat := range page.Chats {
			if chat.ID == "chat:10" {
				t.Fatalf("migrated source reappeared in discovery for %+v: %+v", q, page)
			}
		}
		if q.Type == "" && q.Query == "" && (len(page.Chats) != 1 || page.Chats[0].ID != "channel:20") {
			t.Fatalf("replacement chat missing: %+v", page)
		}
	}
	old, err := s.PanelConversation(ctx, "primary", panelTestPeer(engine.PeerChat, 10), PanelMessagesQuery{})
	if err != nil || old.Chat == nil || old.Chat.ID != "chat:10" || len(old.Messages) != 1 || old.Messages[0].Text != "migrationneedle old history" {
		t.Fatalf("direct old history changed: %+v err=%v", old, err)
	}
	search, err := s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: "migrationneedle"})
	if err != nil || len(search.Messages) != 2 || search.Messages[0].ChatID != "channel:20" || search.Messages[1].ChatID != "chat:10" {
		t.Fatalf("search must retain distinct typed histories: %+v err=%v", search, err)
	}
}

func TestPanelConversationAndSearchPagination(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "user", 1, "user")
	queryPeer(t, s, "channel", 1, "channel")
	querySQL(t, s, `UPDATE peers SET title='',first_name='Alice',last_name='Example' WHERE peer_type='user'`)
	for i := 1; i <= 135; i++ {
		// Equal dates force the complete message key to break ties.
		queryMsg(t, s, "user", 1, i, 1000, "reunião amanhã")
	}
	queryMsg(t, s, "channel", 1, 135, 1000, "reunião amanhã")
	querySQL(t, s, `UPDATE messages SET sender_type='user',sender_id=1,out=1 WHERE peer_type='user' AND msg_id=135`)
	peer := panelTestPeer(engine.PeerUser, 1)
	peer.Title = "untrusted caller metadata"
	page, err := s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{Limit: 100})
	if err != nil || page.Chat == nil || page.Chat.ID != "user:1" || page.Chat.Name != "Alice Example" || len(page.Messages) != 100 || !page.HasMore || page.NextOffset != 100 {
		t.Fatalf("conversation first page: %+v %v", page, err)
	}
	if got := page.Messages[0]; got.ID != "135" || got.ChatID != "user:1" || got.Text != "reunião amanhã" || !got.Out || got.Sender != "Alice Example" || got.Date.Unix() != 1000 {
		t.Fatalf("public message projection: %+v", got)
	}
	next, err := s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{Limit: 100, Offset: page.NextOffset})
	if err != nil || len(next.Messages) != 35 || next.HasMore || next.NextOffset != 0 || next.Messages[0].ID != "35" || next.Messages[34].ID != "1" {
		t.Fatalf("conversation final page: %+v %v", next, err)
	}
	page, err = s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{})
	if err != nil || len(page.Messages) != 30 || !page.HasMore || page.NextOffset != 30 {
		t.Fatalf("default message limit: %+v %v", page, err)
	}
	page, err = s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: `"reuniao amanha"`, Limit: 100})
	if err != nil || page.Chat != nil || len(page.Messages) != 100 || !page.HasMore || page.NextOffset != 100 || page.Messages[0].ChatID != "user:1" || page.Messages[0].ID != "135" {
		t.Fatalf("FTS deterministic first page: %+v %v", page, err)
	}
	next, err = s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: `"reuniao amanha"`, Limit: 100, Offset: 100})
	if err != nil || len(next.Messages) != 36 || next.HasMore || next.Messages[35].ChatID != "channel:1" {
		t.Fatalf("FTS final page: %+v %v", next, err)
	}
	channel := panelTestPeer(engine.PeerChannel, 1)
	page, err = s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: "reuni*", Peer: &channel})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].ChatID != "channel:1" {
		t.Fatalf("typed peer search: %+v %v", page, err)
	}
	page, err = s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{Query: "nohit"})
	if err != nil || page.Chat == nil || page.Messages == nil || len(page.Messages) != 0 || page.HasMore || page.NextOffset != 0 {
		t.Fatalf("conversation FTS filter: %+v %v", page, err)
	}
	page, err = s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: "reuni*", Offset: 999})
	if err != nil || page.Messages == nil || len(page.Messages) != 0 || page.HasMore || page.NextOffset != 0 {
		t.Fatalf("empty message page: %+v %v", page, err)
	}
}

func TestPanelReadVisibilityAtReadTime(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "user", 1, "user")
	queryPeer(t, s, "chat", 1, "group")
	queryPeer(t, s, "channel", 1, "channel")
	for i := 1; i <= 9; i++ {
		queryMsg(t, s, "user", 1, i, 1900+i, "needle")
	}
	queryMsg(t, s, "chat", 1, 1, 1950, "needle")
	queryMsg(t, s, "channel", 1, 1, 1950, "needle")
	queryMsg(t, s, "channel", 1, 2, 1990, "needle")
	querySQL(t, s, `UPDATE messages SET expires_at=2000 WHERE peer_type='user' AND msg_id=9`)
	querySQL(t, s, `UPDATE messages SET ttl_period=92,expires_at=NULL WHERE peer_type='user' AND msg_id=8`)
	querySQL(t, s, `UPDATE messages SET ttl_period=93,expires_at=3000 WHERE peer_type='user' AND msg_id=7`)
	querySQL(t, s, `UPDATE messages SET expires_at=1999 WHERE peer_type='user' AND msg_id=6`)
	querySQL(t, s, `INSERT INTO message_tombstones VALUES('primary','user',1,5,'deleted',1900),('primary','channel',1,2,'deleted',1900)`)
	querySQL(t, s, `INSERT INTO pending_deletes VALUES('primary',1,1900)`)
	querySQL(t, s, `UPDATE messages SET media_type='photo',media_ttl=1,media_remote=x'010203',media_json='{"type":"photo","downloadable":true}' WHERE peer_type='user' AND msg_id=3`)
	querySQL(t, s, `UPDATE messages SET expires_at=2001 WHERE peer_type='user' AND msg_id=4`)
	querySQL(t, s, `INSERT INTO transcripts(row_id,text,source,created_at) SELECT row_id,'spokenneedle','manual',1000 FROM messages`)
	querySQL(t, s, `UPDATE peers SET access='lost',access_hash=987654321`)
	if err := s.SetAuthState(ctx, "primary", engine.AuthRevoked, "offline fixture"); err != nil {
		t.Fatal(err)
	}
	page, err := s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: "needle OR spokenneedle", Limit: 100})
	want := []string{"channel:1/1", "user:1/4", "user:1/3", "user:1/2"}
	got := []string{}
	for _, m := range page.Messages {
		got = append(got, m.ChatID+"/"+m.ID)
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("visibility with stale FTS, TTL and deletes: %v want %v err=%v", got, want, err)
	}
	conversation, err := s.PanelConversation(ctx, "primary", panelTestPeer(engine.PeerUser, 1), PanelMessagesQuery{})
	if err != nil || len(conversation.Messages) != 3 || conversation.Chat.LastMessageAt == nil || conversation.Chat.LastMessageAt.Unix() != 1904 {
		t.Fatalf("cached inaccessible conversation: %+v %v", conversation, err)
	}
	chats, err := s.PanelChats(ctx, "primary", PanelChatsQuery{Limit: 100})
	if err != nil || len(chats.Chats) != 3 || chats.Chats[0].ID != "channel:1" || chats.Chats[0].LastMessageAt.Unix() != 1950 || chats.Chats[1].ID != "user:1" || chats.Chats[1].LastMessageAt.Unix() != 1904 || chats.Chats[2].LastMessageAt != nil {
		t.Fatalf("latest excludes every invisible representation: %+v %v", chats, err)
	}
	raw, err := json.Marshal(struct {
		Chats    PanelChatsPage    `json:"chats_page"`
		Messages PanelMessagesPage `json:"messages_page"`
	}{chats, page})
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"access_hash", "987654321", "media_remote", "row_id", "phone", "remote"} {
		if strings.Contains(string(raw), private) {
			t.Fatalf("private field %q leaked in %s", private, raw)
		}
	}
	// No cleanup or write occurs: expiration is enforced afresh on every read.
	s.now = func() time.Time { return time.Unix(2001, 0).UTC() }
	conversation, err = s.PanelConversation(ctx, "primary", panelTestPeer(engine.PeerUser, 1), PanelMessagesQuery{Query: "spokenneedle"})
	if err != nil || len(conversation.Messages) != 1 || conversation.Chat.LastMessageAt.Unix() != 1903 {
		t.Fatalf("TTL boundary conversation: %+v %v", conversation, err)
	}
	chats, err = s.PanelChats(ctx, "primary", PanelChatsQuery{Query: "user:1"})
	if err != nil || len(chats.Chats) != 1 || chats.Chats[0].LastMessageAt.Unix() != 1903 {
		t.Fatalf("TTL boundary latest: %+v %v", chats, err)
	}
}

func TestPanelReadAccountIsolationAndNoWrites(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	for _, account := range []string{"primary", "other"} {
		querySQL(t, s, `INSERT INTO peers(account,peer_type,peer_id,kind,title,access_hash,updated_at) VALUES(?,'user',1,'user',?,12345,1000)`, account, account)
		querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,unread_count,refreshed_at) VALUES(?,'user',1,?,1000)`, account, map[string]int{"primary": 7, "other": 999}[account])
		querySQL(t, s, `INSERT INTO messages(account,peer_type,peer_id,msg_id,date,text,sender_type,sender_id,source,first_seen_at,updated_at) VALUES(?,'user',1,1,1000,?,'user',1,'update',1000,1000)`, account, account+" needle")
	}
	querySQL(t, s, `INSERT INTO message_tombstones VALUES('other','user',1,1,'deleted',1000)`)
	querySQL(t, s, `INSERT INTO pending_deletes VALUES('other',1,1000)`)
	querySQL(t, s, `INSERT INTO messages(account,peer_type,peer_id,msg_id,date,text,sender_type,sender_id,source,first_seen_at,updated_at) VALUES('other','user',1,2,1900,'other visible needle','user',1,'update',1000,1000)`)
	querySQL(t, s, `INSERT INTO peer_usernames VALUES('other','user',1,'foreignalias')`)
	var before, after int64
	if err := s.writer.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	chats, err := s.PanelChats(ctx, "primary", PanelChatsQuery{})
	if err != nil || len(chats.Chats) != 1 || chats.Chats[0].Name != "primary" || chats.Chats[0].UnreadCount != 7 || chats.Chats[0].LastMessageAt == nil || chats.Chats[0].LastMessageAt.Unix() != 1000 {
		t.Fatalf("chat joins: %+v %v", chats, err)
	}
	peer := panelTestPeer(engine.PeerUser, 1)
	for _, read := range []func() (PanelMessagesPage, error){
		func() (PanelMessagesPage, error) {
			return s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{})
		},
		func() (PanelMessagesPage, error) {
			return s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: "needle"})
		},
	} {
		page, err := read()
		if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "primary needle" || page.Messages[0].Sender != "primary" {
			t.Fatalf("message/sender joins: %+v %v", page, err)
		}
	}
	for _, term := range []string{"other", "foreignalias"} {
		chats, err = s.PanelChats(ctx, "primary", PanelChatsQuery{Query: term})
		if err != nil || len(chats.Chats) != 0 {
			t.Fatalf("foreign peer matched %q: %+v %v", term, chats, err)
		}
	}
	if err := s.writer.QueryRowContext(ctx, `SELECT total_changes()`).Scan(&after); err != nil || after != before {
		t.Fatalf("reads wrote database: before=%d after=%d err=%v", before, after, err)
	}
}

func TestPanelReadValidationAndFTS(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "user", 1, "user")
	queryMsg(t, s, "user", 1, 1, 1000, "ordinary")
	querySQL(t, s, `UPDATE messages SET sender_name='senderonly'`)
	querySQL(t, s, `INSERT INTO transcripts(row_id,text,source,created_at) SELECT row_id,'orçamento secreto','manual',1000 FROM messages`)
	peer := panelTestPeer(engine.PeerUser, 1)
	for _, query := range []string{"orcamento", `"orcamento secreto"`, "orcam*"} {
		page, err := s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: query})
		if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != "ordinary" {
			t.Fatalf("real transcript FTS %q: %+v %v", query, page, err)
		}
	}
	for _, query := range []string{"senderonly", "sender:senderonly", "rcament"} {
		page, err := s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: query})
		if err != nil || len(page.Messages) != 0 {
			t.Fatalf("no sender-column or substring fallback %q: %+v %v", query, page, err)
		}
	}
	for _, query := range []string{"", " ", `"unterminated`, "' OR 1=1 --", "NEAR(ordinary, bad)", "nomatch) OR sender:senderonly OR (nomatch"} {
		if _, err := s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: query}); !errors.Is(err, engine.ErrInvalid) || strings.Contains(err.Error(), "unterminated\"") {
			t.Fatalf("invalid FTS %q: %v", query, err)
		}
		if strings.TrimSpace(query) != "" {
			if _, err := s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{Query: query}); !errors.Is(err, engine.ErrInvalid) {
				t.Fatalf("invalid conversation FTS %q: %v", query, err)
			}
		}
	}
	for _, q := range []PanelChatsQuery{{Limit: -1}, {Limit: 101}, {Offset: -1}, {Type: "unknown"}} {
		if _, err := s.PanelChats(ctx, "primary", q); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("invalid chat query %+v: %v", q, err)
		}
	}
	badPeer := panelTestPeer("unknown", 1)
	for _, q := range []PanelMessagesQuery{{Limit: -1}, {Limit: 101}, {Offset: -1}, {Peer: &badPeer}} {
		if _, err := s.PanelConversation(ctx, "primary", peer, q); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("invalid conversation query %+v: %v", q, err)
		}
		q.Query = "ordinary"
		if _, err := s.PanelSearch(ctx, "primary", q); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("invalid search query %+v: %v", q, err)
		}
	}
	for _, p := range []engine.Peer{{}, badPeer, panelTestPeer(engine.PeerUser, -1)} {
		if _, err := s.PanelConversation(ctx, "primary", p, PanelMessagesQuery{}); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("invalid peer %+v: %v", p, err)
		}
	}
	if _, err := s.PanelConversation(ctx, "primary", panelTestPeer(engine.PeerUser, 99), PanelMessagesQuery{}); !errors.Is(err, engine.ErrPeerNotFound) {
		t.Fatalf("unknown conversation: %v", err)
	}
	for _, account := range []engine.AccountID{"", "other"} {
		if _, err := s.PanelChats(ctx, account, PanelChatsQuery{}); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatal(err)
		}
		if _, err := s.PanelConversation(ctx, account, peer, PanelMessagesQuery{}); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatal(err)
		}
		if _, err := s.PanelSearch(ctx, account, PanelMessagesQuery{Query: "ordinary"}); !errors.Is(err, engine.ErrAccountMismatch) {
			t.Fatal(err)
		}
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	expired, finish := context.WithDeadline(ctx, time.Unix(1, 0))
	defer finish()
	for _, ctx := range []context.Context{canceled, expired} {
		if _, err := s.PanelChats(ctx, "primary", PanelChatsQuery{}); !errors.Is(err, ctx.Err()) {
			t.Fatalf("chats context: %v", err)
		}
		if _, err := s.PanelConversation(ctx, "primary", peer, PanelMessagesQuery{}); !errors.Is(err, ctx.Err()) {
			t.Fatalf("conversation context: %v", err)
		}
		if _, err := s.PanelSearch(ctx, "primary", PanelMessagesQuery{Query: "ordinary"}); !errors.Is(err, ctx.Err()) {
			t.Fatalf("search context: %v", err)
		}
	}
}
