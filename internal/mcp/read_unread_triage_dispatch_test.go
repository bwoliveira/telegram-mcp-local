package mcp

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// Exercise tools/call, durable marks and unread snapshots together, without a
// legacy index/state or a Telegram connection.
func unreadDispatchSQLite(t *testing.T) (*Server, *store.Store, *time.Time, string) {
	t.Helper()
	now := fake.Epoch().Add(24 * time.Hour)
	path := filepath.Join(t.TempDir(), "unread.db")
	st, err := store.Open(context.Background(), path, "primary", store.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctrl, err := fake.New(engine.Config{Account: "primary", Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	f := ctrl.(*fake.Controller)
	if err := f.Disconnect(context.Background(), "offline unread regression"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if len(f.Writes()) != 0 {
			t.Fatalf("local unread triage wrote Telegram: %v", f.Writes())
		}
	})
	return New(Config{Engine: f, ReadStore: st, Clock: func() time.Time { return now }}), st, &now, path
}

func unreadDispatchSeed(t *testing.T, st *store.Store, now time.Time, peers ...engine.PeerRef) {
	t.Helper()
	batch := engine.Batch{Account: "primary", Source: engine.SourceBackfill}
	for i, peer := range peers {
		batch.Peers = append(batch.Peers, engine.PeerUpsert{Peer: engine.Peer{Ref: peer, Kind: engine.KindUser}})
		batch.Dialogs = append(batch.Dialogs, engine.Dialog{Peer: peer, UnreadCount: i + 1})
		batch.Messages = append(batch.Messages, engine.Message{
			Key:  engine.MessageKey{Account: "primary", Peer: peer, ID: 1},
			Date: now.Add(-time.Duration(i+1) * time.Minute), Text: "unread question?",
		})
	}
	if _, err := st.Apply(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

func unreadDispatchMark(t *testing.T, s *Server, tool string, peer engine.PeerRef, now time.Time) {
	t.Helper()
	args := map[string]any{"chat_id": peerID(peer)}
	if tool == "snooze_chat" {
		args["until"] = now.Add(time.Hour).Format(time.RFC3339)
	}
	paritySuccess(t, toolCall(t, s, tool, args))
}

func unreadDispatchExpect(t *testing.T, s *Server, args map[string]any, wantIDs ...string) map[string]any {
	t.Helper()
	body := paritySuccess(t, toolCall(t, s, "list_unread", args))
	rows := body["chats"].([]any)
	if len(rows) != len(wantIDs) || body["count"] != float64(len(wantIDs)) {
		t.Fatalf("unread chats want %v: %v", wantIDs, body)
	}
	for i, id := range wantIDs {
		if rows[i].(map[string]any)["chat_id"] != id {
			t.Fatalf("unread order want %v: %v", wantIDs, rows)
		}
	}
	return body
}

func TestUnreadDispatchTriageFiltersBeforePaginationSQLite(t *testing.T) {
	for _, tool := range []string{"mark_handled", "snooze_chat"} {
		t.Run(tool, func(t *testing.T) {
			s, st, now, _ := unreadDispatchSQLite(t)
			peers := []engine.PeerRef{{Type: engine.PeerUser, ID: 1}, {Type: engine.PeerChat, ID: 1}, {Type: engine.PeerUser, ID: 3}}
			unreadDispatchSeed(t, st, *now, peers...)
			unreadDispatchExpect(t, s, map[string]any{"limit": 1}, "user:1")
			for _, peer := range peers[:2] {
				unreadDispatchMark(t, s, tool, peer, *now)
			}
			body := unreadDispatchExpect(t, s, map[string]any{"limit": 1}, "user:3")
			if body["unread_messages"] != float64(3) || body["truncated"] != false {
				t.Fatalf("suppressed chats affected counters/pagination: %v", body)
			}
			// Local triage must not change Telegram's cached unread counters.
			dialogs, err := st.ReadDialogs(context.Background(), "primary")
			if err != nil || len(dialogs) != 3 {
				t.Fatal(dialogs, err)
			}
			for _, d := range dialogs {
				if d.UnreadCount <= 0 {
					t.Fatalf("triage changed the Telegram unread counter: %+v", d)
				}
			}
			paritySuccess(t, toolCall(t, s, "mark_handled", map[string]any{"chat_id": "user:1", "clear": true}))
			unreadDispatchExpect(t, s, map[string]any{"limit": 1}, "user:1")
		})
	}
}

func TestUnreadDispatchTriageNewIncomingResurfacesSQLite(t *testing.T) {
	for _, tool := range []string{"mark_handled", "snooze_chat"} {
		t.Run(tool, func(t *testing.T) {
			s, st, now, _ := unreadDispatchSQLite(t)
			peer := engine.PeerRef{Type: engine.PeerUser, ID: 1}
			unreadDispatchSeed(t, st, *now, peer)
			unreadDispatchMark(t, s, tool, peer, *now)
			markedAt := *now
			*now = now.Add(10 * time.Second)
			unreadDispatchExpect(t, s, nil)
			apply := func(m engine.Message) {
				t.Helper()
				if _, err := st.Apply(context.Background(), engine.Batch{Account: "primary", Source: engine.SourceBackfill, Messages: []engine.Message{m}}); err != nil {
					t.Fatal(err)
				}
			}
			// Outgoing activity, older backfill, an equal-timestamp incoming and
			// expired incoming content must not cancel a local decision.
			apply(engine.Message{Key: engine.MessageKey{Account: "primary", Peer: peer, ID: 2}, Date: markedAt.Add(time.Second), Out: true})
			apply(engine.Message{Key: engine.MessageKey{Account: "primary", Peer: peer, ID: 3}, Date: markedAt.Add(-time.Hour)})
			apply(engine.Message{Key: engine.MessageKey{Account: "primary", Peer: peer, ID: 4}, Date: markedAt})
			expired := now.Add(-time.Second)
			apply(engine.Message{Key: engine.MessageKey{Account: "primary", Peer: peer, ID: 5}, Date: markedAt.Add(2 * time.Second), ExpiresAt: &expired})
			unreadDispatchExpect(t, s, nil)
			apply(engine.Message{Key: engine.MessageKey{Account: "primary", Peer: peer, ID: 6}, Date: markedAt.Add(3 * time.Second), Text: "new incoming question?"})
			unreadDispatchExpect(t, s, nil, "user:1")
		})
	}
}

func TestUnreadDispatchSnoozeDeadlineResurfacesSQLite(t *testing.T) {
	s, st, now, _ := unreadDispatchSQLite(t)
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	unreadDispatchSeed(t, st, *now, peer)
	unreadDispatchMark(t, s, "snooze_chat", peer, *now)
	deadline := now.Add(time.Hour)
	*now = deadline.Add(-time.Nanosecond)
	unreadDispatchExpect(t, s, nil)
	*now = deadline
	unreadDispatchExpect(t, s, nil, "user:1")
	*now = deadline.Add(time.Second)
	unreadDispatchExpect(t, s, nil, "user:1")
}

func TestUnreadDispatchTriageAccountAndPeerScopeSQLite(t *testing.T) {
	s, st, now, path := unreadDispatchSQLite(t)
	user := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	chat := engine.PeerRef{Type: engine.PeerChat, ID: 1}
	unreadDispatchSeed(t, st, *now, user, chat)
	// Initialize the additive triage table, then seed a foreign namespace in
	// this very SQLite file. list_unread must request the active account's marks.
	if _, err := st.ReadTriageMarks(context.Background(), "primary"); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`INSERT INTO peers(account,peer_type,peer_id,kind,updated_at) VALUES('secondary','user',1,'user',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`INSERT INTO triage(account,peer_type,peer_id,handled_at,note) VALUES('secondary','user',1,?,'foreign account')`, now.UnixNano()); err != nil {
		t.Fatal(err)
	}
	unreadDispatchExpect(t, s, nil, "user:1", "chat:1")
	unreadDispatchMark(t, s, "mark_handled", user, *now)
	unreadDispatchExpect(t, s, nil, "chat:1")
	var foreignNote string
	if err := raw.QueryRow(`SELECT note FROM triage WHERE account='secondary'`).Scan(&foreignNote); err != nil || foreignNote != "foreign account" {
		t.Fatalf("foreign account changed: %q %v", foreignNote, err)
	}
}
