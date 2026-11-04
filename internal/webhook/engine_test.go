package webhook

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

// Subscribe -> HTTP is the public seam: the actual fake commits updates and
// the receiver sees the wire contract, rather than a hand-written relay input.
func TestBindEngineMapsCommittedEvents(t *testing.T) {
	m, _ := manager(t)
	r := newReceiver(t, func(int32) int { return http.StatusNoContent })
	_, secret, err := m.Create(context.Background(), Settings{URL: ptr(r.srv.URL), Events: ptr(Events)})
	if err != nil {
		t.Fatal(err)
	}
	r.secret = secret
	eng, err := fake.New(engine.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.BindEngine(ctx, eng)
	f := eng.(*fake.Controller)
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 777}
	key := engine.MessageKey{Account: "primary", Peer: peer, ID: 9001}
	msg := engine.Message{Key: key, Date: time.Date(2026, 11, 1, 13, 4, 5, 0, time.UTC), Text: "unchanged third-party text", Sender: &peer}
	count := 0
	publish := func(b engine.Batch) {
		t.Helper()
		b.Source = engine.SourceDifference
		b.CatchUp = true
		if err := f.Publish(ctx, b); err != nil {
			t.Fatal(err)
		}
		count++
		waitFor(t, "mapped event", func() bool { return len(r.events()) == count })
	}
	publish(engine.Batch{Messages: []engine.Message{msg}})
	edit := msg.Date.Add(time.Minute)
	msg.Text = "edited"
	msg.EditDate = &edit
	publish(engine.Batch{Edits: []engine.Message{msg}})
	publish(engine.Batch{Reactions: []engine.ReactionUpdate{{Key: key, Counts: []engine.ReactionCount{{Emoji: "👍", Count: 1}}, Recent: []engine.PeerReaction{{By: peer, Emoji: "👍"}}}}})
	publish(engine.Batch{Reads: []engine.ReadUpdate{{Peer: peer, MaxID: 9001, Outbox: true, MessageIDs: []int{9001}}}})
	publish(engine.Batch{Deletes: []engine.Deletion{{Peer: &peer, IDs: []int{9001}}}})
	waitFor(t, "mapped events", func() bool { return len(r.events()) >= 4 })
	evs := r.events()
	want := []string{"message", "edit", "reaction", "read", "delete"}
	if len(evs) != len(want) {
		t.Fatalf("got %d events", len(evs))
	}
	for i, ev := range evs {
		if ev.Kind != want[i] {
			t.Fatalf("event %d: %s, want %s", i, ev.Kind, want[i])
		}
		if ev.Schema != 1 || ev.EventID == "" || !ev.CatchUp || ev.Source != engine.SourceDifference || ev.Chat == nil || ev.Chat.ID != "user:777" {
			t.Fatalf("envelope: %+v", ev)
		}
	}
	if evs[0].Message == nil || evs[0].Message.MessageID != 9001 || evs[0].Message.Text != "unchanged third-party text" || evs[0].Message.SenderID != "user:777" || !evs[0].OccurredAt.Equal(msg.Date) {
		t.Fatalf("message projection: %+v", evs[0])
	}
	if evs[1].Message == nil || evs[1].Message.Text != "edited" || evs[1].Message.EditDate == nil || !evs[1].OccurredAt.Equal(edit) {
		t.Fatalf("edit projection: %+v", evs[1])
	}
	if evs[2].Reaction == nil || evs[2].Reaction.MessageID != 9001 || len(evs[2].Reaction.Counts) != 1 || evs[2].Reaction.Counts[0].Count != 1 || evs[2].Reaction.By != "user:777" || evs[2].Reaction.Emoji != "👍" {
		t.Fatalf("reaction projection: %+v", evs[2])
	}
	if evs[3].Read == nil || evs[3].Read.MaxID != 9001 || len(evs[3].Read.MessageIDs) != 1 || evs[3].Read.MessageIDs[0] != 9001 {
		t.Fatalf("read projection: %+v", evs[3])
	}
	if evs[4].Delete == nil || len(evs[4].Delete.MessageIDs) != 1 || evs[4].Delete.MessageIDs[0] != 9001 || evs[4].Delete.Reason != "deleted" {
		t.Fatalf("delete projection: %+v", evs[4])
	}
	if len(f.Writes()) != 0 {
		t.Fatal("subscribing made a Telegram write")
	}
}
