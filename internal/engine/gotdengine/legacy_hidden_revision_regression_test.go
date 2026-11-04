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
	"github.com/gotd/td/tg"
)

// Upgrade a real v4-shaped SQLite row before feeding actual edit constructors.
// A blank legacy hash is unknown identity, not proof that the payload changed.
func TestLegacyHiddenRevisionAfterMigration(t *testing.T) {
	for _, channel := range []bool{false, true} {
		for _, edited := range []bool{false, true} {
			for _, kind := range []string{"text", "document", "photo", "poll", "temporary_document"} {
				for _, changed := range []bool{false, true} {
					if kind == "temporary_document" && changed {
						continue // The redacted payload cannot prove a hidden content change.
					}
					t.Run(fmt.Sprintf("channel=%v/edited=%v/%s/changed=%v", channel, edited, kind, changed), func(t *testing.T) {
						ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
						defer cancel()
						now := time.Unix(3000, 0)
						path := filepath.Join(t.TempDir(), "legacy.db")
						open := func() *store.Store {
							t.Helper()
							db, err := store.Open(ctx, path, "primary", store.WithClock(func() time.Time { return now }))
							if err != nil {
								t.Fatal(err)
							}
							return db
						}
						db := open()
						var peer tg.PeerClass = &tg.PeerUser{UserID: 10}
						if channel {
							peer = &tg.PeerChannel{ChannelID: 20}
						}
						raw := &tg.Message{ID: 8, PeerID: peer, Date: 1000, Message: "same"}
						if edited {
							raw.SetEditDate(1100)
						}
						switch kind {
						case "document":
							raw.Media = &tg.MessageMediaDocument{Document: &tg.Document{ID: 101, MimeType: "application/pdf", Size: 10, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "before.pdf"}}}}
						case "temporary_document":
							raw.Media = &tg.MessageMediaDocument{TTLSeconds: 30, Document: &tg.Document{ID: 101, MimeType: "application/pdf", Size: 10, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "never-retain.pdf"}}}}
						case "photo":
							raw.Media = &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 101, Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", W: 10, H: 10, Size: 10}}}}
						case "poll":
							raw.Media = &tg.MessageMediaPoll{Poll: tg.Poll{ID: 101, Question: tg.TextWithEntities{Text: "Question"}, Answers: []tg.PollAnswerClass{&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Before"}, Option: []byte{1}}}}}
						}
						m, ok := convertMessage("primary", raw)
						if !ok {
							t.Fatal("conversion failed")
						}
						if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{m}}); err != nil {
							t.Fatal(err)
						}
						if err := db.Close(); err != nil {
							t.Fatal(err)
						}
						legacy, err := sql.Open("sqlite", path)
						if err != nil {
							t.Fatal(err)
						}
						for _, statement := range []string{"ALTER TABLE messages DROP COLUMN content_hash", "ALTER TABLE messages DROP COLUMN server_edit_date", "PRAGMA user_version=4"} {
							if _, err := legacy.ExecContext(ctx, statement); err != nil {
								t.Fatal(err)
							}
						}
						if err := legacy.Close(); err != nil {
							t.Fatal(err)
						}
						db = open()
						defer db.Close()
						e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Session: &memorySession{}, Store: db, Clock: func() time.Time { return now }})
						if err != nil {
							t.Fatal(err)
						}
						e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
						if err := db.BindSelf(ctx, "primary", e.account); err != nil {
							t.Fatal(err)
						}
						before, _, err := db.Message(ctx, m.Key)
						if err != nil {
							t.Fatal(err)
						}
						if before.ContentHash != "" {
							t.Fatal("migration unexpectedly initialized identity")
						}
						version := func() int {
							t.Helper()
							reader, err := sql.Open("sqlite", path)
							if err != nil {
								t.Fatal(err)
							}
							defer reader.Close()
							var v int
							if err := reader.QueryRowContext(ctx, `SELECT version FROM messages WHERE account='primary' AND peer_type=? AND peer_id=? AND msg_id=8`, m.Key.Peer.Type, m.Key.Peer.ID).Scan(&v); err != nil {
								t.Fatal(err)
							}
							return v
						}
						beforeVersion := version()
						events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 8})
						incoming := *raw
						incoming.EditHide = true
						incoming.SetEditDate(1200)
						incoming.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}}})
						if changed {
							switch kind {
							case "text":
								incoming.Message = "really changed"
							case "document":
								incoming.Media = &tg.MessageMediaDocument{Document: &tg.Document{ID: 102, MimeType: "application/pdf", Size: 20, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "after.pdf"}}}}
							case "photo":
								incoming.Media = &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 102, Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", W: 20, H: 20, Size: 20}}}}
							case "poll":
								incoming.Media = &tg.MessageMediaPoll{Poll: tg.Poll{ID: 101, Question: tg.TextWithEntities{Text: "Question"}, Answers: []tg.PollAnswerClass{&tg.PollAnswer{Text: tg.TextWithEntities{Text: "After"}, Option: []byte{1}}}}}
							}
						}
						edit := func() {
							t.Helper()
							var u tg.UpdateClass = &tg.UpdateEditMessage{Message: &incoming}
							if channel {
								u = &tg.UpdateEditChannelMessage{Message: &incoming}
							}
							if err := e.handle(ctx, &tg.UpdateShort{Update: u}); err != nil {
								t.Fatal(err)
							}
						}
						edit()
						after, _, err := db.Message(ctx, m.Key)
						if err != nil {
							t.Fatal(err)
						}
						if after.ServerEditDate == nil || after.ServerEditDate.Unix() != 1200 {
							t.Errorf("server ordering lost: %+v", after)
						}
						if after.ContentHash == "" {
							t.Error("first authoritative content observation did not initialize hash")
						}
						if !changed && version() != beforeVersion {
							t.Errorf("reaction-only migrated row changed content version: before=%d after=%d", beforeVersion, version())
						}
						if !changed {
							if edited && (after.EditDate == nil || after.EditDate.Unix() != 1100) || !edited && after.EditDate != nil {
								t.Errorf("reaction-only update changed visible marker: before=%v after=%v", before.EditDate, after.EditDate)
							}
							if e.metrics.Edits != 0 {
								t.Errorf("reaction-only migrated row counted %d edits", e.metrics.Edits)
							}
						} else {
							if e.metrics.Edits != 1 {
								t.Errorf("genuine hidden content change lost: edits=%d", e.metrics.Edits)
							}
							want, _ := convertMessage("primary", &incoming)
							if after.Text != want.Text || kind == "document" && after.Media.Filename != "after.pdf" || kind == "photo" && after.Media.Size != 20 || kind == "poll" && after.Poll.Options[0].Text != "After" {
								t.Errorf("hidden changed content not stored: %+v", after)
							}
						}
						edits, reactions := 0, 0
						for len(events) > 0 {
							ev := <-events
							switch ev.Kind {
							case engine.EventEdit:
								edits++
							case engine.EventReaction:
								reactions++
							}
						}
						wantEdits := 0
						if changed {
							wantEdits = 1
						}
						if edits != wantEdits || reactions != 1 {
							t.Errorf("events edit=%d reaction=%d, want %d/1", edits, reactions, wantEdits)
						}
						edit()
						if len(events) != 0 {
							t.Error("exact replay emitted another event")
						}
						if err := db.Close(); err != nil {
							t.Fatal(err)
						}
						db = open()
						defer db.Close()
						stale := *raw
						stale.EditHide = true
						stale.SetEditDate(1150)
						old, _ := convertMessage("primary", &stale)
						ap, err := db.Apply(ctx, engine.Batch{Account: "primary", Edits: []engine.Message{old}})
						if err != nil || len(ap.Edited) != 0 {
							t.Errorf("reopen lost server ordering: %+v %v", ap, err)
						}
					})
				}
			}
		}
	}
}
