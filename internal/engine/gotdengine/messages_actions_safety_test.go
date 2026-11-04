package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func actionFixture(t *testing.T) (*Engine, engine.MessageKey) {
	t.Helper()
	e, s := writeFixture(t)
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 71}
	if _, err := s.Apply(context.Background(), engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Out: true, Date: time.Unix(1000, 0), Text: "old"}}}); err != nil {
		t.Fatal(err)
	}
	return e, key
}

func actionRunner(e *Engine, key engine.MessageKey, action string) func(engine.WriteOptions) (engine.WriteResult, error) {
	ctx := context.Background()
	return func(opts engine.WriteOptions) (engine.WriteResult, error) {
		switch action {
		case "edit":
			return e.Edit(ctx, engine.EditRequest{Key: key, Text: "new", WriteOptions: opts})
		case "delete":
			return e.Delete(ctx, engine.DeleteRequest{Peer: key.Peer, IDs: []int{key.ID}, WriteOptions: opts})
		case "react":
			return e.React(ctx, engine.ReactRequest{Key: key, Emoji: "👍", WriteOptions: opts})
		case "forward":
			return e.Forward(ctx, engine.ForwardRequest{From: key.Peer, To: engine.PeerRef{Type: engine.PeerChat, ID: 20}, IDs: []int{key.ID}, WriteOptions: opts})
		case "typing":
			return e.SetTyping(ctx, engine.TypingRequest{Peer: key.Peer, Action: engine.TypingText, WriteOptions: opts})
		default:
			return e.Organise(ctx, engine.OrganiseRequest{Peer: key.Peer, Action: engine.OrganiseArchive, WriteOptions: opts})
		}
	}
}

func TestMessageActionsPreviewAuthorization(t *testing.T) {
	for _, action := range []string{"edit", "delete", "react", "forward", "typing", "organise"} {
		for _, change := range []string{"readonly", "missing", "expired", "target hash", "target name", "message version", "failed RPC", "repreview"} {
			t.Run(action+"/"+change, func(t *testing.T) {
				e, key := actionFixture(t)
				ctx := context.Background()
				now := time.Unix(3000, 0)
				e.cfg.Clock = func() time.Time { return now }
				calls := 0
				e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
					calls++
					return tgerr.New(400, "PEER_ID_INVALID")
				}))
				run := actionRunner(e, key, action)
				if change == "readonly" {
					if err := e.SetMode(ctx, engine.ModeReadOnly); err != nil {
						t.Fatal(err)
					}
				}
				p, err := run(engine.WriteOptions{DryRun: true})
				if err != nil {
					t.Fatal(err)
				}
				if calls != 0 {
					t.Fatal("preview RPC")
				}
				opts := engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
				switch change {
				case "readonly":
					if len(p.Preview.Warnings) == 0 {
						t.Fatal("readonly warning missing")
					}
				case "missing":
					opts.ExpectFingerprint = ""
				case "expired":
					now = now.Add(10 * time.Minute)
				case "target hash", "target name":
					ref := key.Peer
					if action == "forward" {
						ref = engine.PeerRef{Type: engine.PeerChat, ID: 20}
					}
					peer, _, err := e.cfg.Store.LookupPeer(ctx, "primary", ref)
					if err != nil {
						t.Fatal(err)
					}
					hash, _, err := e.cfg.Store.AccessHash(ctx, "primary", ref)
					if err != nil {
						t.Fatal(err)
					}
					// Basic groups have no hash; changing metadata binds their identity too.
					if change == "target hash" && ref.Type == engine.PeerChat {
						peer.Title = "new"
					} else if change == "target hash" {
						hash++
					} else {
						peer.FirstName = "new"
						peer.Title = "new"
					}
					if _, err := e.cfg.Store.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: peer, AccessHash: hash}}}); err != nil {
						t.Fatal(err)
					}
				case "message version":
					if action == "typing" || action == "organise" {
						opts.IdempotencyKey = "new intent"
						break
					}
					m, _, err := e.cfg.Store.Message(ctx, key)
					if err != nil {
						t.Fatal(err)
					}
					d := now
					m.EditDate = &d
					if _, err := e.cfg.Store.Apply(ctx, engine.Batch{Account: "primary", Edits: []engine.Message{m}}); err != nil {
						t.Fatal(err)
					}
				case "failed RPC":
					if _, err := run(opts); err == nil {
						t.Fatal("expected RPC failure")
					}
					if calls != 1 {
						t.Fatal("RPC not reached")
					}
				case "repreview":
					now = now.Add(9 * time.Minute)
					if _, err := run(engine.WriteOptions{DryRun: true}); err != nil {
						t.Fatal(err)
					}
					now = now.Add(time.Minute)
				}
				before := calls
				_, err = run(opts)
				want := engine.ErrPreviewMismatch
				if change == "readonly" {
					want = engine.ErrReadOnly
				}
				if !errors.Is(err, want) || calls != before {
					t.Fatalf("err=%v calls=%d before=%d", err, calls, before)
				}
			})
		}
	}
}

func TestMessageActionsReadOnlyWinsBeforeValidation(t *testing.T) {
	e, _ := writeFixture(t)
	if err := e.SetMode(context.Background(), engine.ModeReadOnly); err != nil {
		t.Fatal(err)
	}
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("readonly RPC"); return nil }))
	for _, action := range []string{"edit", "delete", "react", "forward", "typing", "organise"} {
		if _, err := actionRunner(e, engine.MessageKey{}, action)(engine.WriteOptions{}); !errors.Is(err, engine.ErrReadOnly) {
			t.Fatalf("%s: %v", action, err)
		}
	}
}

func TestMessageActionsDeleteEveryoneConfirmation(t *testing.T) {
	for _, channel := range []bool{false, true} {
		t.Run(fmt.Sprint(channel), func(t *testing.T) {
			e, key := actionFixture(t)
			ctx := context.Background()
			if channel {
				key.Peer = engine.PeerRef{Type: engine.PeerChannel, ID: 30}
				if _, err := e.cfg.Store.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Out: true, Text: "channel"}}}); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				calls++
				if channel {
					if _, ok := in.(*tg.ChannelsDeleteMessagesRequest); !ok {
						t.Fatalf("wrong channel RPC %T", in)
					}
				} else if !in.(*tg.MessagesDeleteMessagesRequest).Revoke {
					t.Fatal("revoke false")
				}
				return nil
			}))
			r := engine.DeleteRequest{Peer: key.Peer, IDs: []int{key.ID}, Revoke: true, WriteOptions: engine.WriteOptions{DryRun: true}}
			p, err := e.Delete(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			r.DryRun = false
			r.ExpectFingerprint = p.Preview.Fingerprint
			if _, err := e.Delete(ctx, r); !errors.Is(err, engine.ErrInvalid) || calls != 0 {
				t.Fatalf("unconfirmed global deletion: %v", err)
			}
			if got, err := e.DeleteConfirmed(ctx, r, true); err != nil || !got.Done || calls != 1 {
				t.Fatalf("%+v %v calls=%d", got, err, calls)
			}
		})
	}
}

type actionSnapshotStore struct {
	engine.Persister
	message engine.Message
}

func (s actionSnapshotStore) Message(_ context.Context, key engine.MessageKey) (engine.Message, bool, error) {
	return s.message, key == s.message.Key, nil
}

func TestMessageActionsForwardProtectedSnapshots(t *testing.T) {
	for _, protection := range []string{"peer", "message", "media TTL", "message TTL", "expiry"} {
		t.Run(protection, func(t *testing.T) {
			e, key := actionFixture(t)
			ctx := context.Background()
			m, _, _ := e.cfg.Store.Message(ctx, key)
			switch protection {
			case "peer":
				p, _, _ := e.cfg.Store.LookupPeer(ctx, "primary", key.Peer)
				p.Protected = true
				if _, err := e.cfg.Store.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: p, AccessHash: 1010}}}); err != nil {
					t.Fatal(err)
				}
			case "message":
				m.Protected = true
			case "media TTL":
				m.Media = &engine.Media{Type: engine.MediaPhoto, TTLSeconds: 5}
			case "message TTL":
				m.TTLPeriod = 3600
			case "expiry":
				d := e.now().Add(time.Hour)
				m.ExpiresAt = &d
			}
			// The lifecycle store deliberately drops protected/TTL content. Inject
			// the snapshot to verify the engine's own independent forwarding gate.
			e.cfg.Store = actionSnapshotStore{Persister: e.cfg.Store, message: m}
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("protected forward RPC"); return nil }))
			_, err := e.Forward(ctx, engine.ForwardRequest{From: key.Peer, To: engine.PeerRef{Type: engine.PeerChat, ID: 20}, IDs: []int{key.ID}, WriteOptions: engine.WriteOptions{DryRun: true}})
			if !errors.Is(err, engine.ErrProtectedContent) {
				t.Fatal(err)
			}
		})
	}
}

func TestMessageActionsEditMentionsAndCustomReaction(t *testing.T) {
	e, key := actionFixture(t)
	ctx := context.Background()
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		switch r := in.(type) {
		case *tg.MessagesEditMessageRequest:
			entity := r.Entities[0].(*tg.InputMessageEntityMentionName)
			if r.Message != "😀 Ana" || entity.Offset != 3 || entity.Length != 3 || entity.UserID.(*tg.InputUser).AccessHash != 1010 {
				t.Fatalf("wrong mention %+v", r)
			}
			if !r.Flags.Has(11) || !r.Flags.Has(3) {
				t.Fatalf("missing edit flags %+v", r.Flags)
			}
		case *tg.MessagesSendReactionRequest:
			if r.Reaction[0].(*tg.ReactionCustomEmoji).DocumentID != 1234 || !r.Big || !r.Flags.Has(0) {
				t.Fatalf("wrong custom reaction %+v", r)
			}
		case *tg.MessagesGetMessagesRequest:
			if len(r.ID) != 1 || r.ID[0].(*tg.InputMessageID).ID != key.ID {
				t.Fatal("wrong custom reaction reconciliation target")
			}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{reactionReadFixture(key, "old", &tg.ReactionCustomEmoji{DocumentID: 1234})}}
			return nil
		default:
			t.Fatalf("unexpected %T", r)
		}
		out.(*tg.UpdatesBox).Updates = &tg.Updates{}
		return nil
	}))
	r := engine.EditRequest{Key: key, Text: "😀 Ana", Mentions: []engine.Mention{{User: key.Peer, Text: "Ana"}}, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.Edit(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	if _, err := e.Edit(ctx, r); err != nil {
		t.Fatal(err)
	}
	reaction := engine.ReactRequest{Key: key, Emoji: "custom:1234", Big: true, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err = e.React(ctx, reaction)
	if err != nil {
		t.Fatal(err)
	}
	reaction.DryRun = false
	reaction.ExpectFingerprint = p.Preview.Fingerprint
	if _, err := e.React(ctx, reaction); err != nil {
		t.Fatal(err)
	}
	if calls != 3 { // Edit, reaction and authoritative reaction read.
		t.Fatal(calls)
	}
}
