package store

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
	"time"
)

func TestMetadataExistingUnsupportedRowIsEnrichedOnRefresh(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
	old := engine.Message{Key: key, Date: time.Unix(1000, 0), Media: &engine.Media{Type: engine.MediaOther}}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{old}}); err != nil {
		t.Fatal(err)
	}
	refreshed := old
	refreshed.Text = "Synthetic question"
	refreshed.Media = &engine.Media{Type: engine.MediaPoll}
	refreshed.Poll = &engine.Poll{ID: 501, Question: refreshed.Text, Options: []engine.PollOption{{Key: "AA==", Text: "Option"}}}
	refreshed.Reactions = []engine.ReactionCount{{Emoji: "👍", Count: 1}}
	b := engine.Batch{Account: "primary", Source: engine.SourceRefresh, Messages: []engine.Message{refreshed}}
	if _, err := s.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	m, ok, err := s.Message(ctx, key)
	if err != nil || !ok || m.Media.Type != engine.MediaPoll || m.Poll == nil || m.Text != refreshed.Text || len(m.Reactions) != 1 {
		t.Fatal("existing unsupported message was never enriched")
	}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Reactions: []engine.ReactionUpdate{{Key: key, Counts: []engine.ReactionCount{}}}}); err != nil {
		t.Fatal(err)
	}
	b.Source = engine.SourceBackfill
	if _, err := s.Apply(ctx, b); err != nil {
		t.Fatal(err)
	}
	m, _, err = s.Message(ctx, key)
	if err != nil || len(m.Reactions) != 0 {
		t.Fatal("metadata repair resurrected cleared reactions")
	}
}

func TestMetadataRefreshCannotEnrichTemporaryOrExpiredRows(t *testing.T) {
	for _, policy := range []string{"temporary", "expired", "older-edit"} {
		t.Run(policy, func(t *testing.T) {
			s := queryStore(t)
			ctx := context.Background()
			key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
			old := engine.Message{Key: key, Date: time.Unix(1000, 0), Media: &engine.Media{Type: engine.MediaOther}}
			if policy == "temporary" {
				old.Media.TTLSeconds = 30
			}
			if policy == "older-edit" {
				edit := time.Unix(1500, 0)
				old.EditDate = &edit
			}
			if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{old}}); err != nil {
				t.Fatal(err)
			}
			if policy == "expired" {
				querySQL(t, s, `UPDATE messages SET expires_at=1900`)
			}
			new := engine.Message{Key: key, Date: old.Date, Text: "Synthetic question", Media: &engine.Media{Type: engine.MediaPoll}, Poll: &engine.Poll{ID: 501, Question: "Synthetic question"}}
			if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Source: engine.SourceRefresh, Messages: []engine.Message{new}}); err != nil {
				t.Fatal(err)
			}
			m, ok, err := s.Message(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if ok && (m.Poll != nil || m.Text == new.Text) {
				t.Fatal("metadata repair ignored lifecycle or edit ordering")
			}
		})
	}
}
