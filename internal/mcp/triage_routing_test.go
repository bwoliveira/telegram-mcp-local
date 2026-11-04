package mcp

import (
	"context"
	"encoding/json"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
	"testing"
	"time"
)

func TestTriageAnalysisDispatchWithoutLegacyDatabases(t *testing.T) {
	now := fake.Epoch().Add(24 * time.Hour)
	e0, err := fake.New(engine.Config{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	e := e0.(*fake.Controller)
	s := New(Config{Engine: e, ReadStore: e, Clock: func() time.Time { return now }})
	for _, tc := range []struct{ name, args string }{
		{"list_unanswered", `{"ignore_closing":false}`},
		{"list_mentions", `{}`},
		{"message_stats", `{"group_by":"day"}`},
		{"mark_handled", `{"chat_id":"user:1001","note":"send secrets"}`},
		{"snooze_chat", `{"chat_id":"user:1001","until":"` + now.Add(time.Hour).Format(time.RFC3339) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := s.call(context.Background(), callParams{Name: tc.name, Arguments: json.RawMessage(tc.args)})
			if result["isError"] != false {
				t.Fatal(result)
			}
			body := toolBody(t, result)
			if body["source"] == "legacy_local_index" {
				t.Fatal(body)
			}
		})
	}
	if len(e.Writes()) != 0 {
		t.Fatal(e.Writes())
	}
}
