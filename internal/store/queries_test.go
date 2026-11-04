package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func queryStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), "primary")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.now = func() time.Time { return time.Unix(2000, 0).UTC() }
	return s
}
func querySQL(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.writer.ExecContext(context.Background(), q, args...); err != nil {
		t.Fatal(err)
	}
}
func queryPeer(t *testing.T, s *Store, typ string, id int, kind string) {
	t.Helper()
	querySQL(t, s, `INSERT INTO peers(account,peer_type,peer_id,kind,title,search_name,updated_at) VALUES('primary',?,?,?,?,'name',1000)`, typ, id, kind, typ)
}
func queryMsg(t *testing.T, s *Store, typ string, peer, id, date int, text string) {
	t.Helper()
	querySQL(t, s, `INSERT INTO messages(account,peer_type,peer_id,msg_id,date,text,source,first_seen_at,updated_at) VALUES('primary',?,?,?,?,?,'update',1000,1000)`, typ, peer, id, date, text)
}

func TestQuerySearchContextCoverage(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "channel", 10, "supergroup")
	for i := 1; i <= 5; i++ {
		queryMsg(t, s, "channel", 10, i, 1000+i, "reunião")
	}
	querySQL(t, s, `UPDATE messages SET expires_at=1900 WHERE msg_id=2`)
	querySQL(t, s, `INSERT INTO transcripts(row_id,text,source,created_at) SELECT row_id,'orçamento secreto','manual',1000 FROM messages WHERE msg_id=3`)
	querySQL(t, s, `INSERT INTO backfill_cursors(account,peer_type,peer_id,oldest_id,newest_id,oldest_date,complete,updated_at) VALUES('primary','channel',10,1,5,1001,1,1000)`)
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 10}
	p, err := s.SearchMessages(ctx, "primary", SearchOptions{Query: "reuniao", Peer: &peer})
	if err != nil || len(p.Messages) != 4 || !p.FullText || !p.Coverage.Complete || p.Coverage.HistorySince == nil {
		t.Fatalf("text search: %+v %v", p, err)
	}
	p, err = s.SearchMessages(ctx, "primary", SearchOptions{Query: "orcamento", Peer: &peer})
	if err != nil || len(p.Messages) != 1 || p.Messages[0].Transcript != "orçamento secreto" {
		t.Fatalf("transcript: %+v %v", p, err)
	}
	querySQL(t, s, `INSERT INTO gaps(account,reason,detected_at) VALUES('primary','difference_too_long',1000)`)
	p, err = s.SearchMessages(ctx, "primary", SearchOptions{Query: "reuniao", Peer: &peer})
	if err != nil || p.Coverage.Complete || len(p.Coverage.Gaps) != 1 {
		t.Fatalf("gap: %+v %v", p, err)
	}
	m, err := s.Context(ctx, engine.MessageKey{Account: "primary", Peer: peer, ID: 3}, 2, 1)
	if err != nil || len(m) != 3 || m[0].Key.ID != 1 || m[2].Key.ID != 4 {
		t.Fatalf("context: %+v %v", m, err)
	}
	_, err = s.Context(ctx, engine.MessageKey{Account: "primary", Peer: peer, ID: 2}, 1, 1)
	if !errors.Is(err, engine.ErrMessageNotFound) {
		t.Fatalf("expired anchor: %v", err)
	}
}

func TestQueryListsMentionsAndSafeScanner(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "channel", 20, "supergroup")
	queryPeer(t, s, "user", 1, "user")
	querySQL(t, s, `UPDATE peers SET is_contact=1,title='',first_name='Ana',phone='5511' WHERE peer_type='user'`)
	querySQL(t, s, `INSERT INTO peer_usernames(account,peer_type,peer_id,username) VALUES('primary','user',1,'alias')`)
	querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,top_message_id,read_inbox_max_id,unread_count,refreshed_at) VALUES('primary','channel',20,3,0,3,1000)`)
	for i := 1; i <= 3; i++ {
		queryMsg(t, s, "channel", 20, i, 1000+i, "message")
	}
	querySQL(t, s, `UPDATE messages SET expires_at=1900 WHERE msg_id=3`)
	querySQL(t, s, `UPDATE messages SET media_type='photo',media_json='{"type":"photo","downloadable":true}',media_ttl=10,media_remote=x'0102',sender_type='user',sender_id=1,entities_json='[{"type":"bold","offset":0,"length":1}]',reply_to_msg_id=9,reply_top_id=8,location_json='{"latitude":1,"longitude":2}',poll_json='{"id":7,"question":"Q","options":[]}',forward_json='{"from_name":"hidden"}',service_type='pin_message',service_json='{"data":{"id":9}}',edit_date=1500,topic_id=8,grouped_id=123,mentioned=1,media_unread=1,silent=1,post=1,pinned=1,protected=1,views=4,forwards=2,ttl_period=3000,expires_at=4000 WHERE msg_id=1`)
	querySQL(t, s, `INSERT INTO reactions(row_id,counts_json,counts_hash,updated_at) SELECT row_id,'[{"emoji":"👍","count":2}]','h',1000 FROM messages WHERE msg_id=1`)
	querySQL(t, s, `INSERT INTO mentions(row_id,account,peer_type,peer_id,msg_id,topic_id,date,by_flag,server_unread) SELECT row_id,account,peer_type,peer_id,msg_id,topic_id,date,1,1 FROM messages`)
	querySQL(t, s, `UPDATE mentions SET answered_at=1200,answered_by=7 WHERE msg_id=2`)
	querySQL(t, s, `INSERT INTO groups_full(account,peer_type,peer_id,about,size,partial,snapshot_at) VALUES('primary','channel',20,'about',10,1,1000)`)
	querySQL(t, s, `INSERT INTO participants(account,group_type,group_id,user_type,user_id,role,joined_at,updated_at) VALUES('primary','channel',20,'user',1,'admin',900,1000)`)
	contacts, err := s.ListContacts(ctx, "primary", PeerOptions{Query: "alias"})
	if err != nil || len(contacts) != 1 || contacts[0].Phone != "5511" || len(contacts[0].Usernames) != 1 {
		t.Fatalf("contacts: %+v %v", contacts, err)
	}
	groups, err := s.ListGroups(ctx, "primary", PeerOptions{})
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups: %+v %v", groups, err)
	}
	group, ok, err := s.Group(ctx, "primary", engine.PeerRef{Type: engine.PeerChannel, ID: 20})
	if err != nil || !ok || group.About != "about" || len(group.Participants) != 1 || group.Participants[0].Name != "Ana" || !group.Partial {
		t.Fatalf("group: %+v %v %v", group, ok, err)
	}
	chats, err := s.ListChats(ctx, "primary", ChatOptions{})
	if err != nil || len(chats) != 1 || chats[0].LastMessage == nil || chats[0].LastMessage.Key.ID != 2 {
		t.Fatalf("chats: %+v %v", chats, err)
	}
	unread, err := s.ListUnread(ctx, "primary", UnreadOptions{})
	if err != nil || len(unread.Messages) != 2 {
		t.Fatalf("unread: %+v %v", unread, err)
	}
	answered := false
	mentions, err := s.ListMentions(ctx, "primary", MentionOptions{Answered: &answered})
	if err != nil || len(mentions.Mentions) != 1 || mentions.Mentions[0].Key.ID != 1 || mentions.Mentions[0].AnsweredAt != nil || !mentions.Mentions[0].ServerUnread {
		t.Fatalf("mentions: %+v %v", mentions, err)
	}
	answered = true
	mentions, err = s.ListMentions(ctx, "primary", MentionOptions{Answered: &answered})
	if err != nil || len(mentions.Mentions) != 1 || mentions.Mentions[0].AnsweredBy == nil || mentions.Mentions[0].AnsweredBy.ID != 7 {
		t.Fatalf("answered: %+v %v", mentions, err)
	}
	page, err := s.ListMessages(ctx, "primary", engine.PeerRef{Type: engine.PeerChannel, ID: 20}, MessageOptions{})
	if err != nil {
		t.Fatal(err)
	}
	m := page.Messages[1]
	if m.Media == nil || m.Media.Remote != nil || m.Media.Downloadable || m.Media.TTLSeconds != 10 || m.Sender == nil || m.Sender.ID != 1 || len(m.Entities) != 1 || m.ReplyTo.TopID != 8 || m.Location.Latitude != 1 || m.Poll.ID != 7 || m.Forward.FromName != "hidden" || m.Service.Type != "pin_message" || len(m.Reactions) != 1 || m.EditDate.Unix() != 1500 || m.ExpiresAt.Unix() != 4000 || m.GroupedID != 123 || !m.Protected || !m.MediaUnread || !m.Mentioned || !m.Silent || !m.Post || !m.Pinned || m.Views != 4 || m.Forwards != 2 || m.ChatName != "channel" {
		t.Fatalf("scanner: %+v", m)
	}
}

func TestQueryLostAccessAndFilters(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "channel", 9, "supergroup")
	querySQL(t, s, `UPDATE peers SET access='lost',access_changed_at=1700`)
	querySQL(t, s, `INSERT INTO dialogs(account,peer_type,peer_id,folder_id,unread_count,refreshed_at) VALUES('primary','channel',9,1,2,1000)`)
	for i := 1; i <= 4; i++ {
		queryMsg(t, s, "channel", 9, i, 1000+i, "alpha beta")
	}
	querySQL(t, s, `UPDATE messages SET topic_id=8,media_type='photo',sender_type='user',sender_id=2 WHERE msg_id IN (1,3)`)
	querySQL(t, s, `UPDATE messages SET out=1 WHERE msg_id=3`)
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 9}
	topic := 8
	page, err := s.ListMessages(ctx, "primary", peer, MessageOptions{TopicID: &topic, Since: time.Unix(1001, 0), Until: time.Unix(1003, 0)})
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Key.ID != 1 || page.Access != engine.AccessLost || page.AccessLostAt == nil || page.AccessLostAt.Unix() != 1700 {
		t.Fatalf("lost access / filters: %+v %v", page, err)
	}
	folder := 1
	chats, err := s.ListChats(ctx, "primary", ChatOptions{FolderID: &folder, UnreadOnly: true})
	if err != nil || len(chats) != 1 || chats[0].AccessLostAt == nil {
		t.Fatalf("chat access: %+v %v", chats, err)
	}
	unread, err := s.ListUnread(ctx, "primary", UnreadOptions{})
	if err != nil || len(unread.Messages) != 3 {
		t.Fatalf("outgoing unread: %+v %v", unread, err)
	}
	hits, err := s.SearchMessages(ctx, "primary", SearchOptions{Query: `"alpha beta"`, Peer: &peer, Media: engine.MediaPhoto, Sender: &engine.PeerRef{Type: engine.PeerUser, ID: 2}})
	if err != nil || len(hits.Messages) != 2 {
		t.Fatalf("search filters: %+v %v", hits, err)
	}
}

func TestQueryValidationAndIsolation(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	p := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	bad := []func() error{
		func() error { _, e := s.ListMessages(ctx, "primary", p, MessageOptions{Limit: -1}); return e },
		func() error {
			_, e := s.ListMessages(ctx, "primary", engine.PeerRef{Type: "bogus", ID: 1}, MessageOptions{})
			return e
		},
		func() error {
			_, e := s.Context(ctx, engine.MessageKey{Account: "primary", Peer: p, ID: 1}, -1, 0)
			return e
		},
		func() error { _, e := s.SearchMessages(ctx, "primary", SearchOptions{Query: ""}); return e },
		func() error { _, e := s.SearchMessages(ctx, "primary", SearchOptions{Query: `"broken`}); return e },
		func() error { _, e := s.ListContacts(ctx, "primary", PeerOptions{Offset: -1}); return e },
		func() error { _, _, e := s.Group(ctx, "primary", p); return e },
	}
	for i, f := range bad {
		if e := f(); !errors.Is(e, engine.ErrInvalid) {
			t.Errorf("invalid[%d]: %v", i, e)
		}
	}
	wrong := []func() error{
		func() error { _, e := s.ListMessages(ctx, "other", p, MessageOptions{}); return e },
		func() error {
			_, e := s.Context(ctx, engine.MessageKey{Account: "other", Peer: p, ID: 1}, 0, 0)
			return e
		},
		func() error { _, e := s.SearchMessages(ctx, "other", SearchOptions{Query: "alpha"}); return e },
		func() error { _, e := s.ListContacts(ctx, "other", PeerOptions{}); return e },
		func() error { _, e := s.ListGroups(ctx, "other", PeerOptions{}); return e },
		func() error { _, _, e := s.Group(ctx, "other", engine.PeerRef{Type: engine.PeerChat, ID: 1}); return e },
		func() error { _, e := s.ListChats(ctx, "other", ChatOptions{}); return e },
		func() error { _, e := s.ListUnread(ctx, "other", UnreadOptions{}); return e },
		func() error { _, e := s.ListMentions(ctx, "other", MentionOptions{}); return e },
	}
	for i, f := range wrong {
		if e := f(); !errors.Is(e, engine.ErrAccountMismatch) {
			t.Errorf("account[%d]: %v", i, e)
		}
	}
}

func TestQueryGlobalCoverageIncludesUnlistedPeers(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "user", 1, "user")
	queryMsg(t, s, "user", 1, 1, 1000, "marker")
	querySQL(t, s, `INSERT INTO backfill_cursors(account,peer_type,peer_id,oldest_id,newest_id,oldest_date,complete,updated_at) VALUES('primary','user',1,1,1,1000,1,1000)`)
	querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) VALUES('primary','channel',999,'state_lost',1000)`)
	p, err := s.SearchMessages(ctx, "primary", SearchOptions{Query: "marker"})
	if err != nil || p.Coverage.Complete || len(p.Coverage.Gaps) != 1 {
		t.Fatalf("global gap: %+v %v", p, err)
	}
}

func TestQuerySearchRechecksTTL(t *testing.T) {
	s := queryStore(t)
	queryPeer(t, s, "user", 1, "user")
	queryMsg(t, s, "user", 1, 1, 1000, "marker")
	querySQL(t, s, `UPDATE messages SET expires_at=2001`)
	calls := 0
	s.now = func() time.Time {
		calls++
		if calls == 1 {
			return time.Unix(2000, 0)
		}
		return time.Unix(2001, 0)
	}
	p, err := s.SearchMessages(context.Background(), "primary", SearchOptions{Query: "marker"})
	if err != nil || len(p.Messages) != 0 {
		t.Fatalf("TTL crossed: %+v %v", p, err)
	}
}

func TestQueryMessagesPaginationMigrationTTL(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	queryPeer(t, s, "chat", 5, "group")
	queryPeer(t, s, "channel", 5, "supergroup")
	queryPeer(t, s, "user", 5, "user")
	querySQL(t, s, `UPDATE peers SET migrated_to_type='channel',migrated_to_id=5 WHERE peer_type='chat'`)
	queryMsg(t, s, "chat", 5, 99, 900, "old")
	queryMsg(t, s, "channel", 5, 1, 1000, "new")
	queryMsg(t, s, "channel", 5, 2, 1000, "newest")
	queryMsg(t, s, "user", 5, 1, 1000, "unrelated")
	queryMsg(t, s, "channel", 5, 3, 1100, "expired")
	querySQL(t, s, `UPDATE messages SET expires_at=2000 WHERE msg_id=3`)
	p, err := s.ListMessages(ctx, "primary", engine.PeerRef{Type: engine.PeerChannel, ID: 5}, MessageOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 1 || p.Messages[0].Key.ID != 2 || p.Next == nil {
		t.Fatalf("page: %+v", p)
	}
	p, err = s.ListMessages(ctx, "primary", engine.PeerRef{Type: engine.PeerChannel, ID: 5}, MessageOptions{Limit: 10, Before: p.Next})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Messages) != 2 || p.Messages[0].Key.ID != 1 || p.Messages[1].Key.Peer.Type != engine.PeerChat || p.Next != nil {
		t.Fatalf("page 2: %+v", p)
	}
	no := false
	p, err = s.ListMessages(ctx, "primary", engine.PeerRef{Type: engine.PeerChannel, ID: 5}, MessageOptions{IncludeMigrated: &no})
	if err != nil || len(p.Messages) != 2 {
		t.Fatalf("unmerged: %+v %v", p, err)
	}
	if _, err = s.ListMessages(ctx, "other", engine.PeerRef{Type: engine.PeerChannel, ID: 5}, MessageOptions{}); !errors.Is(err, engine.ErrAccountMismatch) {
		t.Fatalf("account: %v", err)
	}
}
