package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/gotdengine/pocstore"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func writeFixture(t *testing.T) (*Engine, *pocstore.Store) {
	t.Helper()
	e, s := offlineEngine(t)
	ctx := context.Background()
	if err := e.SetMode(ctx, engine.ModeReadWrite); err != nil {
		t.Fatal(err)
	}
	peers := []engine.PeerUpsert{
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 99}, Kind: engine.KindSelf, FirstName: "Self"}},
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Kind: engine.KindUser, FirstName: "Ana", Username: "ana"}, AccessHash: 1010},
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 11}, Kind: engine.KindUser, FirstName: "Jo😀", LastName: "Silva"}, AccessHash: 1111},
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChat, ID: 20}, Kind: engine.KindGroup, Title: "Basic group"}},
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChannel, ID: 30}, Kind: engine.KindSupergroup, Title: "Forum", Forum: true}, AccessHash: 3030},
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChannel, ID: 40}, Kind: engine.KindChannel, Title: "Broadcast"}, AccessHash: 4040},
	}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Peers: peers}); err != nil {
		t.Fatal(err)
	}
	return e, s
}

func TestProductionSendTextAllPeerTypes(t *testing.T) {
	for _, tc := range []struct {
		ref   engine.PeerRef
		input tg.InputPeerClass
	}{
		{engine.PeerRef{Type: engine.PeerUser, ID: 99}, &tg.InputPeerSelf{}},
		{engine.PeerRef{Type: engine.PeerUser, ID: 10}, &tg.InputPeerUser{UserID: 10, AccessHash: 1010}},
		{engine.PeerRef{Type: engine.PeerChat, ID: 20}, &tg.InputPeerChat{ChatID: 20}},
		{engine.PeerRef{Type: engine.PeerChannel, ID: 30}, &tg.InputPeerChannel{ChannelID: 30, AccessHash: 3030}},
		{engine.PeerRef{Type: engine.PeerChannel, ID: 40}, &tg.InputPeerChannel{ChannelID: 40, AccessHash: 4040}},
	} {
		t.Run(fmt.Sprint(tc.ref), func(t *testing.T) {
			e, s := writeFixture(t)
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				calls++
				r, ok := in.(*tg.MessagesSendMessageRequest)
				if !ok {
					t.Fatalf("unexpected RPC %T", in)
				}
				if !reflect.DeepEqual(r.Peer, tc.input) || r.Message != "Hello 😀" || !r.Silent || !r.NoWebpage {
					t.Fatalf("wrong send request: %#v", r)
				}
				out.(*tg.UpdatesBox).Updates = &tg.UpdateShortSentMessage{ID: 90, Date: 1000}
				return nil
			}))
			r := engine.SendTextRequest{Peer: tc.ref, Text: "Hello 😀", Silent: true, NoWebPage: true, WriteOptions: engine.WriteOptions{DryRun: true}}
			p, err := e.SendText(context.Background(), r)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 || p.Preview.Target.Ref != tc.ref || p.Preview.Target.Kind == "" {
				t.Fatalf("bad preview: %+v calls=%d", p, calls)
			}
			r.DryRun = false
			r.ExpectFingerprint = p.Preview.Fingerprint
			got, err := e.SendText(context.Background(), r)
			if err != nil || !got.Done || calls != 1 || len(got.Messages) != 1 {
				t.Fatalf("%+v %v calls=%d", got, err, calls)
			}
			m, ok, err := s.Message(context.Background(), got.Messages[0])
			if err != nil || !ok || m.Text != r.Text || !m.Out || !m.Silent {
				t.Fatalf("not persisted: %+v %v %v", m, ok, err)
			}
		})
	}
}

func TestProductionSendMentionUTF16ReplyAndTopic(t *testing.T) {
	e, s := writeFixture(t)
	ctx := context.Background()
	ref := engine.PeerRef{Type: engine.PeerChannel, ID: 30}
	quoted := engine.Message{Key: engine.MessageKey{Account: "primary", Peer: ref, ID: 70}, Date: time.Unix(1000, 0), Text: "Quoted version one", TopicID: 60}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{quoted}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		r, ok := in.(*tg.MessagesSendMessageRequest)
		if !ok {
			t.Fatalf("unexpected %T", in)
		}
		if r.Message != "😀 hi Ana and Jo😀" || len(r.Entities) != 2 {
			t.Fatalf("%#v", r)
		}
		first := r.Entities[0].(*tg.InputMessageEntityMentionName)
		second := r.Entities[1].(*tg.InputMessageEntityMentionName)
		if first.Offset != 6 || first.Length != 3 || second.Offset != 14 || second.Length != 4 {
			t.Fatalf("wrong UTF16 spans: %+v %+v", first, second)
		}
		if !reflect.DeepEqual(first.UserID, &tg.InputUser{UserID: 10, AccessHash: 1010}) || !reflect.DeepEqual(second.UserID, &tg.InputUser{UserID: 11, AccessHash: 1111}) {
			t.Fatalf("wrong mention users: %+v %+v", first, second)
		}
		reply, ok := r.ReplyTo.(*tg.InputReplyToMessage)
		if !ok || reply.ReplyToMsgID != 70 || reply.TopMsgID != 60 {
			t.Fatalf("wrong reply: %#v", r.ReplyTo)
		}
		out.(*tg.UpdatesBox).Updates = &tg.UpdateShortSentMessage{ID: 91, Date: 1001}
		return nil
	}))
	r := engine.SendTextRequest{Peer: ref, Text: "😀 hi Ana and Jo😀", ReplyTo: 70, TopicID: 60, Mentions: []engine.Mention{{User: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Text: "Ana"}, {User: engine.PeerRef{Type: engine.PeerUser, ID: 11}, Text: "Jo😀"}}, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.SendText(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if p.Preview.Quoted == nil || p.Preview.Quoted.Text != quoted.Text || len(p.Preview.Users) != 2 || len(p.Preview.Entities) != 2 || calls != 0 {
		t.Fatalf("incomplete preview: %+v", p.Preview)
	}
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	got, err := e.SendText(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	m, ok, err := s.Message(ctx, got.Messages[0])
	if err != nil || !ok || m.TopicID != 60 || m.ReplyTo == nil || m.ReplyTo.ID != 70 || len(m.Entities) != 2 {
		t.Fatalf("reply/entities not persisted: %+v %v", m, err)
	}
}

func TestProductionSendDefaultMentionLabelsAndRepeatedText(t *testing.T) {
	for _, tc := range []struct {
		name, text       string
		mentions         []engine.Mention
		want             string
		offsets, lengths []int
	}{
		{"username", "hi @ana", []engine.Mention{{User: engine.PeerRef{Type: engine.PeerUser, ID: 10}}}, "hi @ana", []int{3}, []int{4}},
		{"append name", "hi 😀", []engine.Mention{{User: engine.PeerRef{Type: engine.PeerUser, ID: 11}}}, "hi 😀 Jo😀 Silva", []int{6}, []int{10}},
		{"distinct spans", "Ana Ana", []engine.Mention{{User: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Text: "Ana"}, {User: engine.PeerRef{Type: engine.PeerUser, ID: 11}, Text: "Ana"}}, "Ana Ana", []int{0, 4}, []int{3, 3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, _ := writeFixture(t)
			p, err := e.SendText(context.Background(), engine.SendTextRequest{Peer: engine.PeerRef{Type: engine.PeerChat, ID: 20}, Text: tc.text, Mentions: tc.mentions, WriteOptions: engine.WriteOptions{DryRun: true}})
			if err != nil {
				t.Fatal(err)
			}
			if p.Preview.Text != tc.want || len(p.Preview.Entities) != len(tc.offsets) {
				t.Fatalf("%+v", p.Preview)
			}
			for i, ent := range p.Preview.Entities {
				if ent.Offset != tc.offsets[i] || ent.Length != tc.lengths[i] {
					t.Fatalf("wrong span: %+v", ent)
				}
			}
		})
	}
}

func TestProductionSendFingerprintBindsResolvedState(t *testing.T) {
	for _, change := range []string{"target hash", "target name", "mention hash", "mention name", "quoted edit", "topic", "silent", "no webpage", "idempotency"} {
		t.Run(change, func(t *testing.T) {
			e, s := writeFixture(t)
			ctx := context.Background()
			ref := engine.PeerRef{Type: engine.PeerChannel, ID: 30}
			quoted := engine.Message{Key: engine.MessageKey{Account: "primary", Peer: ref, ID: 70}, Date: time.Unix(1000, 0), Text: "quote", TopicID: 60}
			if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{quoted}}); err != nil {
				t.Fatal(err)
			}
			var calls atomic.Int32
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
				calls.Add(1)
				return errors.New("must not invoke")
			}))
			r := engine.SendTextRequest{Peer: ref, Text: "Hello Ana", ReplyTo: 70, TopicID: 60, Mentions: []engine.Mention{{User: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Text: "Ana"}}, WriteOptions: engine.WriteOptions{DryRun: true}}
			p, err := e.SendText(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "target hash", "target name", "mention hash", "mention name":
				peerRef := ref
				if strings.HasPrefix(change, "mention") {
					peerRef = r.Mentions[0].User
				}
				p, _, err := s.LookupPeer(ctx, "primary", peerRef)
				if err != nil {
					t.Fatal(err)
				}
				h, _, err := s.AccessHash(ctx, "primary", peerRef)
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasSuffix(change, "hash") {
					h++
				} else {
					p.FirstName = "changed"
					p.Title = "changed"
				}
				if _, err = s.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: p, AccessHash: h}}}); err != nil {
					t.Fatal(err)
				}
			case "quoted edit":
				d := time.Unix(1002, 0)
				quoted.EditDate = &d // A new version with identical text must also invalidate consent.
				if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Edits: []engine.Message{quoted}}); err != nil {
					t.Fatal(err)
				}
			case "topic":
				r.TopicID = 61
				r.ReplyTo = 0
			case "silent":
				r.Silent = true
			case "no webpage":
				r.NoWebPage = true
			case "idempotency":
				r.IdempotencyKey = "other"
			}
			r.DryRun = false
			r.ExpectFingerprint = p.Preview.Fingerprint
			if _, err := e.SendText(ctx, r); !errors.Is(err, engine.ErrPreviewMismatch) {
				t.Fatalf("state change not refused: %v", err)
			}
			if calls.Load() != 0 {
				t.Fatal("state change reached Telegram")
			}
		})
	}
}

func TestProductionPreviewWindowNeverRenewsOrRevives(t *testing.T) {
	for _, action := range []string{"send", "read"} {
		for _, scenario := range []string{"missing", "different", "expired", "repreview expiry", "consumed repreview", "failed RPC consumed"} {
			t.Run(action+"/"+scenario, func(t *testing.T) {
				e, _ := writeFixture(t)
				now := time.Unix(2000, 0)
				e.cfg.Clock = func() time.Time { return now }
				calls := 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					calls++
					if scenario == "failed RPC consumed" {
						return tgerr.New(400, "PEER_ID_INVALID")
					}
					switch in.(type) {
					case *tg.MessagesSendMessageRequest:
						out.(*tg.UpdatesBox).Updates = &tg.UpdateShortSentMessage{ID: 97, Date: 2000}
					case *tg.MessagesReadHistoryRequest:
					default:
						t.Fatalf("unexpected RPC %T", in)
					}
					return nil
				}))
				run := func(dry bool, fp string) (engine.WriteResult, error) {
					opts := engine.WriteOptions{DryRun: dry, ExpectFingerprint: fp}
					if action == "send" {
						return e.SendText(context.Background(), engine.SendTextRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, Text: "hello", WriteOptions: opts})
					}
					return e.MarkRead(context.Background(), engine.MarkReadRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, MaxID: 12, WriteOptions: opts})
				}
				p, err := run(true, "")
				if err != nil {
					t.Fatal(err)
				}
				fp := p.Preview.Fingerprint
				switch scenario {
				case "missing":
					fp = ""
				case "different":
					fp = "not-the-preview"
				case "expired":
					now = now.Add(10 * time.Minute)
				case "repreview expiry":
					now = now.Add(9 * time.Minute)
					if _, err := run(true, ""); err != nil {
						t.Fatal(err)
					}
					now = now.Add(time.Minute)
				case "consumed repreview", "failed RPC consumed":
					_, err := run(false, fp)
					if scenario == "consumed repreview" && err != nil {
						t.Fatal(err)
					}
					if scenario == "failed RPC consumed" && err == nil {
						t.Fatal("RPC did not fail")
					}
					if _, err := run(true, ""); err != nil {
						t.Fatal(err)
					}
				}
				before := calls
				if _, err := run(false, fp); !errors.Is(err, engine.ErrPreviewMismatch) {
					t.Fatalf("invalid preview allowed: %v", err)
				}
				if calls != before {
					t.Fatal("invalid preview caused RPC")
				}
			})
		}
	}
}

func TestProductionPreviewConcurrentConfirmationSingleUse(t *testing.T) {
	e, _ := writeFixture(t)
	var calls atomic.Int32
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls.Add(1)
		out.(*tg.UpdatesBox).Updates = &tg.UpdateShortSentMessage{ID: 98, Date: 2000}
		return nil
	}))
	r := engine.SendTextRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, Text: "concurrent", WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.SendText(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	var wg sync.WaitGroup
	var successes atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.SendText(context.Background(), r)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, engine.ErrPreviewMismatch) {
				t.Errorf("%v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 || successes.Load() != 1 {
		t.Fatalf("calls=%d success=%d", calls.Load(), successes.Load())
	}
}

func TestProductionMarkReadRoutesOnlyExplicitConfirmedRPC(t *testing.T) {
	for _, ref := range []engine.PeerRef{{Type: engine.PeerUser, ID: 99}, {Type: engine.PeerUser, ID: 10}, {Type: engine.PeerChat, ID: 20}, {Type: engine.PeerChannel, ID: 30}} {
		for _, maxID := range []int{0, 72} {
			t.Run(fmt.Sprintf("%v/%d", ref, maxID), func(t *testing.T) {
				e, _ := writeFixture(t)
				calls := 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					calls++
					switch r := in.(type) {
					case *tg.MessagesReadHistoryRequest:
						if ref.Type == engine.PeerChannel || r.MaxID != maxID {
							t.Fatalf("wrong request %+v", r)
						}
					case *tg.ChannelsReadHistoryRequest:
						if ref.Type != engine.PeerChannel || r.MaxID != maxID || !reflect.DeepEqual(r.Channel, &tg.InputChannel{ChannelID: 30, AccessHash: 3030}) {
							t.Fatalf("wrong channel request %+v", r)
						}
						out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
					default:
						t.Fatalf("unexpected RPC %T", in)
					}
					return nil
				}))
				r := engine.MarkReadRequest{Peer: ref, MaxID: maxID, WriteOptions: engine.WriteOptions{DryRun: true}}
				p, err := e.MarkRead(context.Background(), r)
				if err != nil {
					t.Fatal(err)
				}
				if calls != 0 || p.Preview.Action != "mark_read" || p.Preview.Reversible || len(p.Preview.Warnings) == 0 {
					t.Fatalf("bad mark read preview %+v", p)
				}
				r.DryRun = false
				r.ExpectFingerprint = p.Preview.Fingerprint
				got, err := e.MarkRead(context.Background(), r)
				if err != nil || !got.Done || calls != 1 {
					t.Fatalf("%+v %v calls=%d", got, err, calls)
				}
			})
		}
	}
}

func TestProductionWriteValidationAndReadOnlyGate(t *testing.T) {
	e, _ := writeFixture(t)
	ctx := context.Background()
	var calls atomic.Int32
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls.Add(1)
		return errors.New("no RPC expected")
	}))
	ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
	for _, r := range []engine.SendTextRequest{
		{Peer: ref, Text: ""}, {Peer: ref, Text: "  \n"}, {Peer: ref, Text: strings.Repeat("😀", 2049)},
		{Peer: ref, Text: string([]byte{0xff})}, {Peer: engine.PeerRef{Type: engine.PeerUser, ID: -1}, Text: "x"},
		{Peer: engine.PeerRef{Type: "invalid", ID: 10}, Text: "x"}, {Peer: ref, Text: "x", ReplyTo: -1}, {Peer: ref, Text: "x", TopicID: -1}, {Peer: ref, Text: "x", TopicID: 3},
		{Peer: ref, Text: "x", Mentions: []engine.Mention{{User: engine.PeerRef{Type: engine.PeerChat, ID: 20}, Text: "x"}}},
		{Peer: ref, Text: "x", Mentions: []engine.Mention{{User: ref, Text: "missing"}}},
	} {
		r.DryRun = true
		if _, err := e.SendText(ctx, r); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("invalid request accepted %+v: %v", r, err)
		}
	}
	if _, err := e.MarkRead(ctx, engine.MarkReadRequest{Peer: ref, MaxID: -1, WriteOptions: engine.WriteOptions{DryRun: true}}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatal(err)
	}
	if err := e.SetMode(ctx, engine.ModeReadOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SendText(ctx, engine.SendTextRequest{}); !errors.Is(err, engine.ErrReadOnly) {
		t.Fatal(err)
	}
	if _, err := e.MarkRead(ctx, engine.MarkReadRequest{}); !errors.Is(err, engine.ErrReadOnly) {
		t.Fatal(err)
	}
	for _, send := range []bool{true, false} {
		var p engine.WriteResult
		var err error
		if send {
			p, err = e.SendText(ctx, engine.SendTextRequest{Peer: ref, Text: "x", WriteOptions: engine.WriteOptions{DryRun: true}})
		} else {
			p, err = e.MarkRead(ctx, engine.MarkReadRequest{Peer: ref, WriteOptions: engine.WriteOptions{DryRun: true}})
		}
		if err != nil || len(p.Preview.Warnings) == 0 {
			t.Fatal(p, err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("preview/invalid/read-only called RPC")
	}
}

func TestProductionSendTopicOnlyAndSelfMention(t *testing.T) {
	e, s := writeFixture(t)
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		r := in.(*tg.MessagesSendMessageRequest)
		reply := r.ReplyTo.(*tg.InputReplyToMessage)
		if reply.ReplyToMsgID != 60 {
			t.Fatalf("topic-only send lost topic: %+v", reply)
		}
		if _, ok := r.Entities[0].(*tg.InputMessageEntityMentionName).UserID.(*tg.InputUserSelf); !ok {
			t.Fatal("self mention not resolved as self")
		}
		out.(*tg.UpdatesBox).Updates = &tg.UpdateShortSentMessage{ID: 95, Date: 2000}
		return nil
	}))
	r := engine.SendTextRequest{Peer: engine.PeerRef{Type: engine.PeerChannel, ID: 30}, Text: "Self", TopicID: 60, Mentions: []engine.Mention{{User: engine.PeerRef{Type: engine.PeerUser, ID: 99}, Text: "Self"}}, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.SendText(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	result, err := e.SendText(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	m, ok, err := s.Message(context.Background(), result.Messages[0])
	if err != nil || !ok || m.ReplyTo == nil || m.ReplyTo.ID != 60 || m.ReplyTo.TopID != 60 {
		t.Fatalf("topic reply metadata lost: %+v %v", m, err)
	}
}

func TestProductionSendUpdatesResponseAndIdempotency(t *testing.T) {
	for _, combined := range []bool{false, true} {
		t.Run(fmt.Sprint(combined), func(t *testing.T) {
			e, s := writeFixture(t)
			ctx := context.Background()
			ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
			var randomID int64
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				r := in.(*tg.MessagesSendMessageRequest)
				randomID = r.RandomID
				updates := []tg.UpdateClass{&tg.UpdateMessageID{ID: 101, RandomID: r.RandomID}, &tg.UpdateNewMessage{Message: &tg.Message{ID: 101, PeerID: &tg.PeerUser{UserID: 10}, Date: 2000, Message: r.Message, Out: true}}}
				if combined {
					out.(*tg.UpdatesBox).Updates = &tg.UpdatesCombined{Updates: updates}
				} else {
					out.(*tg.UpdatesBox).Updates = &tg.Updates{Updates: updates}
				}
				return nil
			}))
			r := engine.SendTextRequest{Peer: ref, Text: "updates", WriteOptions: engine.WriteOptions{DryRun: true, IdempotencyKey: "stable-key"}}
			p, err := e.SendText(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			r.DryRun = false
			r.ExpectFingerprint = p.Preview.Fingerprint
			result, err := e.SendText(ctx, r)
			if err != nil || !result.Done || len(result.Messages) != 1 || result.Messages[0].ID != 101 {
				t.Fatalf("%+v %v", result, err)
			}
			if m, ok, err := s.Message(ctx, result.Messages[0]); err != nil || !ok || m.Text != r.Text {
				t.Fatalf("server updates not persisted: %+v %v %v", m, ok, err)
			}
			again, err := e.sendRandomID("stable-key")
			if err != nil || again != randomID {
				t.Fatal("idempotency key is not stable", err)
			}
			other, err := e.sendRandomID("another-key")
			if err != nil || other == randomID {
				t.Fatal("different idempotency keys collided", err)
			}
		})
	}
}

func TestProductionMissingPeerHashAndQuoteFailWithoutRPC(t *testing.T) {
	for _, scenario := range []string{"unknown", "min hash", "quote", "access lost", "mention hash"} {
		t.Run(scenario, func(t *testing.T) {
			e, s := writeFixture(t)
			ctx := context.Background()
			ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
			want := engine.ErrPeerNotFound
			var calls atomic.Int32
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
				calls.Add(1)
				return errors.New("unexpected RPC")
			}))
			r := engine.SendTextRequest{Peer: ref, Text: "hi", WriteOptions: engine.WriteOptions{DryRun: true}}
			switch scenario {
			case "unknown":
				r.Peer.ID = 1234
			case "min hash", "mention hash":
				p := engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 12}, Kind: engine.KindUser, FirstName: "New"}
				if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: p, AccessHash: 1212, HashMin: true}}}); err != nil {
					t.Fatal(err)
				}
				if scenario == "min hash" {
					r.Peer = p.Ref
				} else {
					r.Text = "New"
					r.Mentions = []engine.Mention{{User: p.Ref, Text: "New"}}
				}
			case "quote":
				r.ReplyTo = 999
				want = engine.ErrMessageNotFound
			case "access lost":
				p, _, err := s.LookupPeer(ctx, "primary", ref)
				if err != nil {
					t.Fatal(err)
				}
				p.Access = engine.AccessLost
				if _, err = s.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: p, AccessHash: 1010}}}); err != nil {
					t.Fatal(err)
				}
				want = engine.ErrNoAccess
			}
			if _, err := e.SendText(ctx, r); !errors.Is(err, want) {
				t.Fatalf("%s: %v", scenario, err)
			}
			if calls.Load() != 0 {
				t.Fatal("validation performed RPC")
			}
		})
	}
}

func TestProductionMarkReadFingerprintBindsPeerHashAndMaxID(t *testing.T) {
	for _, change := range []string{"hash", "max id", "action"} {
		t.Run(change, func(t *testing.T) {
			e, s := writeFixture(t)
			ctx := context.Background()
			ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
			var calls atomic.Int32
			e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
				calls.Add(1)
				return errors.New("unexpected RPC")
			}))
			r := engine.MarkReadRequest{Peer: ref, MaxID: 7, WriteOptions: engine.WriteOptions{DryRun: true}}
			p, err := e.MarkRead(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			if change == "hash" {
				peer, _, err := s.LookupPeer(ctx, "primary", ref)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: peer, AccessHash: 2222}}}); err != nil {
					t.Fatal(err)
				}
			}
			if change == "max id" {
				r.MaxID = 8
			}
			r.DryRun = false
			r.ExpectFingerprint = p.Preview.Fingerprint
			if change == "action" {
				_, err = e.SendText(ctx, engine.SendTextRequest{Peer: ref, Text: "hi", WriteOptions: r.WriteOptions})
			} else {
				_, err = e.MarkRead(ctx, r)
			}
			if !errors.Is(err, engine.ErrPreviewMismatch) || calls.Load() != 0 {
				t.Fatalf("changed %s reached RPC: %v calls=%d", change, err, calls.Load())
			}
		})
	}
}
