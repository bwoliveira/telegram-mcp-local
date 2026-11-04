package gotdengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/tg"
)

func TestHiddenContentRevisionPersists(t *testing.T) {
	for _, channel := range []bool{false, true} {
		for _, media := range []bool{false, true} {
			name := "user/text"
			if channel {
				name = "channel/text"
			}
			if media {
				name += "-media"
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				now := time.Unix(3000, 0)
				path := filepath.Join(t.TempDir(), "hidden.db")
				db, err := store.Open(ctx, path, "primary", store.WithClock(func() time.Time { return now }))
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = db.Close() }()
				e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Session: &memorySession{}, Store: db, Clock: func() time.Time { return now }})
				if err != nil {
					t.Fatal(err)
				}
				e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
				if err := db.BindSelf(ctx, "primary", e.account); err != nil {
					t.Fatal(err)
				}
				var peer tg.PeerClass = &tg.PeerUser{UserID: 10}
				if channel {
					peer = &tg.PeerChannel{ChannelID: 20}
				}
				raw := &tg.Message{ID: 8, PeerID: peer, Date: 1000, Message: "Original text"}
				if media {
					raw.Media = &tg.MessageMediaDocument{Document: &tg.Document{ID: 101, MimeType: "application/pdf", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "before.pdf"}}}}
				}
				var seed tg.UpdateClass = &tg.UpdateNewMessage{Message: raw}
				if channel {
					seed = &tg.UpdateNewChannelMessage{Message: raw}
				}
				if err := e.handle(ctx, &tg.UpdateShort{Update: seed}); err != nil {
					t.Fatal(err)
				}
				key := engine.MessageKey{Account: "primary", Peer: peerRef(peer), ID: raw.ID}
				events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 8})
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
				incoming := *raw
				incoming.EditHide = true
				incoming.SetEditDate(1200)
				if media {
					// Same caption, different actual attachment.
					incoming.Media = &tg.MessageMediaDocument{Document: &tg.Document{ID: 102, MimeType: "application/pdf", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "after.pdf"}}}}
				} else {
					incoming.Message = "Hidden revised text"
				}
				edit(&incoming)
				got, ok, err := db.Message(ctx, key)
				if err != nil || !ok {
					t.Fatalf("read revised: %v %v", ok, err)
				}
				if got.Text != incoming.Message || media && (got.Media == nil || got.Media.Filename != "after.pdf") {
					t.Fatalf("hidden content revision discarded: %+v", got)
				}
				if got.EditDate != nil || got.ServerEditDate == nil || got.ServerEditDate.Unix() != 1200 {
					t.Fatalf("hidden edit lost server revision or exposed visible marker: %+v", got)
				}
				var firstEvent engine.Event
				select {
				case ev := <-events:
					firstEvent = ev
					if ev.Kind != engine.EventEdit || ev.OccurredAt.Unix() != 1200 {
						t.Fatalf("hidden revision event = %+v, want edit at server revision 1200", ev)
					}
				default:
					t.Fatal("hidden content revision emitted no edit event")
				}
				edit(&incoming)
				select {
				case ev := <-events:
					t.Fatalf("duplicate hidden revision emitted %s", ev.Kind)
				default:
				}
				// A second hidden revision needs a distinct durable event identity,
				// even when the attachment has identical public metadata.
				incoming.SetEditDate(1300)
				if media {
					incoming.Media = &tg.MessageMediaDocument{Document: &tg.Document{ID: 103, MimeType: "application/pdf", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "after.pdf"}}}}
				} else {
					incoming.Message = "Second hidden text"
				}
				edit(&incoming)
				select {
				case ev := <-events:
					if ev.Kind != engine.EventEdit || ev.ID == firstEvent.ID || ev.OccurredAt.Unix() != 1300 {
						t.Fatalf("second hidden revision lost event identity: first=%+v second=%+v", firstEvent, ev)
					}
				default:
					t.Fatal("second hidden content revision emitted no event")
				}
				// Reopen the real store: hidden server ordering must be durable,
				// not remembered only in a converter or engine instance.
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				db, err = store.Open(ctx, path, "primary", store.WithClock(func() time.Time { return now }))
				if err != nil {
					t.Fatal(err)
				}
				stale := *raw
				stale.SetEditDate(1100)
				converted, _ := convertMessage("primary", &stale)
				ap, err := db.Apply(ctx, engine.Batch{Account: "primary", Edits: []engine.Message{converted}})
				if err != nil {
					t.Fatal(err)
				}
				got, _, err = db.Message(ctx, key)
				if err != nil || len(ap.Edited) != 0 || got.Text != incoming.Message || got.EditDate != nil || media && (got.Media == nil || got.Media.Filename != "after.pdf") {
					t.Fatalf("reopen lost hidden revision ordering: message=%+v applied=%+v err=%v", got, ap, err)
				}
			})
		}
	}
}
