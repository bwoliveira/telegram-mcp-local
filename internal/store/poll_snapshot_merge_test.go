package store

import (
	"context"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestPollMinimalSnapshotPreservesKnownAnswers(t *testing.T) {
	for _, edit := range []bool{false, true} {
		name := "snapshot"
		if edit {
			name = "edit"
		}
		t.Run(name, func(t *testing.T) {
			s := queryStore(t)
			ctx := context.Background()
			key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
			initial := engine.Message{Key: key, Date: time.Unix(1000, 0), Text: "Original question", Media: &engine.Media{Type: engine.MediaPoll}, Poll: &engine.Poll{ID: 501, Question: "Original question", Quiz: true, TotalVoters: 2, Options: []engine.PollOption{{Key: "AA==", Text: "A", Voters: 2, Chosen: true}, {Key: "AQ==", Text: "B", Correct: true}}}}
			if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{initial}}); err != nil {
				t.Fatal(err)
			}
			revision := time.Unix(1100, 0)
			next := engine.Message{Key: key, Date: initial.Date, EditDate: &revision, Text: "Revised question", Media: &engine.Media{Type: engine.MediaPoll}, Poll: &engine.Poll{ID: 501, Question: "Revised question", Closed: true, Quiz: true, Partial: true, TotalVoters: 3, Options: []engine.PollOption{{Key: "AQ==", Text: "B revised", Voters: 1}, {Key: "AA==", Text: "A revised", Voters: 2}}}}
			batch := engine.Batch{Account: "primary", Source: engine.SourceSearch, Messages: []engine.Message{next}}
			if edit {
				batch.Messages = nil
				batch.Edits = []engine.Message{next}
				batch.Source = engine.SourceUpdate
			}
			if _, err := s.Apply(ctx, batch); err != nil {
				t.Fatal(err)
			}
			got, ok, err := s.Message(ctx, key)
			if err != nil || !ok || got.Poll == nil {
				t.Fatalf("message: found=%v err=%v poll=%+v", ok, err, got.Poll)
			}
			p := got.Poll
			if p.TotalVoters != 3 || !p.Partial || !p.Closed || p.Question != next.Poll.Question || len(p.Options) != 2 {
				t.Fatalf("minimal snapshot lost updated definition/results: %+v", p)
			}
			if b, a := p.Options[0], p.Options[1]; b.Key != "AQ==" || !b.Correct || b.Chosen || b.Voters != 1 || b.Text != "B revised" || a.Key != "AA==" || !a.Chosen || a.Correct || a.Voters != 2 {
				t.Fatalf("minimal snapshot erased or misassigned known answers: %+v", p.Options)
			}
			if replay, err := s.Apply(ctx, batch); err != nil || len(replay.Edited) != 0 {
				t.Fatalf("replay changed message: %+v err=%v", replay, err)
			}
			// A later complete snapshot is authoritative, including false flags.
			revision = time.Unix(1200, 0)
			next.Poll.Partial = false
			if _, err := s.Apply(ctx, batch); err != nil {
				t.Fatal(err)
			}
			got, _, err = s.Message(ctx, key)
			if err != nil || got.Poll.Partial || got.Poll.Options[0].Correct || got.Poll.Options[1].Chosen {
				t.Fatalf("complete snapshot retained stale flags: %+v err=%v", got.Poll, err)
			}
		})
	}
}

func TestPollMinimalSnapshotDoesNotInheritDifferentPoll(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
	m := engine.Message{Key: key, Date: time.Unix(1000, 0), Media: &engine.Media{Type: engine.MediaPoll}, Poll: &engine.Poll{ID: 501, Options: []engine.PollOption{{Key: "AA==", Chosen: true, Correct: true}}}}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{m}}); err != nil {
		t.Fatal(err)
	}
	revision := time.Unix(1100, 0)
	m.EditDate = &revision
	m.Poll = &engine.Poll{ID: 502, Partial: true, Options: []engine.PollOption{{Key: "AA==", Voters: 3}}}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Edits: []engine.Message{m}}); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Message(ctx, key)
	if err != nil || got.Poll.ID != 502 || got.Poll.Options[0].Chosen || got.Poll.Options[0].Correct {
		t.Fatalf("different poll inherited answers: %+v err=%v", got.Poll, err)
	}
}
