package mcp

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

func migrationChatServer(t *testing.T, b engine.Batch) (*Server, *store.Store, *fake.Controller) {
	t.Helper()
	b.Account = "primary"
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "store.db"), b.Account)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Apply(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	f, err := fake.New(engine.Config{Account: b.Account})
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{Engine: f, ReadStore: db}), db, f.(*fake.Controller)
}

func TestChatMigrationListingUsesPersistedLink(t *testing.T) {
	old := engine.PeerRef{Type: engine.PeerChat, ID: 71}
	next := engine.PeerRef{Type: engine.PeerChannel, ID: 81}
	for _, source := range []string{"dialogs", "peer_only", "message_only"} {
		t.Run(source, func(t *testing.T) {
			// The real SQLite snapshot already has the migration link and both
			// dialogs, without a stored migration service message. Test that
			// persisted shape, not an inferred Telegram TL constructor.
			b := engine.Batch{Peers: []engine.PeerUpsert{
				{Peer: engine.Peer{Ref: old, Kind: engine.KindGroup, Title: "Migration fixture", Access: engine.AccessOK, MigratedTo: &next}},
				{Peer: engine.Peer{Ref: next, Kind: engine.KindSupergroup, Title: "Migration fixture", Access: engine.AccessOK, Forum: true}},
			}, Dialogs: []engine.Dialog{{Peer: next, TopMessageID: 8}}}
			if source == "dialogs" {
				b.Dialogs = append(b.Dialogs, engine.Dialog{Peer: old, TopMessageID: 23448, Pinned: true})
			}
			if source == "message_only" {
				b.Messages = []engine.Message{{Key: engine.MessageKey{Account: "primary", Peer: old, ID: 7}, Date: time.Unix(1000, 0), Text: "Retained predecessor history"}}
			}
			s, db, f := migrationChatServer(t, b)
			for _, args := range []map[string]any{
				{"limit": 1},
				{"search": "Migration fixture", "limit": 1},
				{"query": "Migration fixture", "fields": []string{"chat_id", "kind"}, "limit": 1},
			} {
				body := messagesBody(t, s, "list_chats", args)
				rows := body["chats"].([]any)
				if body["total"] != float64(1) || body["count"] != float64(1) || body["has_more"] != false || body["next_offset"] != nil || len(rows) != 1 {
					t.Fatalf("migrated predecessor changed totals or pagination: %v", body)
				}
				row := rows[0].(map[string]any)
				if row["chat_id"] != "channel:81" || row["kind"] != "supergroup" {
					t.Fatalf("expected only the successor, got %v", row)
				}
			}
			end := messagesBody(t, s, "list_chats", map[string]any{"offset": 1})
			if end["count"] != float64(0) || end["has_more"] != false {
				t.Fatalf("predecessor leaked into a later page: %v", end)
			}
			p, ok, err := db.LookupPeer(context.Background(), "primary", old)
			if err != nil || !ok || p.MigratedTo == nil || *p.MigratedTo != next {
				t.Fatal("listing lost the persisted predecessor identity")
			}
			if source == "message_only" {
				for _, ref := range []string{"chat:71", "channel:81"} {
					history := messagesBody(t, s, "get_chat_messages", map[string]any{"chat_id": ref})
					rows := history["messages"].([]any)
					if len(rows) != 1 || rows[0].(map[string]any)["chat_id"] != "chat:71" {
						t.Fatalf("migration lost typed history: %v", history)
					}
				}
			}
			if len(f.Writes()) != 0 {
				t.Fatal("listing or history performed a Telegram write")
			}
		})
	}
}

// Opt-in evidence check: only public peer metadata is read from a caller-supplied
// backup using mode=ro. The live store is never opened by store.Open; all test
// writes go to a new isolated temporary database. No Telegram client is started.
func TestChatMigrationReadOnlySnapshot(t *testing.T) {
	path := os.Getenv("TELEGRAM_MCP_MIGRATION_SNAPSHOT")
	if path == "" {
		t.Skip("set TELEGRAM_MCP_MIGRATION_SNAPSHOT to a private SQLite backup")
	}
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}
	snapshot, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	rows, err := snapshot.Query(`SELECT p.peer_id,p.title,p.migrated_to_id,n.title,n.forum
 FROM peers p JOIN peers n ON n.account=p.account AND n.peer_type=p.migrated_to_type AND n.peer_id=p.migrated_to_id
 WHERE p.account='primary' AND p.peer_type='chat' AND p.migrated_to_type='channel' AND n.kind='supergroup'`)
	if err != nil {
		t.Fatal(err)
	}
	b := engine.Batch{}
	successors := map[engine.PeerRef]bool{}
	for rows.Next() {
		old := engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChat}, Kind: engine.KindGroup, Access: engine.AccessOK}
		next := engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChannel}, Kind: engine.KindSupergroup, Access: engine.AccessOK}
		if err := rows.Scan(&old.Ref.ID, &old.Title, &next.Ref.ID, &next.Title, &next.Forum); err != nil {
			t.Fatal(err)
		}
		old.MigratedTo = &next.Ref
		b.Peers = append(b.Peers, engine.PeerUpsert{Peer: old})
		if !successors[next.Ref] {
			successors[next.Ref] = true
			b.Peers = append(b.Peers, engine.PeerUpsert{Peer: next})
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(successors) == 0 {
		t.Fatal("snapshot has no linked migrated supergroup")
	}
	for _, p := range b.Peers {
		var d engine.Dialog
		d.Peer = p.Peer.Ref
		err := snapshot.QueryRow(`SELECT top_message_id,read_inbox_max_id,read_outbox_max_id FROM dialogs WHERE account='primary' AND peer_type=? AND peer_id=?`, d.Peer.Type, d.Peer.ID).Scan(&d.TopMessageID, &d.ReadInboxMaxID, &d.ReadOutboxMaxID)
		if err == nil {
			b.Dialogs = append(b.Dialogs, d)
		} else if err != sql.ErrNoRows {
			t.Fatal(err)
		}
	}
	s, db, _ := migrationChatServer(t, b)
	local, err := db.ListChats(context.Background(), "primary", store.ChatOptions{})
	if err != nil || len(local) != len(successors) {
		t.Fatalf("store query did not hide linked predecessors: count=%d err=%v", len(local), err)
	}
	body := messagesBody(t, s, "list_chats", map[string]any{"limit": 100, "fields": []string{"chat_id", "kind"}})
	if body["total"] != float64(len(successors)) || body["has_more"] != false {
		t.Fatalf("linked snapshot predecessors still listed: %v", body)
	}
	for _, raw := range body["chats"].([]any) {
		if raw.(map[string]any)["kind"] != "supergroup" {
			t.Fatalf("unexpected predecessor in snapshot listing: %v", raw)
		}
	}
	t.Logf("read-only snapshot: %d linked peers, %d persisted dialogs, %d visible successors", len(b.Peers), len(b.Dialogs), len(successors))
}
