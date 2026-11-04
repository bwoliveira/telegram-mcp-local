package gotdengine

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// Reproduce the real-account update shape with synthetic content only: unlike
// hidden/same-date reaction refreshes, EditHide is false and edit_date advances.
// No client is started; every message crosses the real converter and SQLite.
func TestRealReactionEditAdvancingVisibleRevision(t *testing.T) {
	for _, channel := range []bool{false, true} {
		for _, edited := range []bool{false, true} {
			for _, legacy := range []bool{false, true} {
				t.Run(fmt.Sprintf("channel=%v/edited=%v/legacy=%v", channel, edited, legacy), func(t *testing.T) {
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					now := time.Unix(3000, 0)
					path := filepath.Join(t.TempDir(), "visible-reactions.db")
					open := func() *store.Store {
						t.Helper()
						db, err := store.Open(ctx, path, "primary", store.WithClock(func() time.Time { return now }))
						if err != nil {
							t.Fatal(err)
						}
						return db
					}
					db := open()
					defer func() { _ = db.Close() }()
					e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Session: &memorySession{}, Store: db, Clock: func() time.Time { return now }})
					if err != nil {
						t.Fatal(err)
					}
					e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
					if err := db.BindSelf(ctx, "primary", e.account); err != nil {
						t.Fatal(err)
					}
					e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
						t.Errorf("unexpected RPC in offline regression: %T", in)
						return fmt.Errorf("offline regression rejects RPC %T", in)
					}))
					var peer tg.PeerClass = &tg.PeerUser{UserID: 10}
					if channel {
						peer = &tg.PeerChannel{ChannelID: 20}
					}
					raw := &tg.Message{ID: 8, PeerID: peer, Date: 1000, Message: "Synthetic original"}
					var seed tg.UpdateClass = &tg.UpdateNewMessage{Message: raw}
					if channel {
						seed = &tg.UpdateNewChannelMessage{Message: raw}
					}
					if err := e.handle(ctx, &tg.UpdateShort{Update: seed}); err != nil {
						t.Fatal(err)
					}
					key := engine.MessageKey{Account: "primary", Peer: peerRef(peer), ID: raw.ID}
					events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
					assertEvents := func(want ...engine.EventKind) {
						t.Helper()
						var got []engine.EventKind
						for len(events) > 0 {
							ev := <-events
							got = append(got, ev.Kind)
							if ev.Message != nil && ev.Message.Key != key || ev.Reaction != nil && ev.Reaction.Key != key {
								t.Errorf("wrong event identity: %+v", ev)
							}
						}
						if fmt.Sprint(got) != fmt.Sprint(want) {
							t.Errorf("events = %v, want %v", got, want)
						}
					}
					edit := func(m *tg.Message) {
						t.Helper()
						var u tg.UpdateClass = &tg.UpdateEditMessage{Message: m}
						if channel {
							u = &tg.UpdateEditChannelMessage{Message: m}
						}
						if err := e.handle(ctx, &tg.UpdateShort{Update: u}); err != nil {
							t.Fatal(err)
						}
					}
					read := func() engine.Message {
						t.Helper()
						m, ok, err := db.Message(ctx, key)
						if err != nil || !ok {
							t.Fatalf("read: found=%v err=%v", ok, err)
						}
						return m
					}
					version := func() int {
						t.Helper()
						reader, err := sql.Open("sqlite", path)
						if err != nil {
							t.Fatal(err)
						}
						defer reader.Close()
						var v int
						if err := reader.QueryRowContext(ctx, `SELECT version FROM messages WHERE account='primary' AND peer_type=? AND peer_id=? AND msg_id=8`, key.Peer.Type, key.Peer.ID).Scan(&v); err != nil {
							t.Fatal(err)
						}
						return v
					}
					wantEdits := int64(0)
					if edited {
						raw.Message = "Synthetic genuinely edited"
						raw.SetEditDate(1100)
						edit(raw)
						assertEvents(engine.EventEdit)
						wantEdits = 1
					}
					before := read()
					beforeVersion := version()
					if legacy {
						// Exercise a real v4 migration, not an invented adapter hash.
						if err := db.Close(); err != nil {
							t.Fatal(err)
						}
						old, err := sql.Open("sqlite", path)
						if err != nil {
							t.Fatal(err)
						}
						defer old.Close()
						for _, statement := range []string{"ALTER TABLE messages DROP COLUMN content_hash", "ALTER TABLE messages DROP COLUMN server_edit_date", "PRAGMA user_version=4"} {
							if _, err := old.ExecContext(ctx, statement); err != nil {
								t.Fatal(err)
							}
						}
						if err := old.Close(); err != nil {
							t.Fatal(err)
						}
						db = open()
						e.cfg.Store = db
						if read().ContentHash != "" {
							t.Fatal("legacy migration unexpectedly initialized content hash")
						}
					}
					assertState := func(revision int64, reactions int) {
						t.Helper()
						after := read()
						if after.Text != before.Text || after.ContentHash != before.ContentHash || version() != beforeVersion {
							t.Errorf("reaction changed retained content/version: text=%q hash=%q version=%d, want %q/%q/%d", after.Text, after.ContentHash, version(), before.Text, before.ContentHash, beforeVersion)
						}
						if (after.EditDate == nil) != (before.EditDate == nil) || before.EditDate != nil && !after.EditDate.Equal(*before.EditDate) {
							t.Errorf("reaction changed last genuine visible edit: before=%v after=%v", before.EditDate, after.EditDate)
						}
						if after.ServerEditDate == nil || after.ServerEditDate.Unix() != revision {
							t.Errorf("server ordering = %v, want %d", after.ServerEditDate, revision)
						}
						if len(after.Reactions) != reactions || e.metrics.Edits != wantEdits {
							t.Errorf("reaction state/edits = %v/%d, want %d reactions/%d edits", after.Reactions, e.metrics.Edits, reactions, wantEdits)
						}
					}
					incoming := *raw
					incoming.EditHide = false
					incoming.SetEditDate(1200)
					incoming.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}}})
					edit(&incoming)
					assertEvents(engine.EventReaction)
					assertState(1200, 1)
					edit(&incoming)
					assertEvents()
					assertState(1200, 1)

					// Empty message copies are non-authoritative reaction enrichment:
					// they must not invent an edit or erase a known reaction. The
					// matching standalone reaction update authoritatively removes it.
					incoming.SetEditDate(1300)
					incoming.SetReactions(tg.MessageReactions{})
					edit(&incoming)
					assertEvents()
					assertState(1300, 1)
					removal := &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: peer, MsgID: raw.ID, Reactions: tg.MessageReactions{}}}
					if err := e.handle(ctx, removal); err != nil {
						t.Fatal(err)
					}
					assertEvents(engine.EventReaction)
					assertState(1300, 0)

					// Revision ordering and the visible marker must survive reopen.
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
					db = open()
					e.cfg.Store = db
					edit(&incoming)
					assertEvents()
					assertState(1300, 0)
					incoming.SetEditDate(1250)
					incoming.Message = "Synthetic stale content"
					incoming.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 2}}})
					edit(&incoming)
					assertEvents()
					assertState(1300, 0)

					// A genuinely changed visible payload must still advance the
					// content version and emit exactly one edit after these refreshes.
					incoming.SetEditDate(1400)
					incoming.Message = "Synthetic next genuine edit"
					incoming.SetReactions(tg.MessageReactions{})
					edit(&incoming)
					assertEvents(engine.EventEdit)
					after := read()
					if after.Text != incoming.Message || after.EditDate == nil || after.EditDate.Unix() != 1400 || after.ServerEditDate == nil || after.ServerEditDate.Unix() != 1400 || version() != beforeVersion+1 || e.metrics.Edits != wantEdits+1 {
						t.Errorf("genuine edit lost: message=%+v version=%d edits=%d", after, version(), e.metrics.Edits)
					}
					edit(&incoming)
					assertEvents()
				})
			}
		}
	}
}
