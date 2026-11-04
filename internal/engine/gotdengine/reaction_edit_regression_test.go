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

func TestReactionEditDateSemantics(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hidden bool
		date   int
		want   bool
	}{
		{"absent", false, 0, false},
		{"server-edit", false, 1234, true},
		{"hidden-server-edit", true, 1234, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 10}, Date: 1000, EditHide: tc.hidden}
			if tc.date != 0 {
				raw.SetEditDate(tc.date)
			}
			m, ok := convertMessage("primary", raw)
			if !ok {
				t.Fatal("message rejected")
			}
			if (m.EditDate != nil) != tc.want {
				t.Fatalf("EditHide=%v EditDate=%d: visible edit date = %v, want present=%v", tc.hidden, tc.date, m.EditDate, tc.want)
			}
			if tc.want && m.EditDate.Unix() != int64(tc.date) {
				t.Fatalf("server edit date changed: %v", m.EditDate)
			}
			if (m.ServerEditDate != nil) != (tc.date != 0) || tc.date != 0 && m.ServerEditDate.Unix() != int64(tc.date) {
				t.Fatalf("server revision lost: hidden=%v date=%d revision=%v", tc.hidden, tc.date, m.ServerEditDate)
			}
		})
	}
}

func TestReactionOnlyEditUpdates(t *testing.T) {
	for _, channel := range []bool{false, true} {
		for _, tc := range []struct {
			name         string
			previousDate int
			incomingDate int
			hidden       bool
		}{
			{"hidden-unedited", 0, 1200, true},
			{"hidden-after-edit", 1100, 1200, true},
			{"same-visible-edit-date", 1100, 1100, false},
			{"no-edit-date", 0, 0, false},
		} {
			name := "user/" + tc.name
			if channel {
				name = "channel/" + tc.name
			}
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				now := time.Unix(3000, 0)
				db, err := store.Open(ctx, filepath.Join(t.TempDir(), "reactions.db"), "primary", store.WithClock(func() time.Time { return now }))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
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
				raw := &tg.Message{ID: 8, PeerID: peer, Date: 1000, Message: "Synthetic content"}
				if tc.previousDate > 0 {
					raw.SetEditDate(tc.previousDate)
				}
				var seed tg.UpdateClass = &tg.UpdateNewMessage{Message: raw}
				if channel {
					seed = &tg.UpdateNewChannelMessage{Message: raw}
				}
				if err := e.handle(ctx, &tg.UpdateShort{Update: seed}); err != nil {
					t.Fatal(err)
				}
				key := engine.MessageKey{Account: "primary", Peer: peerRef(peer), ID: 8}
				before, _, err := db.Message(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
				events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
				assertEvents := func(want ...engine.EventKind) {
					t.Helper()
					for _, kind := range want {
						select {
						case event := <-events:
							if event.Kind != kind {
								t.Fatalf("event kind = %s, want %s", event.Kind, kind)
							}
							if kind == engine.EventReaction && (event.Reaction == nil || event.Reaction.Key != key) {
								t.Fatalf("reaction payload lost: %+v", event)
							}
						default:
							t.Fatalf("missing %s event", kind)
						}
					}
					select {
					case event := <-events:
						t.Fatalf("unexpected event: %s", event.Kind)
					default:
					}
				}
				incoming := *raw
				incoming.EditDate = tc.incomingDate
				incoming.EditHide = tc.hidden
				incoming.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}}})
				update := func(m *tg.Message) tg.UpdatesClass {
					var u tg.UpdateClass = &tg.UpdateEditMessage{Message: m}
					if channel {
						u = &tg.UpdateEditChannelMessage{Message: m}
					}
					return &tg.UpdateShort{Update: u}
				}
				if err := e.handle(ctx, update(&incoming)); err != nil {
					t.Fatal(err)
				}
				after, ok, err := db.Message(ctx, key)
				if err != nil || !ok {
					t.Fatalf("read: %v %v", ok, err)
				}
				if after.Text != before.Text {
					t.Errorf("reaction changed content/version: before=%+v after=%+v", before, after)
				}
				if (after.EditDate == nil) != (before.EditDate == nil) || (before.EditDate != nil && !after.EditDate.Equal(*before.EditDate)) {
					t.Errorf("reaction changed visible edit date: before=%v after=%v", before.EditDate, after.EditDate)
				}
				if len(after.Reactions) != 1 || after.Reactions[0].Count != 1 {
					t.Errorf("reaction snapshot not persisted: %+v", after.Reactions)
				}
				assertEvents(engine.EventReaction)
				if err := e.handle(ctx, update(&incoming)); err != nil {
					t.Fatal(err)
				}
				assertEvents()
				// Even an explicitly empty message copy cannot erase known
				// reactions. Only an authoritative reaction update removes them.
				incoming.SetReactions(tg.MessageReactions{})
				if err := e.handle(ctx, update(&incoming)); err != nil {
					t.Fatal(err)
				}
				after, _, err = db.Message(ctx, key)
				if err != nil || len(after.Reactions) != 1 {
					t.Fatalf("empty message copy erased reaction: %+v %v", after, err)
				}
				assertEvents()
				removal := &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: peer, MsgID: incoming.ID, Reactions: tg.MessageReactions{}}}
				if err := e.handle(ctx, removal); err != nil {
					t.Fatal(err)
				}
				after, _, err = db.Message(ctx, key)
				if err != nil || len(after.Reactions) != 0 || e.metrics.Edits != 0 {
					t.Fatalf("reaction removal lost or changed content: %+v %v", after, err)
				}
				assertEvents(engine.EventReaction)
				if err := e.handle(ctx, removal); err != nil {
					t.Fatal(err)
				}
				assertEvents()
				// A real server edit must still replace content and emit exactly edit.
				incoming.EditHide = false
				incoming.SetEditDate(1300)
				incoming.Message = "Synthetic edited content"
				if err := e.handle(ctx, update(&incoming)); err != nil {
					t.Fatal(err)
				}
				after, _, err = db.Message(ctx, key)
				if err != nil || after.Text != incoming.Message || after.EditDate == nil || after.EditDate.Unix() != 1300 || e.metrics.Edits != 1 {
					t.Fatalf("real edit lost: %+v %v", after, err)
				}
				assertEvents(engine.EventEdit)
				if err := e.handle(ctx, update(&incoming)); err != nil {
					t.Fatal(err)
				}
				assertEvents()
				// An older content revision cannot resurrect a removed reaction.
				incoming.SetEditDate(1250)
				incoming.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 2}}})
				if err := e.handle(ctx, update(&incoming)); err != nil {
					t.Fatal(err)
				}
				after, _, err = db.Message(ctx, key)
				if err != nil || len(after.Reactions) != 0 {
					t.Errorf("stale edit resurrected reaction: %+v %v", after.Reactions, err)
				}
				assertEvents()
			})
		}
	}
}
