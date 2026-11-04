package gotdengine

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func reactionReadFixture(key engine.MessageKey, text string, reaction tg.ReactionClass) *tg.Message {
	m := &tg.Message{ID: key.ID, PeerID: mediaPeerClass(key.Peer), Date: 1000, Message: text, Out: true}
	if reaction != nil {
		count := tg.ReactionCount{Reaction: reaction, Count: 1}
		count.SetChosenOrder(0)
		m.SetReactions(tg.MessageReactions{Results: []tg.ReactionCount{count}})
	}
	return m
}

// These are coverage fixtures, NOT captured live TL. The installed bridge only
// exposes an acceptance receipt, so production shape capture remains necessary.
func TestReactionRPCWithoutTargetSnapshotRefresh(t *testing.T) {
	for _, typ := range []engine.PeerType{engine.PeerUser, engine.PeerChat, engine.PeerChannel} {
		for _, shape := range []string{"empty-updates", "edit-absent", "edit-empty", "edit-min", "unrelated-reaction"} {
			t.Run(fmt.Sprint(typ, "/", shape), func(t *testing.T) {
				ref := engine.PeerRef{Type: typ, ID: 10}
				e, db, raw := actionSQLiteFixture(t, ref)
				ctx := holdMediaSendManager(t, e)
				key := engine.MessageKey{Account: "primary", Peer: ref, ID: raw.ID}
				c := tg.ReactionCount{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}
				c.SetChosenOrder(0)
				full := tg.MessageReactions{Results: []tg.ReactionCount{c}}
				full.SetRecentReactions([]tg.MessagePeerReaction{{PeerID: &tg.PeerUser{UserID: 99}, Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Date: 1000, My: true}})
				if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: raw.PeerID, MsgID: raw.ID, Reactions: full}}); err != nil {
					t.Fatal(err)
				}
				events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 8})
				fetches := 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					switch r := in.(type) {
					case *tg.MessagesSendReactionRequest:
						if reaction, present := r.GetReaction(); !present || len(reaction) != 0 {
							t.Fatal("removal must encode an explicit empty vector")
						}
						u := &tg.Updates{}
						switch shape {
						case "edit-absent", "edit-empty", "edit-min":
							m := *raw
							if shape != "edit-absent" {
								m.SetReactions(tg.MessageReactions{Min: shape == "edit-min"})
							}
							if typ == engine.PeerChannel {
								u.Updates = []tg.UpdateClass{&tg.UpdateEditChannelMessage{Message: &m, Pts: 1, PtsCount: 1}}
							} else {
								u.Updates = []tg.UpdateClass{&tg.UpdateEditMessage{Message: &m, Pts: 1, PtsCount: 1}}
							}
						case "unrelated-reaction":
							u.Updates = []tg.UpdateClass{&tg.UpdateMessageReactions{Peer: raw.PeerID, MsgID: raw.ID + 1}}
						}
						out.(*tg.UpdatesBox).Updates = u
					case *tg.MessagesGetMessagesRequest, *tg.ChannelsGetMessagesRequest:
						fetches++
						m := *raw // Full targeted read omits reactions when the last one is removed.
						out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&m}}
					default:
						t.Fatalf("unexpected %T", in)
					}
					return nil
				}))
				req := engine.ReactRequest{Key: key, WriteOptions: engine.WriteOptions{DryRun: true}}
				p, err := e.React(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				req.WriteOptions = engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
				result, err := e.React(ctx, req)
				if !strings.Contains(result.Note, "reaction response: updates(count=") {
					t.Errorf("bridge receipt lacks safe TL shape capture: %s", result.Note)
				}
				if strings.Contains(result.Note, raw.Message) || strings.Contains(result.Note, "👍") {
					t.Error("receipt diagnostic leaked message or emoji")
				}
				if err != nil || !result.Done || fetches != 1 {
					t.Errorf("receipt=%+v fetches=%d err=%v", result, fetches, err)
				}
				m, _, err := db.Message(ctx, key)
				if err != nil || len(m.Reactions) != 0 {
					t.Errorf("stale reaction: %+v err=%v", m.Reactions, err)
				}
				select {
				case ev := <-events:
					if ev.Kind != engine.EventReaction || len(ev.Reaction.Counts) != 0 || len(ev.Reaction.Removed) != 1 {
						t.Errorf("bad removal event %+v", ev)
					}
				default:
					t.Error("no removal reaction event")
				}
				select {
				case ev := <-events:
					t.Errorf("unexpected event %s", ev.Kind)
				default:
				}
			})
		}
	}
}
