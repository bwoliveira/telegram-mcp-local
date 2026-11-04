package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestTriageStoreGroupMentionTopicAndSameSecondOrder(t *testing.T) {
	for _, tc := range []struct {
		name  string
		topic int
		outID int
		delay time.Duration
		want  bool
	}{
		{"different_topic_later", 20, 12, time.Second, true},
		{"different_topic_same_second", 20, 12, 0, true},
		{"same_topic_earlier_same_second", 10, 10, 0, true},
		{"same_topic_later_same_second", 10, 12, 0, false},
		{"same_topic_later", 10, 12, time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, st, _, now := triageSQLite(t)
			p := engine.PeerRef{Type: engine.PeerChannel, ID: 42}
			at := now.Add(-time.Hour)
			mention := triageMessage(p, 11, at, "question for me?")
			mention.TopicID, mention.Mentioned = 10, true
			out := triageMessage(p, tc.outID, at.Add(tc.delay), "sent")
			out.TopicID, out.Out = tc.topic, true
			batch := engine.Batch{Account: "primary", Source: engine.SourceBackfill,
				Peers:    []engine.PeerUpsert{{Peer: engine.Peer{Ref: p, Kind: engine.KindSupergroup}}},
				Messages: []engine.Message{out, mention},
			}
			if _, err := st.Apply(context.Background(), batch); err != nil {
				t.Fatal(err)
			}
			if got := triageChats(t, s, arguments{}); len(got) != 0 {
				t.Fatalf("groups excluded by default: %v", got)
			}
			got := triageChats(t, s, arguments{IncludeGroupMentions: true})
			row := got[peerID(p)]
			if (row != nil) != tc.want {
				t.Fatalf("want mention visible=%v: %v", tc.want, got)
			}
			if row != nil && row["mention"].(map[string]any)["id"] != "11" {
				t.Fatalf("wrong mention: %v", row)
			}
		})
	}
}

func TestTriageStoreWaitingSameSecondOrder(t *testing.T) {
	s, st, _, now := triageSQLite(t)
	p := engine.PeerRef{Type: engine.PeerUser, ID: 42}
	at := now.Add(-time.Hour)
	out := triageMessage(p, 10, at, "sent")
	out.Out = true
	batch := engine.Batch{Account: "primary", Source: engine.SourceBackfill,
		Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: p, Kind: engine.KindUser}}},
		Messages: []engine.Message{
			triageMessage(p, 9, at, "before outgoing"),
			triageMessage(p, 11, at, "after outgoing?"),
			out,
			triageMessage(p, 12, at.Add(-time.Second), "older despite higher ID"),
		},
	}
	if _, err := st.Apply(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	row := triageChats(t, s, arguments{})[peerID(p)]
	if row == nil || row["waiting_messages"] != float64(1) {
		t.Fatalf("expected only incoming 11 to wait after outgoing 10: %v", row)
	}
	if row["waiting_since"] != at.Format(time.RFC3339) {
		t.Fatalf("waiting_since must retain the incoming timestamp: %v", row)
	}
	// A later outgoing at the same timestamp still clears the chat.
	out.Key.ID = 13
	batch.Peers = nil
	batch.Messages = []engine.Message{out}
	if _, err := st.Apply(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	if got := triageChats(t, s, arguments{}); len(got) != 0 {
		t.Fatal(got)
	}
}
