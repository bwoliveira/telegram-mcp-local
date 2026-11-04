package store

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
	"time"
)

func TestMetadataProtectedExportOmitsRichPayload(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	m := engine.Message{Key: engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}, Date: time.Unix(1000, 0), Text: "Synthetic protected text", Protected: true, Media: &engine.Media{Type: engine.MediaVenue}, Location: &engine.Location{Latitude: 1, Longitude: 2, Address: "Synthetic address"}, Poll: &engine.Poll{ID: 501, Question: "Synthetic question"}}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{m}}); err != nil {
		t.Fatal(err)
	}
	if err := s.StreamExportMessages(ctx, "primary", StatsFilter{}, func(m engine.Message) error {
		if m.Media != nil || m.Location != nil || m.Poll != nil {
			t.Error("protected rich media escaped export redaction")
		}
		if !m.Protected || m.Text != "Synthetic protected text" {
			t.Error("protected text readability changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMetadataDefinitionOnlyPollPreservesVotes(t *testing.T) {
	s := queryStore(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
	p := &engine.Poll{ID: 501, Question: "Synthetic question", TotalVoters: 2, Options: []engine.PollOption{{Key: "AA==", Text: "Option", Voters: 2, Chosen: true}}}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Date: time.Unix(1000, 0), Text: p.Question, Media: &engine.Media{Type: engine.MediaPoll}, Poll: p}}}); err != nil {
		t.Fatal(err)
	}
	d := *p
	d.Closed = true
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Polls: []engine.PollUpdate{{ID: 501, Definition: &d}}}); err != nil {
		t.Fatal(err)
	}
	m, _, err := s.Message(ctx, key)
	if err != nil || !m.Poll.Closed || m.Poll.TotalVoters != 2 || m.Poll.Options[0].Voters != 2 || !m.Poll.Options[0].Chosen {
		t.Fatal("definition-only poll update erased loaded votes")
	}
}
