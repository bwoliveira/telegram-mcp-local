package mcp

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/index"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

// This fixture deliberately is NOT Telegram: it protects the useful local
// behavior during the inherited-index migration without claiming parity.
const localLegacySchema = `
CREATE TABLE chats(jid TEXT PRIMARY KEY,kind TEXT,name TEXT,last_message_ts INTEGER,archived INTEGER DEFAULT 0,pinned INTEGER DEFAULT 0,muted_until INTEGER DEFAULT 0,unread INTEGER DEFAULT 0,unread_count INTEGER DEFAULT 0);
CREATE TABLE contacts(jid TEXT PRIMARY KEY,phone TEXT,push_name TEXT,full_name TEXT,first_name TEXT,business_name TEXT,system_name TEXT,updated_at INTEGER);
CREATE TABLE contact_aliases(jid TEXT PRIMARY KEY,alias TEXT,notes TEXT,updated_at INTEGER);
CREATE TABLE groups(jid TEXT PRIMARY KEY,name TEXT,owner_jid TEXT,created_ts INTEGER,is_parent INTEGER DEFAULT 0,linked_parent_jid TEXT,left_at INTEGER,updated_at INTEGER);
CREATE TABLE group_participants(group_jid TEXT,user_jid TEXT,role TEXT,updated_at INTEGER);
CREATE TABLE messages(rowid INTEGER PRIMARY KEY AUTOINCREMENT,chat_jid TEXT,chat_name TEXT,msg_id TEXT,sender_jid TEXT,sender_name TEXT,ts INTEGER,from_me INTEGER,text TEXT,display_text TEXT,quoted_msg_id TEXT,quoted_sender_jid TEXT,is_forwarded INTEGER DEFAULT 0,forwarding_score INTEGER DEFAULT 0,reaction_to_id TEXT,reaction_emoji TEXT,media_type TEXT,media_caption TEXT,filename TEXT,mime_type TEXT,direct_path TEXT,local_path TEXT,revoked INTEGER DEFAULT 0,deleted_for_me INTEGER DEFAULT 0,deleted_at INTEGER,edited INTEGER DEFAULT 0);
`
const legacyChat = "legacy-user@example.invalid"

func legacyServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(localLegacySchema); err != nil {
		t.Fatal(err)
	}
	exec := func(q string, a ...any) {
		if _, err := db.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO chats(jid,kind,name,last_message_ts,pinned)VALUES(?,'dm','Legacy example',100,1)`, legacyChat)
	exec(`INSERT INTO chats(jid,kind,name,last_message_ts)VALUES('other-legacy','dm','Newer unpinned',200)`)
	for i := 1; i <= 10; i++ {
		exec(`INSERT INTO messages(chat_jid,msg_id,ts,from_me,text,media_type)VALUES(?,?,?,0,?,'')`, legacyChat, fmt.Sprintf("L%d", i), i, fmt.Sprintf("local needle %d", i))
	}
	exec(`INSERT INTO messages(chat_jid,msg_id,ts,from_me,text,media_type)VALUES(?,'AUDIO1',11,0,'','audio')`, legacyChat)
	exec(`INSERT INTO messages(chat_jid,msg_id,ts,from_me,text,media_type)VALUES('other-legacy','OTHER',200,0,'newer','')`)
	exec(`INSERT INTO groups(jid,name,created_ts,is_parent,updated_at)VALUES('legacy-group@example.invalid','Cached legacy group',1,0,1)`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	idx, err := index.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = idx.Close() })
	st, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SaveTranscript(context.Background(), state.Transcript{ChatJID: legacyChat, MessageID: "AUDIO1", Text: "cached transcript", Raw: "raw transcript", Source: "local"}); err != nil {
		t.Fatal(err)
	}
	return New(Config{Index: idx, State: st, MediaDir: filepath.Join(dir, "media"), ExportDir: filepath.Join(dir, "exports")})
}
func TestLegacyCachedTranscriptsAndCorrectionsRemainLocal(t *testing.T) {
	s := legacyServer(t)
	a := map[string]any{"chat_id": legacyChat, "message_id": "AUDIO1"}
	r := toolCall(t, s, "transcribe_audio", a)
	b := toolBody(t, r)
	if r["isError"] != false || b["cached"] != true || b["telegram_verified"] != false || b["source"] != "legacy_local_index" {
		t.Fatal(r)
	}
	a["text"] = "corrected transcript"
	r = toolCall(t, s, "save_transcript", a)
	if r["isError"] != false {
		t.Fatal(r)
	}
	saved, err := s.state.Transcript(context.Background(), legacyChat, "AUDIO1")
	if err != nil || saved.Text != "corrected transcript" || saved.Raw != "raw transcript" {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	// Those same legacy IDs must never reach a Telegram edit/download operation.
	for _, name := range []string{"edit_message", "download_media"} {
		if toolCall(t, s, name, a)["isError"] != true {
			t.Fatalf("legacy identity accepted for %s", name)
		}
	}
}
func TestLegacyBoundedReadsAndPinnedChats(t *testing.T) {
	s := legacyServer(t)
	for _, tc := range []struct{ order, want string }{{"oldest", "L1"}, {"newest", "AUDIO1"}} {
		r := toolCall(t, s, "get_chat_messages", map[string]any{"chat_id": legacyChat, "limit": 1, "order": tc.order})
		b := toolBody(t, r)
		if r["isError"] != false {
			t.Fatal(r)
		}
		msgs := b["messages"].([]any)
		if len(msgs) != 1 || msgs[0].(map[string]any)["id"] != tc.want {
			t.Fatal(b)
		}
	}
	r := toolCall(t, s, "search_messages", map[string]any{"chat_id": legacyChat, "query": "needle", "limit": 2})
	b := toolBody(t, r)
	if r["isError"] != false || b["count"] != float64(2) || b["truncated"] != true || b["telegram_verified"] != false {
		t.Fatal(r)
	}
	r = toolCall(t, s, "list_chats", map[string]any{"limit": 1, "fields": []string{"chat_id", "archived", "pinned"}})
	b = toolBody(t, r)
	c := b["chats"].([]any)[0].(map[string]any)
	if c["chat_id"] != legacyChat || c["archived"] != false || c["pinned"] != true {
		t.Fatal(b)
	}
	r = toolCall(t, s, "get_group", map[string]any{"group_id": "legacy-group@example.invalid", "live": false})
	if r["isError"] != false || toolBody(t, r)["telegram_verified"] != false {
		t.Fatal(r)
	}
}
func TestDownloadInventoryAndScopedPurge(t *testing.T) {
	s, _ := newFakeServer(t, "read_only")
	r := toolCall(t, s, "download_media", map[string]any{"chat_id": "user:1001", "message_id": "4"})
	if r["isError"] != false {
		t.Fatal(r)
	}
	r = toolCall(t, s, "media_stats", nil)
	if r["isError"] != false {
		t.Fatal(r)
	}
	inventory := toolBody(t, r)["inventory"].(map[string]any)
	if inventory["files"] != float64(1) {
		t.Fatal(inventory)
	}
	// The engine download directory and inventory ownership agree even without
	// an inherited index or state dependency.
	got, _, _, err := s.PurgeMedia("user:1001", 0, 0, true)
	if err != nil || got != 1 {
		t.Fatalf("scoped preview files=%d err=%v", got, err)
	}
	got, _, _, err = s.PurgeMedia("user:1001", 0, 0, false)
	if err != nil || got != 1 {
		t.Fatalf("scoped purge files=%d err=%v", got, err)
	}
	if toolCall(t, New(Config{}), "media_stats", nil)["isError"] != true {
		t.Fatal("missing media directory silently succeeded")
	}
}
func TestLegacyLookupRequiresChatEvenWithDuplicateMessageIDs(t *testing.T) {
	s := legacyServer(t)
	if _, fail := s.message(context.Background(), arguments{MessageID: "AUDIO1"}); fail == nil {
		t.Fatal("ID-only local lookup was accepted")
	}
	// Guard that the bounded read never has to wait for an unbounded engine call.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, fail := s.message(ctx, arguments{ChatJID: legacyChat, MessageID: "AUDIO1"}); fail != nil {
		t.Fatal(fail)
	}
}
