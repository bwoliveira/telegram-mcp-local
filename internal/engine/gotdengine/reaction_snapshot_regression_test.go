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

func TestReactionSnapshotNonAuthoritativeNeverErases(t *testing.T) {
	for _, channel := range []bool{false, true} {
		for _, mode := range []string{"absent", "empty", "min-empty", "min-subset", "complete-subset"} {
			name := "user/" + mode
			if channel {
				name = "channel/" + mode
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				db, err := store.Open(ctx, filepath.Join(t.TempDir(), "snapshot.db"), "primary")
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				var peer tg.PeerClass = &tg.PeerUser{UserID: 10}
				if channel {
					peer = &tg.PeerChannel{ChannelID: 20}
				}
				raw := &tg.Message{ID: 8, PeerID: peer, Date: 1000, Message: "synthetic"}
				chosen := tg.ReactionCount{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 2}
				chosen.SetChosenOrder(0)
				reactions := tg.MessageReactions{Results: []tg.ReactionCount{chosen, {Reaction: &tg.ReactionEmoji{Emoticon: "❤️"}, Count: 3}}}
				reactions.SetRecentReactions([]tg.MessagePeerReaction{{PeerID: &tg.PeerUser{UserID: 11}, Reaction: &tg.ReactionEmoji{Emoticon: "❤️"}, Date: 1500}})
				raw.SetReactions(reactions)
				batch := engine.Batch{Account: "primary", Source: engine.SourceUpdate}
				appendMessage(&batch, raw, false, engine.PeerRef{})
				if _, err := db.Apply(ctx, batch); err != nil {
					t.Fatal(err)
				}
				key := batch.Messages[0].Key
				incoming := &tg.Message{ID: 8, PeerID: peer, Date: 1000, Message: "edited"}
				incoming.SetEditDate(1600)
				switch mode {
				case "empty":
					incoming.SetReactions(tg.MessageReactions{})
				case "min-empty":
					incoming.SetReactions(tg.MessageReactions{Min: true})
				case "min-subset", "complete-subset":
					incoming.SetReactions(tg.MessageReactions{Min: mode == "min-subset", Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 4}}})
				}
				batch = engine.Batch{Account: "primary", Source: engine.SourceUpdate}
				appendMessage(&batch, incoming, true, engine.PeerRef{})
				applied, err := db.Apply(ctx, batch)
				if err != nil {
					t.Fatal(err)
				}
				m, found, err := db.Message(ctx, key)
				if err != nil || !found || m.Text != "edited" || len(m.Reactions) != 2 {
					t.Fatalf("message snapshot erased known reactions: message=%+v found=%v err=%v", m, found, err)
				}
				for _, r := range m.Reactions {
					if r.Emoji == "👍" && !r.Chosen {
						t.Errorf("message snapshot erased chosen: %+v", r)
					}
				}
				for _, event := range applied.Reactions {
					if len(event.Removed) != 0 {
						t.Errorf("partial snapshot emitted removal: %+v", event)
					}
				}
				// Only an explicit reaction update replaces the durable snapshot, including
				// the empty state. This is also the result shape of getMessagesReactions.
				e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Session: &memorySession{}, Store: db, Clock: func() time.Time { return time.Unix(3000, 0) }})
				if err != nil {
					t.Fatal(err)
				}
				e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
				if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: peer, MsgID: 8, Reactions: tg.MessageReactions{}}}); err != nil {
					t.Fatal(err)
				}
				m, _, err = db.Message(ctx, key)
				if err != nil || len(m.Reactions) != 0 {
					t.Fatalf("authoritative clear failed: %+v %v", m.Reactions, err)
				}
			})
		}
	}
}

func TestReactionSnapshotMinUpdatePreservesOmittedAndRecent(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"empty", "subset", "zero"} {
		t.Run(mode, func(t *testing.T) {
			db, err := store.Open(ctx, filepath.Join(t.TempDir(), "snapshot.db"), "primary")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
			initial := engine.ReactionUpdate{Key: key, Counts: []engine.ReactionCount{{Emoji: "👍", Count: 2, Chosen: true}, {Emoji: "❤️", Count: 3}}, Recent: []engine.PeerReaction{{By: engine.PeerRef{Type: engine.PeerUser, ID: 11}, Emoji: "❤️"}}}
			if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Date: time.Unix(1000, 0)}}, Reactions: []engine.ReactionUpdate{initial}}); err != nil {
				t.Fatal(err)
			}
			partial := engine.ReactionUpdate{Key: key, Min: true, Counts: []engine.ReactionCount{}, Recent: []engine.PeerReaction{}}
			if mode != "empty" {
				partial.Counts = []engine.ReactionCount{{Emoji: "👍", Count: 4}}
			}
			if mode == "zero" {
				partial.Counts[0].Count = 0
			}
			applied, err := db.Apply(ctx, engine.Batch{Account: "primary", Reactions: []engine.ReactionUpdate{partial}})
			if err != nil {
				t.Fatal(err)
			}
			m, _, err := db.Message(ctx, key)
			if err != nil || len(m.Reactions) != 2 {
				t.Fatalf("min erased known reactions: %+v %v", m.Reactions, err)
			}
			for _, event := range applied.Reactions {
				if len(event.Removed) != 0 {
					t.Errorf("min erased recent reaction: %+v", event)
				}
			}
			if mode != "subset" && len(applied.Reactions) != 0 {
				t.Errorf("non-authoritative removal changed state: %+v", applied.Reactions)
			}
			if mode == "subset" && (partial.Counts[0].Chosen || partial.Counts[0].Count != 4) {
				t.Errorf("apply mutated caller: %+v", partial)
			}
			applied, err = db.Apply(ctx, engine.Batch{Account: "primary", Reactions: []engine.ReactionUpdate{{Key: key, Counts: []engine.ReactionCount{}}}})
			if err != nil || len(applied.Reactions) != 1 || len(applied.Reactions[0].Removed) != 1 {
				t.Fatalf("known recent list lost before authoritative clear: %+v %v", applied, err)
			}
		})
	}
}
