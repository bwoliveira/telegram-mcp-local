package store

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
	"time"
)

func TestMetadataReactionRemovalAndMinState(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
	apply := func(b engine.Batch) engine.Applied {
		t.Helper()
		b.Account = "primary"
		a, err := s.Apply(ctx, b)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	apply(engine.Batch{Messages: []engine.Message{{Key: key, Date: time.Unix(1000, 0), Text: "Synthetic message"}}})
	recent := []engine.PeerReaction{{By: engine.PeerRef{Type: engine.PeerUser, ID: 11}, Emoji: "👍", Date: time.Unix(1500, 0)}}
	apply(engine.Batch{Reactions: []engine.ReactionUpdate{{Key: key, Counts: []engine.ReactionCount{{Emoji: "👍", Count: 1, Chosen: true}}, Recent: recent}}})
	apply(engine.Batch{Reactions: []engine.ReactionUpdate{{Key: key, Counts: []engine.ReactionCount{{Emoji: "👍", Count: 2}}, Min: true}}})
	m, _, err := s.Message(ctx, key)
	if err != nil || len(m.Reactions) != 1 || !m.Reactions[0].Chosen {
		t.Fatal("min snapshot erased account-specific chosen state")
	}
	a := apply(engine.Batch{Reactions: []engine.ReactionUpdate{{Key: key, Counts: []engine.ReactionCount{}}}})
	if len(a.Reactions) != 1 || len(a.Reactions[0].Removed) != 1 {
		t.Fatal("cleared counts retained stale recent reactors")
	}
	if a = apply(engine.Batch{Reactions: []engine.ReactionUpdate{{Key: key, Counts: []engine.ReactionCount{}}}}); len(a.Reactions) != 0 {
		t.Fatal("replayed clear emitted duplicate event")
	}
}

func TestMetadataPollUpdateLifecycleAndRestart(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
	p := &engine.Poll{ID: 501, Question: "Synthetic question", Options: []engine.PollOption{{Key: "AA==", Text: "Option", Voters: 2, Chosen: true}}, TotalVoters: 2}
	b := engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Date: time.Unix(1000, 0), Text: p.Question, Media: &engine.Media{Type: engine.MediaPoll}, Poll: p}}}
	if _, err := s.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	total := 3
	u := engine.Batch{Account: "primary", Polls: []engine.PollUpdate{{ID: 501, TotalVoters: &total, Min: true, Results: []engine.PollOption{{Key: "AA==", Voters: 3}}}}}
	if _, err := s.Apply(ctx, u); err != nil {
		t.Fatal(err)
	}
	m, _, err := s.Message(ctx, key)
	if err != nil || m.Poll.TotalVoters != 3 || !m.Poll.Options[0].Chosen {
		t.Fatal("min poll update erased prior account vote")
	}
	var version, updated int
	if err := s.writer.QueryRow(`SELECT version,updated_at FROM messages`).Scan(&version, &updated); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Unix(2100, 0) }
	if _, err := s.Apply(ctx, u); err != nil {
		t.Fatal(err)
	}
	var nextVersion, nextUpdated int
	if err := s.writer.QueryRow(`SELECT version,updated_at FROM messages`).Scan(&nextVersion, &nextUpdated); err != nil {
		t.Fatal(err)
	}
	if version != nextVersion || updated != nextUpdated {
		t.Fatal("replayed poll update changed durable version")
	}
	// Deleted/expired/temporary rows cannot be repopulated by poll/reaction updates.
	querySQL(t, s, `UPDATE messages SET expires_at=2050`)
	if _, err := s.Apply(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Message(ctx, key); err != nil || ok {
		t.Fatal("expired poll exposed")
	}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Deletes: []engine.Deletion{{Peer: &key.Peer, IDs: []int{key.ID}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, u); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Message(ctx, key); err != nil || ok {
		t.Fatal("poll update resurrected tombstone")
	}
}
