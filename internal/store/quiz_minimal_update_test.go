package store

import (
	"context"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestQuizMinimalResultPreservesKnownCorrectAnswer(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
	p := &engine.Poll{ID: 501, Quiz: true, TotalVoters: 2, Options: []engine.PollOption{{Key: "AA==", Text: "A", Voters: 2, Chosen: true, Correct: true}}}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Date: time.Unix(1000, 0), Media: &engine.Media{Type: engine.MediaPoll}, Poll: p}}}); err != nil {
		t.Fatal(err)
	}
	total := 3
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Polls: []engine.PollUpdate{{ID: 501, Min: true, TotalVoters: &total, Results: []engine.PollOption{{Key: "AA==", Voters: 3}}}}}); err != nil {
		t.Fatal(err)
	}
	m, _, err := s.Message(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Poll.Options[0]; !got.Correct || !got.Chosen || got.Voters != 3 {
		t.Fatalf("minimal quiz result lost account state: %+v", got)
	}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Polls: []engine.PollUpdate{{ID: 501, TotalVoters: &total, Results: []engine.PollOption{{Key: "AA==", Voters: 3}}}}}); err != nil {
		t.Fatal(err)
	}
	m, _, err = s.Message(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Poll.Options[0]; got.Correct || got.Chosen {
		t.Fatalf("full quiz result retained stale account state: %+v", got)
	}
}
