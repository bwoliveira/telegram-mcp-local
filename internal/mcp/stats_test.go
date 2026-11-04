package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestStatsMessagesFakeCountsAndFilters(t *testing.T) {
	s, _, _, st := messagesSetup(t)
	p := engine.PeerRef{Type: engine.PeerUser, ID: 1}
	sender := engine.PeerRef{Type: engine.PeerUser, ID: 9}
	at := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	st.peers = []engine.Peer{{Ref: p, FirstName: "Ana"}, {Ref: sender, FirstName: "Bob"}}
	st.messages = []engine.Message{
		{Key: engine.MessageKey{Account: "people", Peer: p, ID: 1}, Date: at, Sender: &sender, Text: "one"},
		{Key: engine.MessageKey{Account: "people", Peer: p, ID: 2}, Date: at.Add(time.Second), Out: true},
		{Key: engine.MessageKey{Account: "people", Peer: engine.PeerRef{Type: engine.PeerChat, ID: 1}, ID: 1}, Date: at.Add(2 * time.Second), Sender: &sender},
	}
	b := toolBody(t, s.statsMessages(context.Background(), arguments{Limit: 1}))
	if b["total"] != float64(3) || b["groups"] != float64(2) || b["truncated"] != true || b["coverage_complete"] != false {
		t.Fatal(b)
	}
	buckets := b["buckets"].([]any)
	if len(buckets) != 1 || buckets[0].(map[string]any)["key"] != "user:1" || buckets[0].(map[string]any)["name"] != "Ana" {
		t.Fatal(b)
	}
	b = toolBody(t, s.statsMessages(context.Background(), arguments{Direction: "received", ExcludeGroups: true, Since: at.Format(time.RFC3339), Until: at.Add(time.Second).Format(time.RFC3339), GroupBy: "sender"}))
	if b["total"] != float64(1) || b["buckets"].([]any)[0].(map[string]any)["key"] != "user:9" {
		t.Fatal(b)
	}
	for _, a := range []arguments{{GroupBy: "invalid"}, {ChatJID: "-1"}, {Direction: "sideways"}, {Since: "not a date"}, {Since: "2026-01-02", Until: "2026-01-01"}} {
		if r := s.statsMessages(context.Background(), a); r["isError"] != true {
			t.Fatal(r)
		}
	}
}
