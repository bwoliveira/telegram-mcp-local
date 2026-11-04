package gotdengine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/tg"
)

func TestMetadataMessageReactionRecentAndMinEdit(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(2000, 0)
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "metadata.db"), "primary", store.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	raw := &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 10}, Date: 1000, Message: "Synthetic message"}
	c := tg.ReactionCount{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}
	c.SetChosenOrder(0)
	r := tg.MessageReactions{Results: []tg.ReactionCount{c}, RecentReactions: []tg.MessagePeerReaction{{PeerID: &tg.PeerUser{UserID: 11}, Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Date: 1500}}}
	raw.SetReactions(r)
	b := engine.Batch{Account: "primary", Source: engine.SourceUpdate}
	appendMessage(&b, raw, false, engine.PeerRef{})
	if _, err := db.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	key := b.Messages[0].Key
	// A minimal edit changes content/counts, not this account's chosen state.
	raw.EditDate = 1600
	raw.SetReactions(tg.MessageReactions{Min: true, Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 2}}})
	b = engine.Batch{Account: "primary", Source: engine.SourceUpdate}
	appendMessage(&b, raw, true, engine.PeerRef{})
	if _, err := db.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	m, _, err := db.Message(ctx, key)
	if err != nil || !m.Reactions[0].Chosen {
		t.Fatal("minimal full-message snapshot erased chosen state")
	}
	a, err := db.Apply(ctx, engine.Batch{Account: "primary", Reactions: []engine.ReactionUpdate{{Key: key, Counts: []engine.ReactionCount{}}}})
	if err != nil || len(a.Reactions) != 1 || len(a.Reactions[0].Removed) != 1 {
		t.Fatal("message.reactions recent peers were not ingested")
	}
	// Replaying old history cannot resurrect a removed reaction.
	raw.EditDate = 0
	raw.SetReactions(r)
	b = engine.Batch{Account: "primary", Source: engine.SourceBackfill}
	appendMessage(&b, raw, false, engine.PeerRef{})
	if _, err := db.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	m, _, err = db.Message(ctx, key)
	if err != nil || len(m.Reactions) != 0 {
		t.Fatal("stale history resurrected reaction")
	}
}

func TestMetadataRichMediaContainsOnlyTelegramFields(t *testing.T) {
	cases := []struct {
		raw        tg.MessageMediaClass
		key, value string
	}{
		{&tg.MessageMediaContact{PhoneNumber: "000", FirstName: "Synthetic", Vcard: "BEGIN:VCARD", UserID: 10}, "vcard", "BEGIN:VCARD"},
		{&tg.MessageMediaDice{Emoticon: "🎲", Value: 4}, "emoji", "🎲"},
		{&tg.MessageMediaStory{Peer: &tg.PeerChannel{ChannelID: 10}, ID: 7}, "id", "7"},
		{&tg.MessageMediaWebPage{Webpage: &tg.WebPage{ID: 3, URL: "https://example.invalid", Title: "Synthetic title"}}, "title", "Synthetic title"},
	}
	for _, tc := range cases {
		m, ok := convertMessage("primary", &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 10}, Media: tc.raw})
		if !ok {
			t.Fatal("message rejected")
		}
		data, err := json.Marshal(m.Media)
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]any
		if err := json.Unmarshal(data, &root); err != nil {
			t.Fatal(err)
		}
		field := string(m.Media.Type)
		if field == "webpage" {
			field = "webpage"
		}
		nested, ok := root[field].(map[string]any)
		if !ok {
			t.Fatal("missing rich media metadata")
		}
		value := nested[tc.key]
		if tc.key == "id" {
			if value != float64(7) {
				t.Fatal("story id lost")
			}
		} else if value != tc.value {
			t.Fatal("Telegram metadata lost")
		}
		if root["remote"] != nil || root["access_hash"] != nil {
			t.Fatal("opaque credentials escaped")
		}
	}
}
