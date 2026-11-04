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

func TestPollMinimalMediaSnapshotSQLiteMerge(t *testing.T) {
	for _, path := range []string{"edit", "channel_edit", "search_snapshot"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			now := time.Unix(2000, 0)
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
			if path == "channel_edit" {
				peer = &tg.PeerChannel{ChannelID: 30}
			}
			media := metadataPoll()
			media.Poll.Quiz = true
			media.Results.Results[0].Correct = true
			raw := &tg.Message{ID: 8, PeerID: peer, Date: 1000, Media: media}
			if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateNewMessage{Message: raw}}); err != nil {
				t.Fatal(err)
			}
			raw.EditDate = 1100
			media.Poll.Closed = true
			media.Poll.Question.Text = "Revised question"
			media.Results = tg.PollResults{Min: true, Results: []tg.PollAnswerVoters{{Option: []byte{0, 255}, Voters: 3}}}
			media.Results.SetTotalVoters(3)
			var ingest func() error
			switch path {
			case "edit":
				ingest = func() error { return e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateEditMessage{Message: raw}}) }
			case "channel_edit":
				ingest = func() error {
					return e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateEditChannelMessage{Message: raw}})
				}
			case "search_snapshot":
				ingest = func() error {
					b := engine.Batch{Account: "primary", Source: engine.SourceSearch}
					appendMessage(&b, raw, false, engine.PeerRef{})
					_, err := db.Apply(ctx, b)
					return err
				}
			}
			if err := ingest(); err != nil {
				t.Fatal(err)
			}
			key := engine.MessageKey{Account: "primary", Peer: peerRef(peer), ID: 8}
			got, ok, err := db.Message(ctx, key)
			if err != nil || !ok || got.Poll == nil {
				t.Fatalf("message: found=%v err=%v poll=%+v", ok, err, got.Poll)
			}
			if p := got.Poll; p.TotalVoters != 3 || !p.Partial || !p.Closed || p.Question != "Revised question" || p.Options[0].Voters != 3 || !p.Options[0].Chosen || !p.Options[0].Correct {
				t.Fatalf("minimal media snapshot lost loaded answers or fresh metadata: %+v option=%+v", p, p.Options[0])
			}
			// Complete media snapshots must still clear no-longer-present answers.
			raw.EditDate = 1200
			media.Results.Min = false
			if err := ingest(); err != nil {
				t.Fatal(err)
			}
			got, _, err = db.Message(ctx, key)
			if err != nil || got.Poll.Partial || got.Poll.Options[0].Chosen || got.Poll.Options[0].Correct {
				t.Fatalf("complete media snapshot retained stale answers: %+v err=%v", got.Poll, err)
			}
		})
	}
}
