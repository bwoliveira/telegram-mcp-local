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

func TestPollContentHashIgnoresResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*engine.Poll)
	}{
		{"total-voters", func(p *engine.Poll) { p.TotalVoters++ }},
		{"answer-voters", func(p *engine.Poll) { p.Options[0].Voters++ }},
		{"chosen", func(p *engine.Poll) { p.Options[0].Chosen = !p.Options[0].Chosen }},
		{"correct-revealed", func(p *engine.Poll) { p.Options[0].Correct = !p.Options[0].Correct }},
		{"partial", func(p *engine.Poll) { p.Partial = !p.Partial }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := convertMessage("primary", &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 10}, Date: 1000, Media: metadataPoll()})
			if !ok || m.Poll == nil {
				t.Fatal("poll conversion failed")
			}
			tc.change(m.Poll)
			if got := messageContentHash(m, nil); got != m.ContentHash {
				t.Fatalf("%s changed content hash: before=%s after=%s", tc.name, m.ContentHash, got)
			}
		})
	}
}

func TestPollContentHashIncludesDefinition(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*engine.Poll)
	}{
		{"identity", func(p *engine.Poll) { p.ID++ }},
		{"question", func(p *engine.Poll) { p.Question += " revised" }},
		{"answer-text", func(p *engine.Poll) { p.Options[0].Text += " revised" }},
		{"answer-key", func(p *engine.Poll) { p.Options[0].Key = "AQ==" }},
		{"answer-added", func(p *engine.Poll) { p.Options = append(p.Options, engine.PollOption{Key: "AQ==", Text: "Option B"}) }},
		{"closed", func(p *engine.Poll) { p.Closed = !p.Closed }},
		{"anonymous", func(p *engine.Poll) { p.Anonymous = !p.Anonymous }},
		{"multiple", func(p *engine.Poll) { p.Multiple = !p.Multiple }},
		{"quiz", func(p *engine.Poll) { p.Quiz = !p.Quiz }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, ok := convertMessage("primary", &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 10}, Date: 1000, Media: metadataPoll()})
			if !ok || m.Poll == nil {
				t.Fatal("poll conversion failed")
			}
			tc.change(m.Poll)
			if messageContentHash(m, nil) == m.ContentHash {
				t.Fatalf("%s definition change did not change content hash", tc.name)
			}
		})
	}
}

func TestHiddenPollRefreshDoesNotEdit(t *testing.T) {
	for _, channel := range []bool{false, true} {
		name := "user"
		if channel {
			name = "channel"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			now := time.Unix(3000, 0)
			db, err := store.Open(ctx, filepath.Join(t.TempDir(), "poll.db"), "primary", store.WithClock(func() time.Time { return now }))
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
			raw := &tg.Message{ID: 8, PeerID: peer, Date: 1000, Media: metadataPoll()}
			var seed tg.UpdateClass = &tg.UpdateNewMessage{Message: raw}
			if channel {
				seed = &tg.UpdateNewChannelMessage{Message: raw}
			}
			if err := e.handle(ctx, &tg.UpdateShort{Update: seed}); err != nil {
				t.Fatal(err)
			}
			key := engine.MessageKey{Account: "primary", Peer: peerRef(peer), ID: raw.ID}
			before, ok, err := db.Message(ctx, key)
			if err != nil || !ok {
				t.Fatalf("read seeded poll: %v %v", ok, err)
			}
			events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
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
			// A hidden message snapshot can refresh both reaction and poll results.
			incoming := *raw
			incoming.EditHide = true
			incoming.SetEditDate(1200)
			poll := metadataPoll()
			poll.Results.TotalVoters = 3
			poll.Results.Results[0].Voters = 3
			poll.Results.Results[0].Chosen = false
			poll.Results.Results[0].Correct = true
			poll.Results.Min = true
			incoming.Media = poll
			incoming.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}}})
			edit(&incoming)
			after, ok, err := db.Message(ctx, key)
			if err != nil || !ok {
				t.Fatalf("read refreshed poll: %v %v", ok, err)
			}
			if after.ContentHash != before.ContentHash || after.EditDate != nil || after.ServerEditDate == nil || after.ServerEditDate.Unix() != 1200 || e.metrics.Edits != 0 {
				t.Fatalf("hidden poll results became an edit: before hash=%s after hash=%s visible=%v server=%v edits=%d", before.ContentHash, after.ContentHash, after.EditDate, after.ServerEditDate, e.metrics.Edits)
			}
			select {
			case ev := <-events:
				if ev.Kind != engine.EventReaction {
					t.Fatalf("refresh event=%s, want reaction", ev.Kind)
				}
			default:
				t.Fatal("reaction refresh event missing")
			}
			select {
			case ev := <-events:
				t.Fatalf("extra refresh event=%s", ev.Kind)
			default:
			}
			edit(&incoming)
			select {
			case ev := <-events:
				t.Fatalf("duplicate refresh event=%s", ev.Kind)
			default:
			}

			// Keep text and results identical: only the actual answer definition changes.
			incoming.SetEditDate(1300)
			poll.Poll.Answers[0].(*tg.PollAnswer).Text.Text = "Revised option"
			edit(&incoming)
			after, ok, err = db.Message(ctx, key)
			if err != nil || !ok || after.Poll == nil || after.Poll.Options[0].Text != "Revised option" || after.ContentHash == before.ContentHash || after.EditDate != nil || after.ServerEditDate == nil || after.ServerEditDate.Unix() != 1300 || e.metrics.Edits != 1 {
				t.Fatalf("hidden poll definition edit lost: message=%+v edits=%d err=%v", after, e.metrics.Edits, err)
			}
			select {
			case ev := <-events:
				if ev.Kind != engine.EventEdit || ev.OccurredAt.Unix() != 1300 {
					t.Fatalf("definition edit event=%+v", ev)
				}
			default:
				t.Fatal("definition edit event missing")
			}
			edit(&incoming)
			select {
			case ev := <-events:
				t.Fatalf("duplicate definition event=%s", ev.Kind)
			default:
			}
		})
	}
}
