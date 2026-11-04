package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"strings"
	"testing"
	"time"
)

type failMutation struct {
	engine.Persister
	fail bool
}

func (p *failMutation) Apply(ctx context.Context, b engine.Batch) (engine.Applied, error) {
	if p.fail && (len(b.Edits) > 0 || len(b.Deletes) > 0) {
		return engine.Applied{}, errors.New("synthetic mutation failure")
	}
	return p.Persister.Apply(ctx, b)
}

// Exercise the real manager's split OtherUpdates/NewMessages dispatch. Its
// in-memory PTS can advance after a handler error; durable PTS must not follow.
func TestManagerBarrierGuardsEditsAndDeletesAcrossGenerations(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		for _, mutation := range []string{"edit", "delete"} {
			t.Run(fmt.Sprintf("recovery=%v/%s", recovery, mutation), func(t *testing.T) {
				e, s := offlineEngine(t)
				ctx := context.Background()
				peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
				var rawPeer tg.PeerClass = &tg.PeerUser{UserID: 99}
				key := engine.MessageKey{Account: "primary", Peer: peer, ID: 7}
				if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Date: time.Unix(1000, 0), Text: "original"}}}); err != nil {
					t.Fatal(err)
				}
				if err := s.SaveUpdatesState(ctx, "primary", engine.UpdatesState{Pts: 1, Date: 1000}); err != nil {
					t.Fatal(err)
				}
				p := &failMutation{Persister: s, fail: true}
				e.cfg.Store = p
				if recovery {
					e.resetGeneration()
				}
				var update tg.UpdateClass
				message := &tg.Message{ID: 7, PeerID: rawPeer, Date: 1000, EditDate: 1001, Message: "edited"}
				switch mutation {
				case "edit":
					update = &tg.UpdateEditMessage{Pts: 2, PtsCount: 1, Message: message}
				case "delete":
					update = &tg.UpdateDeleteMessages{Pts: 2, PtsCount: 1, Messages: []int{7}}
				}
				run := func() {
					t.Helper()
					runctx, cancel := context.WithTimeout(ctx, 3*time.Second)
					defer cancel()
					continued := make(chan struct{}, 1)
					calls := 0
					e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
						r, ok := in.(*tg.UpdatesGetDifferenceRequest)
						if !ok {
							t.Errorf("forbidden RPC %T", in)
							return errors.New("forbidden RPC")
						}
						calls++
						if calls == 1 {
							if r.Pts != 1 {
								t.Errorf("replay did not start at durable checkpoint: %d", r.Pts)
							}
							out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceSlice{OtherUpdates: []tg.UpdateClass{update}, NewMessages: []tg.MessageClass{&tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 99}, Date: 1001, Message: "next"}}, IntermediateState: tg.UpdatesState{Pts: 3, Date: 1001}}
						} else {
							out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1001}
							select {
							case continued <- struct{}{}:
							default:
							}
						}
						return nil
					}))
					done := make(chan error, 1)
					go func() { done <- e.manager.Run(runctx, e.api, 99, updates.AuthOptions{}) }()
					select {
					case <-continued:
					case <-runctx.Done():
						t.Error(runctx.Err())
					}
					cancel()
					<-done
					e.manager.Reset()
				}
				run()
				state, _, err := s.LoadUpdatesState(ctx, "primary")
				if err != nil || state.Pts != 1 || !e.held.Load() {
					t.Fatalf("unsafe checkpoint: %+v held=%v err=%v", state, e.held.Load(), err)
				}
				if _, ok, _ := s.Message(ctx, engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, ID: 8}); ok {
					t.Fatal("later successful batch escaped hold")
				}
				sa := stateAdapter{p: p, acc: "primary", e: e}
				if err := sa.SetChannelPts(ctx, 99, 42, 50); !errors.Is(err, engine.ErrCheckpointHeld) {
					t.Fatalf("channel checkpoint escaped global barrier: %v", err)
				}
				// Join/reset above is mandatory before releasing this generation.
				p.fail = false
				e.resetGeneration()
				run()
				state, _, err = s.LoadUpdatesState(ctx, "primary")
				if err != nil || state.Pts != 3 {
					t.Fatalf("replay did not commit: %+v held=%v err=%v", state, e.held.Load(), err)
				}
				m, ok, err := s.Message(ctx, key)
				if err != nil || (strings.HasSuffix(mutation, "edit") && (!ok || m.Text != "edited")) || (strings.HasSuffix(mutation, "delete") && ok) {
					t.Fatalf("mutation not durably replayed: %+v ok=%v err=%v", m, ok, err)
				}
				if _, ok, _ := s.Message(ctx, engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, ID: 8}); !ok {
					t.Fatal("later batch absent after replay")
				}
			})
		}
	}
}

type failEdit struct{ engine.Persister }

func (p failEdit) Apply(ctx context.Context, b engine.Batch) (engine.Applied, error) {
	if len(b.Edits) > 0 {
		return engine.Applied{}, errors.New("synthetic edit failure")
	}
	return p.Persister.Apply(ctx, b)
}
func TestRecoveryGenerationFailureNeverAdvancesOverMissingEdit(t *testing.T) {
	e, s := offlineEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.SaveUpdatesState(ctx, "primary", engine.UpdatesState{Pts: 1, Date: 1000}); err != nil {
		t.Fatal(err)
	}
	e.cfg.Store = failEdit{s}
	e.resetGeneration()
	n := 0
	continued := make(chan struct{}, 1)
	e.api = tg.NewClient(invokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.UpdatesGetDifferenceRequest:
			n++
			if n == 1 {
				out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceSlice{OtherUpdates: []tg.UpdateClass{&tg.UpdateEditMessage{Pts: 2, PtsCount: 1, Message: &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, EditDate: 1001, Message: "edited"}}}, NewMessages: []tg.MessageClass{&tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 99}, Date: 1001, Message: "next"}}, IntermediateState: tg.UpdatesState{Pts: 3, Date: 1001}}
			} else {
				out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1001}
				select {
				case continued <- struct{}{}:
				default:
				}
			}
			return nil
		default:
			t.Errorf("unexpected RPC %T", in)
			return errors.New("unexpected RPC")
		}
	}))
	done := make(chan error, 1)
	go func() { done <- e.manager.Run(ctx, e.api, 99, updates.AuthOptions{}) }()
	select {
	case <-continued:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	<-done
	st, _, err := s.LoadUpdatesState(context.Background(), "primary")
	if err != nil {
		t.Fatal(err)
	}
	if st.Pts != 1 || !e.held.Load() {
		t.Fatalf("unsafe checkpoint: pts=%d held=%v", st.Pts, e.held.Load())
	}
	if _, ok, _ := s.Message(context.Background(), engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, ID: 8}); ok {
		t.Fatal("later batch escaped failed generation")
	}
}
func TestDifferenceMessagesCarryCatchUp(t *testing.T) {
	for _, channel := range []bool{false, true} {
		e, _ := offlineEngine(t)
		ctx, cancel := context.WithCancel(context.Background())
		ch := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 1})
		m := &tg.Message{ID: 9, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "backlog"}
		var u tg.UpdateClass = &tg.UpdateNewMessage{Pts: -1, PtsCount: -1, Message: m}
		if channel {
			m.PeerID = &tg.PeerChannel{ChannelID: 42}
			u = &tg.UpdateNewChannelMessage{Pts: -1, PtsCount: -1, Message: m}
		}
		if err := e.handle(ctx, &tg.Updates{Updates: []tg.UpdateClass{u}}); err != nil {
			t.Fatal(err)
		}
		ev := <-ch
		if !ev.CatchUp || ev.Source != engine.SourceDifference {
			t.Fatal(ev)
		}
		cancel()
	}
}
func TestCancelledLoginGenerationRejectsLateResults(t *testing.T) {
	e, _ := offlineEngine(t)
	e.active = context.Background()
	e.account.Authenticated = false
	if err := e.ResumePhone(context.Background(), engine.Secret(`{"Phone":"synthetic","Hash":"synthetic","State":"code"}`)); err != nil {
		t.Fatal(err)
	}
	ctx, done, gen, err := e.operation(context.Background(), engine.LoginWantCode)
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	e.CancelLogin()
	if ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("operation not cancelled")
		}
	}
	if err := e.sentCode(context.Background(), gen, &tg.AuthSentCode{PhoneCodeHash: "late"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if e.Login().State != engine.LoginCancelled {
		t.Fatal(e.Login().State)
	}
}
func TestDialogPaginationMatchesCompositeMessageKey(t *testing.T) {
	e, _ := offlineEngine(t)
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		r := in.(*tg.MessagesGetDialogsRequest)
		calls++
		if calls == 1 {
			ds := make([]tg.DialogClass, 100)
			for i := range ds {
				ds[i] = &tg.Dialog{Peer: &tg.PeerChat{ChatID: int64(i + 1)}, TopMessage: 7}
			}
			out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogsSlice{Dialogs: ds, Messages: []tg.MessageClass{&tg.MessageService{ID: 7, PeerID: &tg.PeerChat{ChatID: 100}, Date: 1200}, &tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 444}, Date: 9999}}}
		} else {
			if r.OffsetDate != 1200 {
				t.Fatalf("cross-peer offset date: %d", r.OffsetDate)
			}
			out.(*tg.MessagesDialogsBox).Dialogs = &tg.MessagesDialogs{}
		}
		return nil
	}))
	if _, err := e.RefreshDialogs(context.Background(), engine.RefreshRequest{Limit: 101}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
