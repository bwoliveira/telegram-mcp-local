package fake_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

func newFake(t *testing.T, cfg engine.Config) *fake.Controller {
	t.Helper()
	c, err := fake.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c.(*fake.Controller)
}
func key(peer engine.PeerRef, id int) engine.MessageKey {
	return engine.MessageKey{Account: "test", Peer: peer, ID: id}
}
func confirm(t *testing.T, call func(engine.WriteOptions) (engine.WriteResult, error)) engine.WriteResult {
	t.Helper()
	p, err := call(engine.WriteOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err := call(engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Done || r.DryRun {
		t.Fatalf("not executed: %+v", r)
	}
	return r
}

func TestEveryWriteUsesOneGate(t *testing.T) {
	media := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(media, []byte("synthetic document"), 0600); err != nil {
		t.Fatal(err)
	}
	title := "Changed synthetic group"
	for _, name := range []string{"send_text", "send_media", "send_location", "send_poll", "edit", "delete", "react", "forward", "mark_read", "typing", "organise", "participants", "update_group", "invite_link", "leave"} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, engine.Config{Account: "test"})
			ctx := context.Background()
			call := func(opts engine.WriteOptions) (engine.WriteResult, error) {
				switch name {
				case "send_text":
					return f.SendText(ctx, engine.SendTextRequest{Peer: fake.PrivatePeer, Text: "hello", WriteOptions: opts})
				case "send_media":
					return f.SendMedia(ctx, engine.SendMediaRequest{Peer: fake.PrivatePeer, Path: media, Kind: engine.MediaDocument, WriteOptions: opts})
				case "send_location":
					return f.SendLocation(ctx, engine.SendLocationRequest{Peer: fake.PrivatePeer, Location: engine.Location{Latitude: -23, Longitude: -46}, WriteOptions: opts})
				case "send_poll":
					return f.SendPoll(ctx, engine.SendPollRequest{Peer: fake.PrivatePeer, Question: "Which?", Options: []string{"Alpha", "Beta"}, WriteOptions: opts})
				case "edit":
					return f.Edit(ctx, engine.EditRequest{Key: key(fake.SelfPeer, 8), Text: "updated", WriteOptions: opts})
				case "delete":
					return f.Delete(ctx, engine.DeleteRequest{Peer: fake.PrivatePeer, IDs: []int{1}, Revoke: true, ConfirmRevoke: !opts.DryRun, WriteOptions: opts})
				case "react":
					return f.React(ctx, engine.ReactRequest{Key: key(fake.PrivatePeer, 1), Emoji: "👍", WriteOptions: opts})
				case "forward":
					return f.Forward(ctx, engine.ForwardRequest{From: fake.PrivatePeer, IDs: []int{1}, To: fake.BasicGroupPeer, WriteOptions: opts})
				case "mark_read":
					return f.MarkRead(ctx, engine.MarkReadRequest{Peer: fake.PrivatePeer, WriteOptions: opts})
				case "typing":
					return f.SetTyping(ctx, engine.TypingRequest{Peer: fake.PrivatePeer, Action: engine.TypingText, WriteOptions: opts})
				case "organise":
					return f.Organise(ctx, engine.OrganiseRequest{Peer: fake.PrivatePeer, Action: engine.OrganiseArchive, WriteOptions: opts})
				case "participants":
					r, e := f.ManageParticipants(ctx, engine.ParticipantsRequest{Group: fake.BasicGroupPeer, Action: engine.ParticipantsPromote, Users: []engine.PeerRef{fake.PrivatePeer}, WriteOptions: opts})
					return r.WriteResult, e
				case "update_group":
					return f.UpdateGroup(ctx, engine.UpdateGroupRequest{Group: fake.BasicGroupPeer, Title: &title, WriteOptions: opts})
				case "invite_link":
					r, e := f.InviteLink(ctx, engine.InviteLinkRequest{Group: fake.BasicGroupPeer, Revoke: true, WriteOptions: opts})
					return r.WriteResult, e
				case "leave":
					return f.Leave(ctx, engine.LeaveRequest{Group: fake.BasicGroupPeer, WriteOptions: opts})
				default:
					panic("missing test action")
				}
			}
			before := f.Snapshot()
			if _, err := call(engine.WriteOptions{}); !errors.Is(err, engine.ErrReadOnly) {
				t.Fatalf("gate: %v", err)
			}
			p, err := call(engine.WriteOptions{DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			if p.Preview == nil || !p.DryRun || len(p.Preview.Warnings) == 0 {
				t.Fatalf("preview %+v", p)
			}
			if !reflect.DeepEqual(before, f.Snapshot()) || len(f.Writes()) != 0 {
				t.Fatal("preview or blocked action changed data")
			}
			if err := f.SetMode(ctx, engine.ModeReadWrite); err != nil {
				t.Fatal(err)
			}
			if _, err := call(engine.WriteOptions{}); !errors.Is(err, engine.ErrPreviewMismatch) {
				t.Fatalf("missing fingerprint: %v", err)
			}
			if _, err := call(engine.WriteOptions{ExpectFingerprint: "wrong"}); !errors.Is(err, engine.ErrPreviewMismatch) {
				t.Fatalf("wrong fingerprint: %v", err)
			}
			result, err := call(engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Done || result.DryRun || len(f.Writes()) != 1 {
				t.Fatalf("write result %+v", result)
			}
		})
	}
}

func TestFingerprintExpirySingleUseAndIdempotency(t *testing.T) {
	now := fake.Epoch()
	f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite, Clock: func() time.Time { return now }})
	ctx := context.Background()
	r := engine.SendTextRequest{Peer: fake.PrivatePeer, Text: "first", WriteOptions: engine.WriteOptions{DryRun: true}}
	p, _ := f.SendText(ctx, r)
	r.Text = "changed"
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	if _, e := f.SendText(ctx, r); !errors.Is(e, engine.ErrPreviewMismatch) {
		t.Fatalf("changed args: %v", e)
	}
	r.Text = "first"
	now = now.Add(10 * time.Minute)
	if _, e := f.SendText(ctx, r); !errors.Is(e, engine.ErrPreviewMismatch) {
		t.Fatalf("expired: %v", e)
	}
	r.DryRun = true
	p, _ = f.SendText(ctx, r)
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	r.IdempotencyKey = "send-once"
	first, e := f.SendText(ctx, r)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.SendText(ctx, r)
	if e != nil || !reflect.DeepEqual(first, again) || len(f.Writes()) != 1 {
		t.Fatalf("retry %+v %v", again, e)
	}
	r.IdempotencyKey = ""
	if _, e := f.SendText(ctx, r); !errors.Is(e, engine.ErrPreviewMismatch) {
		t.Fatalf("reused approval: %v", e)
	}
	r.Text = "changed"
	r.IdempotencyKey = "send-once"
	if _, e := f.SendText(ctx, r); !errors.Is(e, engine.ErrPreviewMismatch) {
		t.Fatalf("idempotency key reused with changed args: %v", e)
	}
}

func TestStateChangingWritesCanBeRetried(t *testing.T) {
	for _, name := range []string{"delete", "edit", "leave"} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite})
			ctx := context.Background()
			call := func(o engine.WriteOptions) (engine.WriteResult, error) {
				switch name {
				case "delete":
					return f.Delete(ctx, engine.DeleteRequest{Peer: fake.PrivatePeer, IDs: []int{1}, WriteOptions: o})
				case "edit":
					return f.Edit(ctx, engine.EditRequest{Key: key(fake.SelfPeer, 8), Text: "edited", WriteOptions: o})
				default:
					return f.Leave(ctx, engine.LeaveRequest{Group: fake.BasicGroupPeer, WriteOptions: o})
				}
			}
			p, e := call(engine.WriteOptions{DryRun: true})
			if e != nil {
				t.Fatal(e)
			}
			opts := engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint, IdempotencyKey: "stable-key"}
			first, e := call(opts)
			if e != nil {
				t.Fatal(e)
			}
			again, e := call(opts)
			if e != nil || !reflect.DeepEqual(first, again) || len(f.Writes()) != 1 {
				t.Fatalf("retry: %v", e)
			}
		})
	}
}

func TestMentionsUseUTF16AndSnapshotsAreOwned(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite})
	ctx := context.Background()
	r := engine.SendTextRequest{Peer: fake.SupergroupPeer, Text: "😀 Ana, hello", Mentions: []engine.Mention{{User: fake.PrivatePeer, Text: "Ana"}}}
	result := confirm(t, func(o engine.WriteOptions) (engine.WriteResult, error) { r.WriteOptions = o; return f.SendText(ctx, r) })
	e := result.Preview.Entities[0]
	if e.Offset != 3 || e.Length != 3 || e.UserID != 1001 {
		t.Fatalf("mention: %+v", e)
	}
	r.Mentions[0].Text = "mutated request"
	result.Preview.Entities[0].Offset = 999
	journal := f.Writes()
	if journal[0].Result.Preview.Entities[0].Offset != 3 || journal[0].Request.(engine.SendTextRequest).Mentions[0].Text != "Ana" {
		t.Fatal("journal aliases request/result")
	}
	journal[0].Result.Preview.Entities[0].Offset = 700
	if f.Writes()[0].Result.Preview.Entities[0].Offset != 3 {
		t.Fatal("journal aliases snapshot")
	}
	d := f.Snapshot()
	d.Peers[0].FirstName = "not shared"
	d.Messages[0].Text = "not shared"
	if reflect.DeepEqual(d, f.Snapshot()) {
		t.Fatal("snapshot mutation leaked")
	}
}

func TestPendingDeletionDoesNotResurrectInHistory(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test"})
	ctx := context.Background()
	if e := f.Publish(ctx, engine.Batch{Deletes: []engine.Deletion{{IDs: []int{50}}}}); e != nil {
		t.Fatal(e)
	}
	m := engine.Message{Key: key(fake.PrivatePeer, 50), Text: "must not reappear", Date: fake.Epoch()}
	if e := f.Publish(ctx, engine.Batch{Source: engine.SourceBackfill, Messages: []engine.Message{m}}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.FetchWindow(ctx, engine.WindowRequest{Key: m.Key}); !errors.Is(e, engine.ErrMessageNotFound) {
		t.Fatalf("deleted content resurrected: %v", e)
	}
	m.Key.Peer = fake.ChannelPeer
	if e := f.Publish(ctx, engine.Batch{Messages: []engine.Message{m}}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.FetchWindow(ctx, engine.WindowRequest{Key: m.Key}); e != nil {
		t.Fatalf("channel namespace incorrectly tombstoned: %v", e)
	}
}

func TestBackfillAdvancesCursor(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test"})
	ctx := context.Background()
	first, e := f.Backfill(ctx, engine.BackfillRequest{Peer: fake.PrivatePeer, Count: 2, Rounds: 1})
	if e != nil {
		t.Fatal(e)
	}
	second, e := f.Backfill(ctx, engine.BackfillRequest{Peer: fake.PrivatePeer, Count: 2, Rounds: 1})
	if e != nil {
		t.Fatal(e)
	}
	if first.Complete || second.OldestID >= first.OldestID {
		t.Fatalf("cursor did not advance: first %+v second %+v", first, second)
	}
	for i := 0; i < 10; i++ {
		r, e := f.Backfill(ctx, engine.BackfillRequest{Peer: fake.PrivatePeer, Count: 2, Rounds: 1})
		if e != nil {
			t.Fatal(e)
		}
		if r.Complete {
			return
		}
	}
	t.Fatal("backfill never completed")
}

func TestCanceledContext(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := f.SetMode(ctx, engine.ModeReadOnly); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e := f.Resolve(ctx, engine.ResolveRequest{Query: "Ana"}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e := f.SendText(ctx, engine.SendTextRequest{}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestConcurrentJournalAndSubscriptions(t *testing.T) {
	f := newFake(t, engine.Config{Account: "test", Mode: engine.ModeReadWrite})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := f.Subscribe(ctx, engine.SubscribeOptions{Kinds: []engine.EventKind{engine.EventMessage}, Buffer: 1})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			text := string(rune('a' + i))
			r := engine.SendTextRequest{Peer: fake.PrivatePeer, Text: text, WriteOptions: engine.WriteOptions{DryRun: true}}
			p, e := f.SendText(ctx, r)
			if e != nil {
				t.Error(e)
				return
			}
			r.DryRun = false
			r.ExpectFingerprint = p.Preview.Fingerprint
			if _, e = f.SendText(ctx, r); e != nil {
				t.Error(e)
			}
			_ = f.Status()
			_ = f.Writes()
		}(i)
	}
	wg.Wait()
	if len(f.Writes()) != 20 {
		t.Fatalf("journal size %d", len(f.Writes()))
	}
	if f.DroppedEvents() != 19 {
		t.Fatalf("drops %d", f.DroppedEvents())
	}
	e := <-events
	if e.Kind != engine.EventMessage || len(e.ID) != 32 {
		t.Fatalf("event %+v", e)
	}
	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("subscription still open")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription not closed")
	}
}
