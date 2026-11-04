package mcp

import (
	"encoding/json"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"testing"
)

func TestMetadataMessageProjectionAndFields(t *testing.T) {
	m := engine.Message{Reactions: []engine.ReactionCount{{Emoji: "👍", Count: 2, Chosen: true}}, Poll: &engine.Poll{ID: 501, Question: "Synthetic question"}, Location: &engine.Location{Latitude: 1, Longitude: 2}, Media: &engine.Media{Type: engine.MediaDice, Dice: &engine.DiceInfo{Emoji: "🎲", Value: 4}}}
	raw, err := json.Marshal(engineMessage(m))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"reactions", "poll", "location", "media"} {
		if _, ok := row[field]; !ok {
			t.Errorf("metadata %s omitted by common message projection", field)
		}
	}
	if _, err := newShaping(arguments{Fields: []string{"reactions", "poll", "location", "media"}}, messageFields); err != nil {
		t.Fatal("metadata fields cannot be selected")
	}
}
