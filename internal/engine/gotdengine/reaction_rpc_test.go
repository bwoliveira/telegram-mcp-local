package gotdengine

import (
	"context"
	"fmt"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestReactionRPCMinimalRemovalRefresh(t *testing.T) {
	for _, typ := range []engine.PeerType{engine.PeerUser, engine.PeerChat, engine.PeerChannel} {
		for _, failure := range []string{"", "fetch", "partial", "wrong-peer"} {
			t.Run(fmt.Sprint(typ, "/", failure), func(t *testing.T) {
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
						if len(r.Reaction) != 0 {
							t.Fatal("not a removal")
						}
						out.(*tg.UpdatesBox).Updates = &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: raw.PeerID, MsgID: raw.ID, Reactions: tg.MessageReactions{Min: true}}}
					case *tg.MessagesGetMessagesRequest, *tg.ChannelsGetMessagesRequest:
						fetches++
						if failure == "fetch" {
							return engine.ErrPermission
						}
						m := *raw
						if failure == "partial" {
							m.SetReactions(tg.MessageReactions{Min: true})
						}
						if failure == "wrong-peer" {
							m.PeerID = &tg.PeerUser{UserID: 123}
						}
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
				if !result.Done || fetches != 1 {
					t.Errorf("receipt=%+v fetches=%d", result, fetches)
				}
				m, _, readErr := db.Message(ctx, key)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if failure == "" {
					if err != nil || len(m.Reactions) != 0 {
						t.Errorf("removal stale: %+v err=%v", m.Reactions, err)
					}
					select {
					case ev := <-events:
						if ev.Kind != engine.EventReaction || len(ev.Reaction.Counts) != 0 || len(ev.Reaction.Removed) != 1 {
							t.Errorf("bad removal event %+v", ev)
						}
					default:
						t.Error("no removal reaction event")
					}
				} else {
					if err == nil || len(m.Reactions) != 1 {
						t.Errorf("partial failure concealed or guessed: %+v err=%v", m.Reactions, err)
					}
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
