package gotdengine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

// Expanded from pr17-review-engine_test.go: fail real SQLite only after RPC acceptance.
func TestAcceptedWritesRetainEvidenceOnSQLiteFailure(t *testing.T) {
	for _, action := range []string{"delete", "delete_channel", "edit", "react", "archive", "unarchive", "forward", "text_short", "text_updates", "text_combined", "media_short", "media_updates", "location_short", "location_updates", "poll_short", "poll_updates"} {
		t.Run(action, func(t *testing.T) {
			e, s := writeFixture(t)
			ctx := context.Background()
			ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
			if action == "delete_channel" {
				ref = engine.PeerRef{Type: engine.PeerChannel, ID: 30}
			}
			key := engine.MessageKey{Account: "primary", Peer: ref, ID: 71}
			if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Text: "old", Out: true, Date: time.Unix(1000, 0)}}}); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(t.TempDir(), "upload.txt")
			if err := os.WriteFile(file, []byte("synthetic media"), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			var localErr error
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				if _, ok := in.(*tg.UploadSaveFilePartRequest); ok {
					out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
					return nil
				}
				calls++
				if calls != 1 {
					t.Fatalf("unexpected extra RPC %T", in)
				}
				var id int64
				switch r := in.(type) {
				case *tg.MessagesSendMessageRequest:
					id = r.RandomID
				case *tg.MessagesSendMediaRequest:
					id = r.RandomID
				case *tg.MessagesForwardMessagesRequest:
					id = r.RandomID[0]
				}
				switch v := out.(type) {
				case *tg.MessagesAffectedMessages:
					v.Pts = 1
					v.PtsCount = 1
				case *tg.UpdatesBox:
					if strings.HasSuffix(action, "_short") {
						v.Updates = &tg.UpdateShortSentMessage{ID: 90, Date: 1000, Pts: 1, PtsCount: 1}
					} else {
						// A concrete peer update forces SQLite persistence in the updates manager.
						u := &tg.Updates{Users: []tg.UserClass{&tg.User{ID: 10, FirstName: "Accepted update"}}, Updates: []tg.UpdateClass{&tg.UpdateMessageID{ID: 90, RandomID: id}}}
						v.Updates = u
						if action == "text_combined" {
							v.Updates = &tg.UpdatesCombined{Users: u.Users, Updates: u.Updates}
						}
					}
				default:
					t.Fatalf("unexpected reply %T", out)
				}
				localErr = groupsCloseStore(t, s)
				return nil
			}))
			run := func(opts engine.WriteOptions) (engine.WriteResult, error) {
				switch action {
				case "delete", "delete_channel":
					return e.DeleteConfirmed(ctx, engine.DeleteRequest{Peer: ref, IDs: []int{71}, Revoke: action == "delete_channel", WriteOptions: opts}, action == "delete_channel")
				case "edit":
					return e.Edit(ctx, engine.EditRequest{Key: key, Text: "new", WriteOptions: opts})
				case "react":
					return e.React(ctx, engine.ReactRequest{Key: key, Emoji: "👍", WriteOptions: opts})
				case "archive", "unarchive":
					return e.Organise(ctx, engine.OrganiseRequest{Peer: ref, Action: engine.OrganiseAction(action), WriteOptions: opts})
				case "forward":
					return e.Forward(ctx, engine.ForwardRequest{From: ref, To: engine.PeerRef{Type: engine.PeerChat, ID: 20}, IDs: []int{71}, WriteOptions: opts})
				case "text_short", "text_updates", "text_combined":
					return e.SendText(ctx, engine.SendTextRequest{Peer: ref, Text: "hello", WriteOptions: opts})
				case "media_short", "media_updates":
					return e.SendMedia(ctx, engine.SendMediaRequest{Peer: ref, Path: file, Kind: engine.MediaDocument, WriteOptions: opts})
				case "location_short", "location_updates":
					return e.SendLocation(ctx, engine.SendLocationRequest{Peer: ref, Location: engine.Location{Latitude: 1, Longitude: 2}, WriteOptions: opts})
				case "poll_short", "poll_updates":
					return e.SendPoll(ctx, engine.SendPollRequest{Peer: ref, Question: "Pick?", Options: []string{"one", "two"}, WriteOptions: opts})
				default:
					t.Fatal(action)
					return engine.WriteResult{}, nil
				}
			}
			p, err := run(engine.WriteOptions{DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatal("preview performed RPC")
			}
			got, err := run(engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint})
			if err == nil || !errors.Is(err, localErr) || !got.Done || calls != 1 {
				t.Errorf("lost acceptance: result=%+v err=%v underlying=%v calls=%d", got, err, localErr, calls)
			}
			if got.Affected != 1 {
				t.Errorf("lost affected: %+v", got)
			}
			if strings.Contains(action, "short") || strings.Contains(action, "updates") || action == "text_combined" || action == "forward" {
				wantPeer := ref
				if action == "forward" {
					wantPeer = engine.PeerRef{Type: engine.PeerChat, ID: 20}
				}
				if len(got.Messages) != 1 || got.Messages[0] != (engine.MessageKey{Account: "primary", Peer: wantPeer, ID: 90}) {
					t.Errorf("lost accepted ID: %+v", got.Messages)
				}
			}
			if !strings.Contains(got.Note, "do not blindly retry") || err == nil || !strings.Contains(err.Error(), "do not blindly retry") {
				t.Errorf("missing warning: result=%+v err=%v", got, err)
			}
		})
	}
}

// The read receipt can succeed while a canceled caller prevents its PTS enqueue.
// Hold the real manager before its worker starts and fill its bounded queue so
// cancellation is the only selectable branch (not a timing-dependent race).
func TestAcceptedReadReceiptRetainsEvidenceOnReconciliationCancellation(t *testing.T) {
	e, _ := writeFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.UpdatesGetStateRequest:
			*out.(*tg.UpdatesState) = tg.UpdatesState{Pts: 1, Date: 1000}
		case *tg.UpdatesGetDifferenceRequest:
			out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1000}
		default:
			t.Errorf("unexpected setup RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	}))
	done := make(chan error, 1)
	go func() {
		done <- e.manager.Run(ctx, e.api, 99, updates.AuthOptions{OnStart: func(context.Context) { close(started); <-release }})
	}()
	defer func() { cancel(); close(release); <-done; e.manager.Reset() }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for i := 0; i < 10; i++ {
		if err := e.manager.HandleAffected(ctx, 0, 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	callctx, cancelCall := context.WithCancel(ctx)
	defer cancelCall()
	accepted := false
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesReadHistoryRequest); !ok {
			t.Fatalf("unexpected receipt RPC %T", in)
		}
		accepted = true
		*out.(*tg.MessagesAffectedMessages) = tg.MessagesAffectedMessages{Pts: 2, PtsCount: 1}
		cancelCall()
		return nil
	}))
	r := engine.MarkReadRequest{Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, MaxID: 71, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.MarkRead(callctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	got, err := e.MarkRead(callctx, r)
	if !accepted || !got.Done || !errors.Is(err, context.Canceled) {
		t.Fatalf("lost accepted receipt: %+v err=%v accepted=%v", got, err, accepted)
	}
	if !strings.Contains(got.Note, "do not blindly retry") || err == nil || !strings.Contains(err.Error(), "do not blindly retry") {
		t.Fatalf("missing retry warning: %+v err=%v", got, err)
	}
}

// Based on the review's reaction-only drift reproducer; preview approval remains independent.
func TestForwardStableIdempotencyAcrossPreviewChanges(t *testing.T) {
	e, s := writeFixture(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 71}
	m := engine.Message{Key: key, Text: "source", Date: time.Unix(1000, 0)}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{m}}); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		r := in.(*tg.MessagesForwardMessagesRequest)
		ids = append(ids, r.RandomID[0])
		out.(*tg.UpdatesBox).Updates = &tg.Updates{}
		return nil
	}))
	req := engine.ForwardRequest{From: key.Peer, To: engine.PeerRef{Type: engine.PeerChat, ID: 20}, IDs: []int{71}, WriteOptions: engine.WriteOptions{IdempotencyKey: "same-operation"}}
	req.DryRun = true
	p, err := e.Forward(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.DryRun = false
	req.ExpectFingerprint = p.Preview.Fingerprint
	if _, err = e.Forward(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Forward(ctx, req); !errors.Is(err, engine.ErrPreviewMismatch) {
		t.Fatalf("reused preview allowed: %v", err)
	}
	if _, err = s.Apply(ctx, engine.Batch{Account: "primary", Reactions: []engine.ReactionUpdate{{Key: key, Counts: []engine.ReactionCount{{Emoji: "👍", Count: 1}}}}}); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Forward(ctx, req); !errors.Is(err, engine.ErrPreviewMismatch) {
		t.Fatalf("stale approval allowed: %v", err)
	}
	req.DryRun = true
	p2, err := e.Forward(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Preview.Fingerprint == p.Preview.Fingerprint {
		t.Fatal("preview did not bind changed snapshot")
	}
	req.DryRun = false
	req.ExpectFingerprint = p2.Preview.Fingerprint
	if _, err = e.Forward(ctx, req); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("same operation changed random IDs: %v", ids)
	}
	// A distinct forwarding intent must not alias the server dedupe domain.
	req.To = engine.PeerRef{Type: engine.PeerChat, ID: 20}
	req.Silent = true
	req.DryRun = true
	p3, err := e.Forward(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.DryRun = false
	req.ExpectFingerprint = p3.Preview.Fingerprint
	if _, err = e.Forward(ctx, req); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || ids[2] == ids[0] {
		t.Fatalf("different forwarding intent shared random ID: %v", ids)
	}
}
