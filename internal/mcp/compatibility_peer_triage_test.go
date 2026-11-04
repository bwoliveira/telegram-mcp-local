package mcp

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/index"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

// This fixture is a typed Telegram compatibility index, not the engine store
// or the deliberately non-Telegram fixture in local_test.go.
func compatibilityPeerServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "typed-peers.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	const schema = `
CREATE TABLE chats(jid TEXT PRIMARY KEY,name TEXT,last_message_ts INTEGER,archived INTEGER DEFAULT 0,pinned INTEGER DEFAULT 0,muted_until INTEGER DEFAULT 0,unread INTEGER DEFAULT 0,unread_count INTEGER DEFAULT 0);
CREATE TABLE contact_aliases(jid TEXT PRIMARY KEY,alias TEXT);
CREATE TABLE messages(rowid INTEGER PRIMARY KEY,chat_jid TEXT,chat_name TEXT,msg_id TEXT,sender_jid TEXT,sender_name TEXT,ts INTEGER,from_me INTEGER,text TEXT,display_text TEXT,quoted_msg_id TEXT,is_forwarded INTEGER DEFAULT 0,reaction_to_id TEXT,reaction_emoji TEXT,media_type TEXT,media_caption TEXT,filename TEXT,mime_type TEXT,deleted_at INTEGER,edited INTEGER DEFAULT 0,revoked INTEGER DEFAULT 0);
`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	ts := time.Now().Add(-time.Minute).Unix()
	for _, peer := range []string{"user:42", "chat:42", "channel:42"} {
		if _, err := db.Exec(`INSERT INTO chats(jid,name,last_message_ts,unread_count) VALUES(?,?,?,1)`, peer, "Named "+peer, ts); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO messages(chat_jid,msg_id,sender_jid,sender_name,ts,from_me,text) VALUES(?,'1','user:99','Sender',?,0,'Can you review this?')`, peer, ts); err != nil {
			t.Fatal(err)
		}
	}
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
	return New(Config{Index: idx, State: st})
}

func TestCompatibilityTypedPeersTriageDispatcher(t *testing.T) {
	s := compatibilityPeerServer(t)
	if s.readStore != nil || s.eng != nil {
		t.Fatal("test must use only Config.Index, without ReadStore or engine")
	}
	for _, tc := range []struct {
		name string
		tool string
		args map[string]any
		want map[string]bool
	}{
		{"unread classifies collective peers", "list_unread", nil, map[string]bool{"user:42": false, "chat:42": true, "channel:42": true}},
		{"unanswered includes collective peers", "list_unanswered", map[string]any{"include_groups": true}, map[string]bool{"user:42": false, "chat:42": true, "channel:42": true}},
		{"unanswered excludes collective peers", "list_unanswered", map[string]any{"include_groups": false}, map[string]bool{"user:42": false}},
		{"unanswered defaults to direct peers", "list_unanswered", nil, map[string]bool{"user:42": false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := toolCall(t, s, tc.tool, tc.args)
			if result["isError"] != false {
				t.Fatalf("dispatcher error: %v", result)
			}
			body := toolBody(t, result)
			if body["source"] != "legacy_local_index" || body["telegram_verified"] != false {
				t.Fatalf("not the compatibility route: %v", body)
			}
			chats, ok := body["chats"].([]any)
			if !ok {
				t.Fatalf("missing chats: %v", body)
			}
			if len(chats) != len(tc.want) || body["count"] != float64(len(tc.want)) {
				t.Errorf("chats/count = %v/%v; want %d", chats, body["count"], len(tc.want))
			}
			seen := map[string]bool{}
			for _, raw := range chats {
				row := raw.(map[string]any)
				id, _ := row["chat_id"].(string)
				wantGroup, wanted := tc.want[id]
				if !wanted || seen[id] {
					t.Errorf("unexpected or duplicate peer: %v", row)
				}
				seen[id] = true
				if row["group"] != wantGroup {
					t.Errorf("%s group = %v, want %v", id, row["group"], wantGroup)
				}
			}
			for id := range tc.want {
				if !seen[id] {
					t.Errorf("missing peer %s", id)
				}
			}
		})
	}
}

func TestCompatibilityIndexHasNoOwnIdentity(t *testing.T) {
	s := compatibilityPeerServer(t)
	if ids := s.ownIDs(context.Background()); len(ids) != 0 {
		t.Fatalf("compatibility index invented account identity: %v", ids)
	}
}
