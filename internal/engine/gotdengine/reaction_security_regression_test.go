package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

// These are full server messages, not security-only DTOs. Each policy must be
// persisted alongside the authoritative reaction transition, even at equal
// content revisions and when the returned reaction object remains minimal.
func TestReactionRefreshPersistsFullLifecycle(t *testing.T) {
	for _, typ := range []engine.PeerType{engine.PeerUser, engine.PeerChat, engine.PeerChannel} {
		for _, advancing := range []bool{false, true} {
			for _, minimal := range []bool{false, true} {
				for _, policy := range []string{"message-protected", "protected-media", "peer-protected", "message-ttl", "media-ttl"} {
					if typ == engine.PeerUser && policy == "peer-protected" {
						continue
					} // User has no noforwards field in TL.
					t.Run(fmt.Sprintf("%s/advance=%t/min=%t/%s", typ, advancing, minimal, policy), func(t *testing.T) {
						ref := engine.PeerRef{Type: typ, ID: 10}
						e, db, initial := actionSQLiteFixture(t, ref)
						ctx := holdMediaSendManager(t, e)
						raw := *initial
						raw.SetEditDate(1100)
						if policy == "media-ttl" || policy == "protected-media" {
							raw.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{ID: 401, AccessHash: 402, DCID: 2, FileReference: []byte("synthetic-reference"), Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "synthetic.txt"}}}})
						}
						if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateEditMessage{Message: &raw}}); err != nil {
							t.Fatal(err)
						}
						key := engine.MessageKey{Account: "primary", Peer: ref, ID: raw.ID}
						initialReaction := tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 2}}}
						if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: raw.PeerID, MsgID: raw.ID, Reactions: initialReaction}}); err != nil {
							t.Fatal(err)
						}
						before, _, err := db.Message(ctx, key)
						if err != nil {
							t.Fatal(err)
						}
						full := raw
						if advancing {
							full.SetEditDate(1200)
						}
						users := []tg.UserClass{&tg.User{ID: 123, FirstName: "Synthetic fetched user", AccessHash: 999}}
						var chats []tg.ChatClass
						switch policy {
						case "message-protected", "protected-media":
							full.Noforwards = true
						case "peer-protected":
							if typ == engine.PeerChat {
								chats = []tg.ChatClass{&tg.Chat{ID: ref.ID, Title: "Synthetic protected group", Noforwards: true}}
							} else {
								chats = []tg.ChatClass{&tg.Channel{ID: ref.ID, Title: "Synthetic protected channel", Noforwards: true, AccessHash: 1010}}
							}
						case "message-ttl":
							full.SetTTLPeriod(4000)
						case "media-ttl":
							media := *raw.Media.(*tg.MessageMediaDocument)
							media.SetTTLSeconds(60)
							full.SetMedia(&media)
						}
						snapshot := tg.MessageReactions{Min: minimal}
						if minimal {
							snapshot.Results = []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "❤️"}, Count: 99}}
						}
						full.SetReactions(snapshot)
						fetched := &tg.MessagesMessages{Messages: []tg.MessageClass{&full}, Users: users, Chats: chats}
						capture := &capturePersister{Persister: db}
						e.cfg.Store = capture
						events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 8})
						e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
							switch in.(type) {
							case *tg.MessagesSendReactionRequest:
								out.(*tg.UpdatesBox).Updates = &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: raw.PeerID, MsgID: raw.ID, Reactions: tg.MessageReactions{Min: true}}}
							case *tg.MessagesGetMessagesRequest, *tg.ChannelsGetMessagesRequest:
								out.(*tg.MessagesMessagesBox).Messages = fetched
							default:
								t.Fatalf("unexpected RPC %T", in)
							}
							return nil
						}))
						req := engine.ReactRequest{Key: key, WriteOptions: engine.WriteOptions{DryRun: true}}
						preview, err := e.React(ctx, req)
						if err != nil {
							t.Fatal(err)
						}
						req.WriteOptions = engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint}
						result, err := e.React(ctx, req)
						if !result.Done || result.Affected != 1 {
							t.Fatalf("lost accepted receipt: %+v", result)
						}
						if minimal {
							if !errors.Is(err, engine.ErrNotSupported) || !strings.Contains(result.Note, "do not blindly retry") {
								t.Errorf("concealed partial result: %+v error=%v", result, err)
							}
						} else if err != nil {
							t.Fatal(err)
						}
						got, found, err := db.Message(ctx, key)
						if err != nil || !found {
							t.Fatalf("message found=%t err=%v", found, err)
						}
						switch policy {
						case "message-protected", "protected-media", "peer-protected":
							if !got.Protected {
								t.Error("authoritative refresh dropped protection")
							}
						case "message-ttl":
							if got.TTLPeriod != 4000 || got.ExpiresAt == nil || !got.ExpiresAt.Equal(time.Unix(5000, 0)) {
								t.Errorf("lost message TTL: %+v", got)
							}
						case "media-ttl":
							if got.Media == nil || got.Media.TTLSeconds != 60 || got.Media.Downloadable || len(got.Media.Remote) != 0 || got.Media.Filename != "" || len(got.Entities) != 0 || strings.Contains(got.Text, "Synthetic original") {
								t.Errorf("temporary media not redacted: %+v", got)
							}
						}
						fetchedUser, userFound, userErr := db.LookupPeer(ctx, "primary", engine.PeerRef{Type: engine.PeerUser, ID: 123})
						if userErr != nil || !userFound || fetchedUser.FirstName != "Synthetic fetched user" {
							t.Errorf("getMessages users were discarded: found=%t peer=%+v error=%v", userFound, fetchedUser, userErr)
						}
						if policy == "peer-protected" {
							fetchedPeer, peerFound, peerErr := db.LookupPeer(ctx, "primary", ref)
							if peerErr != nil || !peerFound || !fetchedPeer.Protected {
								t.Errorf("getMessages chats were discarded: found=%t peer=%+v error=%v", peerFound, fetchedPeer, peerErr)
							}
						}
						if !reflect.DeepEqual(got.EditDate, before.EditDate) {
							t.Errorf("security-only refresh changed visible edit date: before=%v after=%v", before.EditDate, got.EditDate)
						}
						if minimal {
							if len(got.Reactions) != 1 || got.Reactions[0].Emoji != "👍" || got.Reactions[0].Count != 2 {
								t.Errorf("minimal refresh guessed reactions: %+v", got.Reactions)
							}
						} else if len(got.Reactions) != 0 {
							t.Errorf("authoritative removal did not commit: %+v", got.Reactions)
						}
						// Verify the production batch boundary, not just eventually matching reads.
						b := capture.batches[len(capture.batches)-1]
						if len(b.Messages) != 1 || len(b.Edits) != 0 || len(b.Peers) == 0 || b.Messages[0].Key != key || b.Messages[0].ReactionSnapshot != nil || b.Messages[0].Reactions != nil {
							t.Errorf("full message/peers not committed as one independent snapshot: %+v", b)
						}
						if (!minimal && len(b.Reactions) != 1) || (minimal && len(b.Reactions) != 0) {
							t.Errorf("incorrect authoritative reaction batch: %+v", b.Reactions)
						}
						var reactionEvents int
						for {
							select {
							case ev := <-events:
								if ev.Kind != engine.EventReaction {
									t.Errorf("security-only refresh emitted %s", ev.Kind)
								} else {
									reactionEvents++
								}
							default:
								want := 1
								if minimal {
									want = 0
								}
								if reactionEvents != want {
									t.Errorf("reaction events=%d want=%d", reactionEvents, want)
								}
								return
							}
						}
					})
				}
			}
		}
	}
}

func TestReactionRefreshWrongPeerCannotApplyFullMessage(t *testing.T) {
	for _, typ := range []engine.PeerType{engine.PeerUser, engine.PeerChat, engine.PeerChannel} {
		t.Run(string(typ), func(t *testing.T) {
			ref := engine.PeerRef{Type: typ, ID: 10}
			e, db, raw := actionSQLiteFixture(t, ref)
			ctx := holdMediaSendManager(t, e)
			key := engine.MessageKey{Account: "primary", Peer: ref, ID: raw.ID}
			wrong := *raw
			wrong.PeerID = mediaPeerClass(engine.PeerRef{Type: typ, ID: 123})
			wrong.Noforwards = true
			wrong.SetTTLPeriod(4000)
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.MessagesSendReactionRequest:
					out.(*tg.UpdatesBox).Updates = &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: raw.PeerID, MsgID: raw.ID, Reactions: tg.MessageReactions{Min: true}}}
				case *tg.MessagesGetMessagesRequest, *tg.ChannelsGetMessagesRequest:
					out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&wrong}, Chats: []tg.ChatClass{&tg.Chat{ID: 10, Noforwards: true}}}
				default:
					t.Fatalf("unexpected RPC %T", in)
				}
				return nil
			}))
			req := engine.ReactRequest{Key: key, WriteOptions: engine.WriteOptions{DryRun: true}}
			preview, err := e.React(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			req.WriteOptions = engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint}
			receipt, err := e.React(ctx, req)
			if !receipt.Done || !errors.Is(err, engine.ErrMessageNotFound) {
				t.Fatalf("wrong-peer receipt=%+v error=%v", receipt, err)
			}
			got, found, err := db.Message(ctx, key)
			if err != nil || !found || got.Protected || got.TTLPeriod != 0 {
				t.Fatalf("wrong-peer response mutated target: %+v found=%t error=%v", got, found, err)
			}
			wrongKey := engine.MessageKey{Account: "primary", Peer: peerRef(wrong.PeerID), ID: wrong.ID}
			if _, found, err := db.Message(ctx, wrongKey); err != nil || found {
				t.Fatalf("wrong-peer message applied: found=%t error=%v", found, err)
			}
		})
	}
}

func TestReactionRefreshStorageFailureKeepsAcceptedReceipt(t *testing.T) {
	e, db, raw := actionSQLiteFixture(t, engine.PeerRef{Type: engine.PeerUser, ID: 10})
	ctx := holdMediaSendManager(t, e)
	key := engine.MessageKey{Account: "primary", Peer: peerRef(raw.PeerID), ID: raw.ID}
	storageErr := errors.New("synthetic reaction refresh transaction failure")
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesSendReactionRequest:
			out.(*tg.UpdatesBox).Updates = &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: raw.PeerID, MsgID: raw.ID, Reactions: tg.MessageReactions{Min: true}}}
		case *tg.MessagesGetMessagesRequest:
			full := *raw
			full.Noforwards = true
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&full}}
			e.cfg.Store = mediaSendFailStore{Persister: db, err: storageErr}
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))
	req := engine.ReactRequest{Key: key, WriteOptions: engine.WriteOptions{DryRun: true}}
	preview, err := e.React(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.WriteOptions = engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint}
	receipt, err := e.React(ctx, req)
	if !receipt.Done || receipt.Affected != 1 || !errors.Is(err, storageErr) || !strings.Contains(receipt.Note, "do not blindly retry") || !e.held.Load() {
		t.Fatalf("lost accepted failure receipt=%+v error=%v held=%t", receipt, err, e.held.Load())
	}
	got, _, err := db.Message(ctx, key)
	if err != nil || got.Protected {
		t.Fatalf("failed transaction changed durable policy: %+v error=%v", got, err)
	}
}
