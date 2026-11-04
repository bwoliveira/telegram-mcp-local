package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func actionSQLiteFixture(t *testing.T, ref engine.PeerRef) (*Engine, *store.Store, *tg.Message) {
	t.Helper()
	ctx := context.Background()
	now := func() time.Time { return time.Unix(3000, 0) }
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "actions.db"), "primary", store.WithClock(now))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Session: &memorySession{}, Store: db, Clock: now})
	if err != nil {
		t.Fatal(err)
	}
	e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
	if err := db.BindSelf(ctx, "primary", e.account); err != nil {
		t.Fatal(err)
	}
	if err := e.SetMode(ctx, engine.ModeReadWrite); err != nil {
		t.Fatal(err)
	}
	kind := engine.KindUser
	if ref.Type == engine.PeerChat {
		kind = engine.KindGroup
	}
	if ref.Type == engine.PeerChannel {
		kind = engine.KindChannel
	}
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: ref, Kind: kind, Title: "Synthetic target", Access: engine.AccessOK}, AccessHash: 1010}}}); err != nil {
		t.Fatal(err)
	}
	raw := &tg.Message{ID: 8, PeerID: mediaPeerClass(ref), FromID: &tg.PeerUser{UserID: 99}, Out: true, Date: 1000, Message: "Synthetic original"}
	if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateNewMessage{Message: raw}}); err != nil {
		t.Fatal(err)
	}
	return e, db, raw
}

func actionEnvelope(name string, us ...tg.UpdateClass) tg.UpdatesClass {
	switch name {
	case "combined":
		return &tg.UpdatesCombined{Updates: us, Date: 1000, Seq: 1, SeqStart: 1}
	case "short":
		return &tg.UpdateShort{Update: us[0], Date: 1000}
	default:
		return &tg.Updates{Updates: us, Date: 1000, Seq: 1}
	}
}

// The observed real-account sequence advanced non-hidden edit dates after
// reactions, while RPC acceptance preceded manager dispatch. All data is fake.
func TestMessageActionRPCImmediateSQLite(t *testing.T) {
	for _, typ := range []engine.PeerType{engine.PeerUser, engine.PeerChat, engine.PeerChannel} {
		for _, envelope := range []string{"updates", "combined", "short"} {
			t.Run(fmt.Sprint(typ, "/", envelope), func(t *testing.T) {
				ref := engine.PeerRef{Type: typ, ID: 10}
				e, db, raw := actionSQLiteFixture(t, ref)
				ctx := holdMediaSendManager(t, e)
				key := engine.MessageKey{Account: "primary", Peer: ref, ID: raw.ID}
				events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 16})
				var returned tg.UpdatesClass
				var replies []tg.UpdatesClass
				assertEvent := func(want engine.EventKind) {
					t.Helper()
					select {
					case ev := <-events:
						if ev.Kind != want {
							t.Fatalf("event=%s want=%s", ev.Kind, want)
						}
					default:
						t.Errorf("missing synchronous %s", want)
					}
					select {
					case ev := <-events:
						t.Errorf("extra event %s", ev.Kind)
					default:
					}
				}
				calls := 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					calls++
					switch r := in.(type) {
					case *tg.MessagesEditMessageRequest:
						raw.Message = "Synthetic edited"
						raw.SetEditDate(1100)
						var u tg.UpdateClass = &tg.UpdateEditMessage{Message: raw, Pts: 2, PtsCount: 1}
						if typ == engine.PeerChannel {
							u = &tg.UpdateEditChannelMessage{Message: raw, Pts: 2, PtsCount: 1}
						}
						returned = actionEnvelope(envelope, u)
					case *tg.MessagesSendReactionRequest:
						counts := []tg.ReactionCount{}
						if len(r.Reaction) > 0 {
							c := tg.ReactionCount{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}
							c.SetChosenOrder(0)
							counts = append(counts, c)
						}
						returned = actionEnvelope(envelope, &tg.UpdateMessageReactions{Peer: raw.PeerID, MsgID: raw.ID, Reactions: tg.MessageReactions{Results: counts}})
					default:
						t.Fatalf("unexpected RPC %T", in)
					}
					out.(*tg.UpdatesBox).Updates = returned
					replies = append(replies, returned)
					return nil
				}))
				edit := engine.EditRequest{Key: key, Text: "Synthetic edited", WriteOptions: engine.WriteOptions{DryRun: true}}
				p, err := e.Edit(ctx, edit)
				if err != nil {
					t.Fatal(err)
				}
				if calls != 0 {
					t.Fatal("preview wrote")
				}
				edit.WriteOptions = engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
				r, err := e.Edit(ctx, edit)
				if err != nil || !r.Done {
					t.Fatalf("edit %+v %v", r, err)
				}
				m, _, err := db.Message(ctx, key)
				if err != nil || m.Text != "Synthetic edited" {
					t.Errorf("immediate edit=%q err=%v", m.Text, err)
				}
				assertEvent(engine.EventEdit)
				for _, emoji := range []string{"👍", ""} {
					react := engine.ReactRequest{Key: key, Emoji: emoji, WriteOptions: engine.WriteOptions{DryRun: true}}
					p, err := e.React(ctx, react)
					if err != nil {
						t.Fatal(err)
					}
					react.WriteOptions = engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}
					r, err := e.React(ctx, react)
					if err != nil || !r.Done {
						t.Fatalf("react %+v %v", r, err)
					}
					m, _, err := db.Message(ctx, key)
					want := 0
					if emoji != "" {
						want = 1
					}
					if err != nil || len(m.Reactions) != want {
						t.Errorf("immediate reaction %q = %+v err=%v", emoji, m.Reactions, err)
					}
					assertEvent(engine.EventReaction)
					// The later edit copy has the same content and a newer visible server date.
					copy := *raw
					date := 1200
					if emoji == "" {
						date = 1300
					}
					copy.SetEditDate(date)
					var u tg.UpdateClass = &tg.UpdateEditMessage{Message: &copy}
					if typ == engine.PeerChannel {
						u = &tg.UpdateEditChannelMessage{Message: &copy}
					}
					if err := e.handle(ctx, actionEnvelope(envelope, u)); err != nil {
						t.Fatal(err)
					}
					select {
					case ev := <-events:
						t.Errorf("reaction copy emitted false %s", ev.Kind)
					default:
					}
				}
				// Drain the manager projection of every response after the removal:
				// no intermediate reaction may be resurrected by queued acknowledgments.
				for _, reply := range replies {
					if err := e.handle(ctx, withoutActionReactionReplay(reply)); err != nil {
						t.Fatal(err)
					}
					m, _, err := db.Message(ctx, key)
					if err != nil || len(m.Reactions) != 0 {
						t.Errorf("queued response restored stale reactions: %+v err=%v", m.Reactions, err)
					}
				}
				if err := e.handle(ctx, returned); err != nil {
					t.Fatal(err)
				}
				select {
				case ev := <-events:
					t.Errorf("duplicate response event %s", ev.Kind)
				default:
				}
			})
		}
	}
}

func TestMessageActionImmediateFailureRetainsAcceptance(t *testing.T) {
	for _, action := range []string{"edit", "react"} {
		t.Run(action, func(t *testing.T) {
			ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
			e, db, raw := actionSQLiteFixture(t, ref)
			ctx := holdMediaSendManager(t, e)
			key := engine.MessageKey{Account: "primary", Peer: ref, ID: raw.ID}
			localErr := errors.New("synthetic action persistence failure")
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
				out.(*tg.UpdatesBox).Updates = &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: raw.PeerID, MsgID: raw.ID}}
				e.cfg.Store = mediaSendFailStore{Persister: db, err: localErr}
				return nil
			}))
			run := func(opts engine.WriteOptions) (engine.WriteResult, error) {
				if action == "edit" {
					return e.Edit(ctx, engine.EditRequest{Key: key, Text: "new", WriteOptions: opts})
				}
				return e.React(ctx, engine.ReactRequest{Key: key, Emoji: "👍", WriteOptions: opts})
			}
			p, err := run(engine.WriteOptions{DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := run(engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint})
			if !errors.Is(err, localErr) || !result.Done || result.Affected != 1 || !strings.Contains(result.Note, "do not blindly retry") || !e.held.Load() {
				t.Fatalf("lost receipt/error result=%+v err=%v", result, err)
			}
		})
	}
}
