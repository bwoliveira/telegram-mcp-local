package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestMessageActionsPeerNamespaces(t *testing.T) {
	for _, tc := range []struct {
		ref   engine.PeerRef
		input tg.InputPeerClass
	}{
		{engine.PeerRef{Type: engine.PeerUser, ID: 99}, &tg.InputPeerSelf{}},
		{engine.PeerRef{Type: engine.PeerUser, ID: 10}, &tg.InputPeerUser{UserID: 10, AccessHash: 1010}},
		{engine.PeerRef{Type: engine.PeerChat, ID: 20}, &tg.InputPeerChat{ChatID: 20}},
		{engine.PeerRef{Type: engine.PeerChannel, ID: 30}, &tg.InputPeerChannel{ChannelID: 30, AccessHash: 3030}},
	} {
		for _, action := range []string{"edit", "react", "typing", "organise", "delete", "forward"} {
			t.Run(fmt.Sprintf("%s/%s:%d", action, tc.ref.Type, tc.ref.ID), func(t *testing.T) {
				e, s := writeFixture(t)
				ctx := context.Background()
				key := engine.MessageKey{Account: "primary", Peer: tc.ref, ID: 72}
				if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Text: "source", Out: true, Date: time.Unix(1000, 0)}}}); err != nil {
					t.Fatal(err)
				}
				calls := 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					calls++
					var input tg.InputPeerClass
					switch r := in.(type) {
					case *tg.MessagesEditMessageRequest:
						input = r.Peer
					case *tg.MessagesSendReactionRequest:
						input = r.Peer
					case *tg.MessagesGetMessagesRequest:
						if action != "react" || tc.ref.Type == engine.PeerChannel || len(r.ID) != 1 || r.ID[0].(*tg.InputMessageID).ID != key.ID {
							t.Fatal("wrong reaction reconciliation namespace or message")
						}
						out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{reactionReadFixture(key, "source", &tg.ReactionEmoji{Emoticon: "👍"})}}
						return nil
					case *tg.ChannelsGetMessagesRequest:
						if action != "react" || tc.ref.Type != engine.PeerChannel || !reflect.DeepEqual(r.Channel, &tg.InputChannel{ChannelID: 30, AccessHash: 3030}) || len(r.ID) != 1 || r.ID[0].(*tg.InputMessageID).ID != key.ID {
							t.Fatal("wrong channel reaction reconciliation target")
						}
						out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{reactionReadFixture(key, "source", &tg.ReactionEmoji{Emoticon: "👍"})}}
						return nil
					case *tg.MessagesSetTypingRequest:
						input = r.Peer
					case *tg.FoldersEditPeerFoldersRequest:
						input = r.FolderPeers[0].Peer
					case *tg.MessagesForwardMessagesRequest:
						input = r.FromPeer
					case *tg.MessagesDeleteMessagesRequest:
						if tc.ref.Type == engine.PeerChannel {
							t.Fatal("channel went to messages.deleteMessages")
						}
					case *tg.ChannelsDeleteMessagesRequest:
						if tc.ref.Type != engine.PeerChannel || !reflect.DeepEqual(r.Channel, &tg.InputChannel{ChannelID: 30, AccessHash: 3030}) {
							t.Fatal("wrong channel namespace")
						}
					default:
						t.Fatalf("unexpected %T", in)
					}
					if input != nil && !reflect.DeepEqual(input, tc.input) {
						t.Fatalf("wrong input got=%+v want=%+v", input, tc.input)
					}
					switch v := out.(type) {
					case *tg.UpdatesBox:
						v.Updates = &tg.Updates{}
					case *tg.BoolBox:
						v.Bool = &tg.BoolTrue{}
					}
					return nil
				}))
				run := actionRunner(e, key, action)
				if action == "delete" {
					run = func(opts engine.WriteOptions) (engine.WriteResult, error) {
						return e.DeleteConfirmed(ctx, engine.DeleteRequest{Peer: tc.ref, IDs: []int{key.ID}, Revoke: tc.ref.Type == engine.PeerChannel, WriteOptions: opts}, true)
					}
				}
				p, err := run(engine.WriteOptions{DryRun: true})
				if err != nil {
					t.Fatal(err)
				}
				if calls != 0 {
					t.Fatal("preview mutated")
				}
				result, err := run(engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint})
				wantCalls := 1
				if action == "react" {
					wantCalls++ // Empty acknowledgment requires a target-scoped read.
				}
				if err != nil || !result.Done || calls != wantCalls {
					t.Fatalf("%+v %v calls=%d", result, err, calls)
				}
			})
		}
	}
}

func TestMessageActionsForwardBindsEverySnapshotAndHash(t *testing.T) {
	for _, change := range []string{"second source", "source hash", "source metadata", "destination hash", "drop author", "silent", "topic", "ids"} {
		t.Run(change, func(t *testing.T) {
			e, key := actionFixture(t)
			ctx := context.Background()
			second := engine.Message{Key: engine.MessageKey{Account: "primary", Peer: key.Peer, ID: 72}, Text: "second", Date: time.Unix(1000, 0)}
			if _, err := e.cfg.Store.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{second}}); err != nil {
				t.Fatal(err)
			}
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
				t.Fatal("changed forward reached RPC")
				return nil
			}))
			r := engine.ForwardRequest{From: key.Peer, To: engine.PeerRef{Type: engine.PeerChannel, ID: 30}, IDs: []int{71, 72}, WriteOptions: engine.WriteOptions{DryRun: true}}
			p, err := e.Forward(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "second source":
				d := e.now()
				second.EditDate = &d
				if _, err := e.cfg.Store.Apply(ctx, engine.Batch{Account: "primary", Edits: []engine.Message{second}}); err != nil {
					t.Fatal(err)
				}
			case "source hash", "destination hash", "source metadata":
				ref := key.Peer
				if change == "destination hash" {
					ref = r.To
				}
				peer, _, _ := e.cfg.Store.LookupPeer(ctx, "primary", ref)
				hash, _, _ := e.cfg.Store.AccessHash(ctx, "primary", ref)
				if change == "source metadata" {
					peer.FirstName = "renamed"
				} else {
					hash++
				}
				if _, err := e.cfg.Store.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: peer, AccessHash: hash}}}); err != nil {
					t.Fatal(err)
				}
			case "drop author":
				r.DropAuthor = true
			case "silent":
				r.Silent = true
			case "topic":
				r.TopicID = 60
			case "ids":
				r.IDs = []int{72, 71}
			}
			r.DryRun = false
			r.ExpectFingerprint = p.Preview.Fingerprint
			if _, err := e.Forward(ctx, r); !errors.Is(err, engine.ErrPreviewMismatch) {
				t.Fatal(err)
			}
		})
	}
}

func TestMessageActionsForwardResponseAndRandomIDs(t *testing.T) {
	e, key := actionFixture(t)
	ctx := context.Background()
	if _, err := e.cfg.Store.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: engine.MessageKey{Account: "primary", Peer: key.Peer, ID: 72}, Text: "second", Date: time.Unix(1000, 0)}}}); err != nil {
		t.Fatal(err)
	}
	var first []int64
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		r := in.(*tg.MessagesForwardMessagesRequest)
		if len(r.RandomID) != 2 || r.RandomID[0] == r.RandomID[1] {
			t.Fatalf("bad random IDs %+v", r.RandomID)
		}
		if first == nil {
			first = append([]int64(nil), r.RandomID...)
		} else if !reflect.DeepEqual(first, r.RandomID) {
			t.Fatal("unstable random IDs")
		}
		out.(*tg.UpdatesBox).Updates = &tg.Updates{Updates: []tg.UpdateClass{
			&tg.UpdateMessageID{ID: 90, RandomID: r.RandomID[0]}, &tg.UpdateMessageID{ID: 91, RandomID: r.RandomID[1]},
			&tg.UpdateNewMessage{Message: &tg.Message{ID: 90, Out: true, Date: 1001, PeerID: &tg.PeerChat{ChatID: 20}, Message: "first forwarded"}},
			&tg.UpdateNewMessage{Message: &tg.Message{ID: 91, Out: true, Date: 1002, PeerID: &tg.PeerChat{ChatID: 20}, Message: "second forwarded"}},
		}}
		return nil
	}))
	r := engine.ForwardRequest{From: key.Peer, To: engine.PeerRef{Type: engine.PeerChat, ID: 20}, IDs: []int{71, 72}, WriteOptions: engine.WriteOptions{DryRun: true, IdempotencyKey: "stable"}}
	p, err := e.Forward(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	got, err := e.Forward(ctx, r)
	if err != nil || len(got.Messages) != 2 {
		t.Fatalf("%+v %v", got, err)
	}
	for _, k := range got.Messages {
		if m, ok, err := e.cfg.Store.Message(ctx, k); err != nil || !ok || m.Text == "" {
			t.Fatalf("not persisted %+v %v %v", m, ok, err)
		}
	}
}

func TestMessageActionsEditPersistsNativeResponse(t *testing.T) {
	e, key := actionFixture(t)
	ctx := context.Background()
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		out.(*tg.UpdatesBox).Updates = &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateEditMessage{Message: &tg.Message{ID: key.ID, Out: true, Date: 1000, EditDate: 1001, PeerID: &tg.PeerUser{UserID: 10}, Message: "server edited"}}}}
		return nil
	}))
	run := actionRunner(e, key, "edit")
	p, err := run(engine.WriteOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	m, ok, err := e.cfg.Store.Message(ctx, key)
	if err != nil || !ok || m.Text != "server edited" {
		t.Fatalf("native edit not persisted %+v %v", m, err)
	}
}
