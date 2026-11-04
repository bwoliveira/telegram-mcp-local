package gotdengine

import (
	"context"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

func TestReactionRPCManagerDrainDoesNotReplayABA(t *testing.T) {
	ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
	e, db, raw := actionSQLiteFixture(t, ref)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	managerAPI := tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.UpdatesGetStateRequest:
			*out.(*tg.UpdatesState) = tg.UpdatesState{Pts: 1, Seq: 1, Date: 1000}
		case *tg.UpdatesGetDifferenceRequest:
			out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1000, Seq: 1}
		default:
			t.Errorf("unexpected manager %T", in)
		}
		return nil
	}))
	go func() {
		done <- e.manager.Run(ctx, managerAPI, e.account.SelfID, updates.AuthOptions{OnStart: func(context.Context) { close(entered); <-release }})
	}()
	released := false
	defer func() {
		if !released {
			close(release)
		}
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("manager did not stop")
		}
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("manager startup")
	}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		req, ok := in.(*tg.MessagesSendReactionRequest)
		if !ok {
			t.Fatalf("unexpected %T", in)
		}
		counts := []tg.ReactionCount{}
		if len(req.Reaction) > 0 {
			counts = []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}}
		}
		out.(*tg.UpdatesBox).Updates = &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: raw.PeerID, MsgID: raw.ID, Reactions: tg.MessageReactions{Results: counts}}}
		return nil
	}))
	events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
	key := engine.MessageKey{Account: "primary", Peer: ref, ID: raw.ID}
	for _, emoji := range []string{"👍", ""} {
		req := engine.ReactRequest{Key: key, Emoji: emoji, WriteOptions: engine.WriteOptions{DryRun: true}}
		p, err := e.React(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		req.WriteOptions = engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
		if _, err := e.React(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	// Drain both synchronous events, then release the real worker. A genuine
	// message is the FIFO barrier after the queued RPC responses.
	for i := 0; i < 2; i++ {
		select {
		case ev := <-events:
			if ev.Kind != engine.EventReaction {
				t.Fatal(ev.Kind)
			}
		default:
			t.Fatal("missing immediate reaction")
		}
	}
	close(release)
	released = true
	barrier := &tg.Message{ID: 999, PeerID: raw.PeerID, FromID: &tg.PeerUser{UserID: 10}, Date: 2000, Message: "Synthetic drain barrier"}
	if err := e.manager.Handle(ctx, &tg.UpdateShort{Update: &tg.UpdateNewMessage{Message: barrier, Pts: 2, PtsCount: 1}}); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case ev := <-events:
			if ev.Kind == engine.EventReaction {
				t.Errorf("queued response resurrected reaction: %+v", ev.Reaction)
			}
			if ev.Message != nil && ev.Message.Key.ID == 999 {
				m, _, err := db.Message(ctx, key)
				if err != nil || len(m.Reactions) != 0 {
					t.Fatalf("drained state=%+v err=%v", m.Reactions, err)
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("missing manager FIFO barrier")
		}
	}
}
