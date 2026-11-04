package gotdengine

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func r6SeedGlobal(t *testing.T, s *store.Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.SaveUpdatesState(ctx, barrierAccount, engine.UpdatesState{Pts: 1, Date: 1000}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, engine.Batch{Account: barrierAccount, Gaps: []engine.GapMark{{Reason: engine.GapStateLost, Detected: time.Unix(900, 0), RecoveryAnchor: &engine.UpdatesState{Pts: 1, Date: 1000}}}}); err != nil {
		t.Fatal(err)
	}
}

func TestR6GlobalRecoveryRequiresFinalDifferenceAndDurableCheckpoint(t *testing.T) {
	for _, scenario := range []string{"full", "empty", "slice-final", "rpc-error", "slice-error", "too-long", "apply-error", "checkpoint-error"} {
		t.Run(scenario, func(t *testing.T) {
			var armed atomic.Bool
			s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"), store.FaultAt(func(stage string) error {
				if scenario == "apply-error" && armed.Load() && stage == "apply_before_commit" {
					return errors.New("synthetic disk failure")
				}
				return nil
			}))
			r6SeedGlobal(t, s)
			e := newBarrierEngine(t, s)
			if scenario == "checkpoint-error" {
				var err error
				e, err = New(engine.Config{Account: barrierAccount, APIID: 1, APIHash: "synthetic-test-only", Store: r6FailCheckpoint{Persister: s}, Session: &memorySession{}})
				if err != nil {
					t.Fatal(err)
				}
				e.account = engine.Account{ID: barrierAccount, SelfID: 99, Authenticated: true}
			}
			var calls atomic.Int32
			responded := make(chan struct{})
			e.api = tg.NewClient(productionRPC(e, invokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
				r, ok := in.(*tg.UpdatesGetDifferenceRequest)
				if !ok {
					t.Errorf("forbidden RPC %T", in)
					return engine.ErrNotSupported
				}
				n := calls.Add(1)
				if n == 1 && r.Pts != 1 {
					t.Errorf("late durable checkpoint ignored: pts=%d", r.Pts)
				}
				defer func() {
					select {
					case <-responded:
					default:
						close(responded)
					}
				}()
				if scenario == "rpc-error" || (scenario == "slice-error" && n > 1) {
					return errors.New("synthetic offline RPC failure")
				}
				var d tg.UpdatesDifferenceClass
				switch {
				case scenario == "too-long" && n == 1:
					d = &tg.UpdatesDifferenceTooLong{Pts: 2}
				case (scenario == "slice-final" || scenario == "slice-error") && n == 1:
					d = &tg.UpdatesDifferenceSlice{IntermediateState: tg.UpdatesState{Pts: 2, Date: 1001}}
				case scenario == "empty" || scenario == "too-long":
					d = &tg.UpdatesDifferenceEmpty{Date: 1002, Seq: 3}
				default:
					d = &tg.UpdatesDifference{State: tg.UpdatesState{Pts: 3, Date: 1002, Seq: 3}, NewMessages: []tg.MessageClass{&tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1002, Message: "offline recovery marker"}}}
				}
				out.(*tg.UpdatesDifferenceBox).Difference = d
				armed.Store(true)
				return nil
			})))
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			e.active = ctx
			done := make(chan error, 1)
			go func() { done <- e.manager.Run(ctx, e.api, 99, updates.AuthOptions{}) }()
			<-responded
			wantClosed := scenario == "full" || scenario == "empty" || scenario == "slice-final"
			if wantClosed {
				deadline := time.Now().Add(1500 * time.Millisecond)
				for time.Now().Before(deadline) {
					c, _ := s.StatsCoverage(context.Background(), barrierAccount, nil)
					if len(c.Gaps) == 0 {
						break
					}
					time.Sleep(time.Millisecond)
				}
			} else {
				if scenario == "slice-error" || scenario == "too-long" {
					waitBarrier(t, func() bool { return calls.Load() >= 2 })
				}
				time.Sleep(25 * time.Millisecond)
			}
			cancel()
			<-done
			e.manager.Reset()
			c, err := s.StatsCoverage(context.Background(), barrierAccount, nil)
			if err != nil {
				t.Fatal(err)
			}
			hasStateGap := false
			for _, g := range c.Gaps {
				hasStateGap = hasStateGap || g.Reason == engine.GapStateLost
			}
			if hasStateGap == wantClosed {
				t.Fatalf("state_lost open=%v wantClosed=%v calls=%d gaps=%+v", hasStateGap, wantClosed, calls.Load(), c.Gaps)
			}
			if wantClosed {
				st, ok, err := s.LoadUpdatesState(context.Background(), barrierAccount)
				if err != nil || !ok || st.Date != 1002 {
					t.Fatalf("closed without final checkpoint: %+v %v %v", st, ok, err)
				}
			}
		})
	}
}

type r6FailCheckpoint struct{ engine.Persister }

func (p r6FailCheckpoint) PendingGaps(ctx context.Context, account engine.AccountID, peer *engine.PeerRef) ([]engine.GapMark, error) {
	return p.Persister.(pendingGapReader).PendingGaps(ctx, account, peer)
}

func (p r6FailCheckpoint) SaveUpdatesState(context.Context, engine.AccountID, engine.UpdatesState) error {
	return errors.New("synthetic checkpoint failure")
}

func TestR6GlobalStateLostCallbackDoesNotScanHistory(t *testing.T) {
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	r6SeedGlobal(t, s)
	e := newBarrierEngine(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.active = ctx
	var calls atomic.Int32
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { calls.Add(1); return engine.ErrNotSupported }))
	e.gap(nil, engine.GapStateLost)
	time.Sleep(40 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("state_lost launched unrelated dialog/history RPCs: %d", calls.Load())
	}
}

func TestR6MissingChannelHashRecoveredByExactPublicIdentity(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-channel", true: "reassigned-username"}[mismatch], func(t *testing.T) {
			s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
			e := newBarrierEngine(t, s)
			if _, err := s.Apply(context.Background(), engine.Batch{Account: barrierAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: barrierPeer, Username: "synthetic_public_channel"}}}}); err != nil {
				t.Fatal(err)
			}
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				if _, ok := in.(*tg.ContactsResolveUsernameRequest); !ok {
					t.Errorf("forbidden RPC %T", in)
					return engine.ErrNotSupported
				}
				id := int64(42)
				if mismatch {
					id = 43
				}
				out.(*tg.ContactsResolvedPeer).Peer = &tg.PeerChannel{ChannelID: id}
				out.(*tg.ContactsResolvedPeer).Chats = []tg.ChatClass{&tg.Channel{ID: id, AccessHash: 123, Username: "synthetic_public_channel"}}
				return nil
			}))
			h, ok, err := (stateAdapter{p: s, acc: barrierAccount, e: e}).GetChannelAccessHash(context.Background(), 99, 42)
			if err != nil || ok == mismatch || (!mismatch && h != 123) {
				t.Fatalf("hash=%d found=%v err=%v mismatch=%v", h, ok, err, mismatch)
			}
		})
	}
}

func TestR6ChannelDifferenceRepairsStaleHashOnce(t *testing.T) {
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	e := newBarrierEngine(t, s)
	if _, err := s.Apply(context.Background(), engine.Batch{Account: barrierAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: barrierPeer, Username: "synthetic_public_channel"}, AccessHash: 111}}}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.api = tg.NewClient(productionRPC(e, invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.ContactsResolveUsernameRequest:
			out.(*tg.ContactsResolvedPeer).Peer = &tg.PeerChannel{ChannelID: 42}
			out.(*tg.ContactsResolvedPeer).Chats = []tg.ChatClass{&tg.Channel{ID: 42, AccessHash: 222}}
		case *tg.UpdatesGetChannelDifferenceRequest:
			calls++
			h := r.Channel.(*tg.InputChannel).AccessHash
			if h == 111 {
				return tgerr.New(400, "CHANNEL_PRIVATE")
			}
			if h != 222 {
				t.Errorf("unexpected hash %d", h)
			}
			out.(*tg.UpdatesChannelDifferenceBox).ChannelDifference = &tg.UpdatesChannelDifferenceEmpty{Final: true, Pts: 2}
		default:
			t.Errorf("forbidden RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})))
	_, err := e.api.UpdatesGetChannelDifference(context.Background(), &tg.UpdatesGetChannelDifferenceRequest{Channel: &tg.InputChannel{ChannelID: 42, AccessHash: 111}, Filter: &tg.ChannelMessagesFilterEmpty{}, Pts: 1, Limit: 10})
	if err != nil || calls != 2 {
		t.Fatalf("stale hash not repaired: calls=%d err=%v", calls, err)
	}
}

func TestR6ManagerLoadsCheckpointWrittenAfterEngineCreation(t *testing.T) {
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	e := newBarrierEngine(t, s)
	// An imported/late-bound session may populate the account after New returns.
	if err := s.SaveUpdatesState(context.Background(), barrierAccount, engine.UpdatesState{Pts: 77, Qts: 4, Date: 1234, Seq: 8}); err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{})
	e.api = tg.NewClient(productionRPC(e, invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		r, ok := in.(*tg.UpdatesGetDifferenceRequest)
		if !ok {
			t.Errorf("late state overwritten via %T", in)
			return engine.ErrNotSupported
		}
		if r.Pts != 77 || r.Qts != 4 || r.Date != 1234 {
			t.Errorf("stale startup checkpoint: %+v", r)
		}
		out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1235, Seq: 9}
		close(called)
		return nil
	})))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	done := make(chan error, 1)
	go func() { done <- e.manager.Run(ctx, e.api, 99, updates.AuthOptions{}) }()
	select {
	case <-called:
	case <-ctx.Done():
		t.Error(ctx.Err())
	}
	cancel()
	<-done
}

func TestR6LegacyAccessRowsKeepReadyAndCoverageWarning(t *testing.T) {
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	e := newBarrierEngine(t, s)
	ctx := context.Background()
	if _, err := s.Apply(ctx, engine.Batch{Account: barrierAccount, Gaps: []engine.GapMark{{Peer: &barrierPeer, Reason: engine.GapAccessLost, Detected: time.Unix(900, 0)}}}); err != nil {
		t.Fatal(err)
	}
	e.status.State = engine.StateConnected
	server := mcp.New(mcp.Config{Engine: e, ReadStore: s})
	raw := server.Handle(ctx, []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"telegram_status","arguments":{}}}`))
	var reply struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || len(reply.Result.Content) != 1 {
		t.Fatalf("invalid status reply: %s %v", raw, err)
	}
	var body struct {
		Ready bool `json:"ready"`
	}
	if err := json.Unmarshal([]byte(reply.Result.Content[0].Text), &body); err != nil || !body.Ready {
		t.Fatalf("legacy access rows blocked readiness: %s %v", reply.Result.Content[0].Text, err)
	}
	coverage, err := s.StatsCoverage(ctx, barrierAccount, &barrierPeer)
	if err != nil || coverage.Complete || len(coverage.Gaps) != 1 || coverage.Gaps[0].Reason != engine.GapAccessLost {
		t.Fatalf("lost inaccessible warning: %+v %v", coverage, err)
	}
}

func TestR6AccessCallbackDeduplicatesAcrossTimestamps(t *testing.T) {
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	e := newBarrierEngine(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.active = ctx
	now := time.Unix(1000, 0)
	e.cfg.Clock = func() time.Time { return now }
	for i := 0; i < 8; i++ {
		e.gap(&barrierPeer, engine.GapAccessLost)
		now = now.Add(time.Minute)
	}
	gaps, err := s.PendingGaps(ctx, barrierAccount, &barrierPeer)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("callback duplicated evidence: %+v %v", gaps, err)
	}
	_, status, err := s.ReadStatus(ctx, barrierAccount)
	if err != nil || status.OpenGaps != 0 || status.InaccessibleGaps != 1 {
		t.Fatalf("inaccessible callback became recoverable: %+v %v", status, err)
	}
}

func TestR6HeadScanCannotProveUnboundedUpdateGap(t *testing.T) {
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	e := newBarrierEngine(t, s)
	ctx := context.Background()
	g := engine.GapMark{Peer: &barrierPeer, Reason: engine.GapChannelTooLong, Detected: time.Unix(900, 0)}
	if _, err := s.Apply(ctx, engine.Batch{Account: barrierAccount, Gaps: []engine.GapMark{g}, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: barrierPeer}, AccessHash: 123}}, Messages: []engine.Message{{Key: engine.MessageKey{Account: barrierAccount, Peer: barrierPeer, ID: 7}, Date: time.Unix(800, 0)}}}); err != nil {
		t.Fatal(err)
	}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		if _, ok := in.(*tg.MessagesGetHistoryRequest); !ok {
			t.Errorf("forbidden RPC %T", in)
			return engine.ErrNotSupported
		}
		out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 42}, Date: 800}}}
		return nil
	}))
	e.recoverGap(ctx, barrierPeer, g)
	gaps, err := s.PendingGaps(ctx, barrierAccount, &barrierPeer)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("head message ID falsely proved missed edits/deletes: %+v %v", gaps, err)
	}
}

func TestR6StartupHeldUntilFinalDifferenceCheckpoint(t *testing.T) {
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	r6SeedGlobal(t, s)
	e := newBarrierEngine(t, s)
	e.resetGeneration()
	if _, err := e.apply(context.Background(), engine.Batch{Account: barrierAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: barrierPeer}, AccessHash: 123}}}); err != nil {
		t.Fatal(err)
	}
	if !e.Status().Updates.Held {
		t.Fatal("metadata batch prematurely announced ready")
	}
	e.api = tg.NewClient(e.durableDifference(invokeFunc(func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
		out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1001, Seq: 2}
		return nil
	})))
	if _, err := e.api.UpdatesGetDifference(context.Background(), &tg.UpdatesGetDifferenceRequest{Pts: 1, Date: 1000}); err != nil {
		t.Fatal(err)
	}
	if !e.Status().Updates.Held {
		t.Fatal("RPC response without durable checkpoint announced ready")
	}
	if err := (stateAdapter{p: s, acc: barrierAccount, e: e}).SetDateSeq(context.Background(), 99, 1001, 2); err != nil {
		t.Fatal(err)
	}
	if e.Status().Updates.Held {
		t.Fatal("final durable difference did not release startup hold")
	}
}
