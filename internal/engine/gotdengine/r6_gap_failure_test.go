package gotdengine

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestR6MissingBaselineRetainsEvidenceAcrossRestart(t *testing.T) {
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	for generation := 0; generation < 2; generation++ {
		e := newBarrierEngine(t, s)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		e.active = ctx
		e.resetGeneration()
		e.api = tg.NewClient(e.durableDifference(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
			switch in.(type) {
			case *tg.UpdatesGetStateRequest:
				*out.(*tg.UpdatesState) = tg.UpdatesState{Pts: 100, Date: 1000, Seq: 10}
			case *tg.UpdatesGetDifferenceRequest:
				out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1001 + generation, Seq: 10}
			default:
				t.Errorf("forbidden RPC %T", in)
				return engine.ErrNotSupported
			}
			return nil
		})))
		done := make(chan error, 1)
		go func() { done <- e.manager.Run(ctx, e.api, 99, updates.AuthOptions{}) }()
		waitBarrier(t, func() bool {
			st, ok, _ := s.LoadUpdatesState(context.Background(), barrierAccount)
			return ok && st.Date == 1001+generation
		})
		cancel()
		<-done
		gaps, err := s.PendingGaps(context.Background(), barrierAccount, nil)
		if err != nil || len(gaps) != 2 {
			t.Fatalf("unknown baseline falsely recovered: generation=%d gaps=%+v err=%v", generation, gaps, err)
		}
		status := e.Status().Updates
		if status.OpenGaps != 0 || status.CoverageGaps != 2 || status.TerminalGaps != 2 || status.GapCauses[engine.GapRecoveryUnknownBaseline] != 1 || status.GapCauses[engine.GapRecoveryDifferenceTooLong] != 1 {
			t.Fatalf("terminal lost stream must warn without claiming ongoing replay: %+v", status)
		}
	}
}

func TestR6FirstAccountBindEstablishesBaselineWithoutFalseGap(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.db"), barrierAccount)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e, err := New(engine.Config{Account: barrierAccount, APIID: 1, APIHash: "synthetic-test-only", Store: s, Session: &memorySession{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.bind(ctx, &tg.User{ID: 99, Self: true}); err != nil {
		t.Fatal(err)
	}
	run, cancel := context.WithTimeout(ctx, time.Second)
	e.active = run
	e.resetGeneration()
	e.api = tg.NewClient(e.durableDifference(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.UpdatesGetStateRequest:
			*out.(*tg.UpdatesState) = tg.UpdatesState{Pts: 100, Date: 1000, Seq: 10}
		case *tg.UpdatesGetDifferenceRequest:
			out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1001, Seq: 10}
		default:
			t.Errorf("forbidden RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})))
	done := make(chan error, 1)
	go func() { done <- e.manager.Run(run, e.api, 99, updates.AuthOptions{}) }()
	waitBarrier(t, func() bool { return !e.Status().Updates.Held })
	cancel()
	<-done
	gaps, err := s.PendingGaps(ctx, barrierAccount, nil)
	if err != nil || len(gaps) != 0 {
		t.Fatalf("first baseline invented loss: %+v %v", gaps, err)
	}
}

func TestR6TransientHashRepairPreservesChannelCheckpoint(t *testing.T) {
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	e := newBarrierEngine(t, s)
	if _, err := s.Apply(context.Background(), engine.Batch{Account: barrierAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: barrierPeer, Username: "synthetic_public_channel"}, AccessHash: 111}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveChannelPts(context.Background(), barrierAccount, 42, 1); err != nil {
		t.Fatal(err)
	}
	transient := errors.New("synthetic transient resolution failure")
	e.api = tg.NewClient(e.durableDifference(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.UpdatesGetChannelDifferenceRequest:
			return tgerr.New(400, "CHANNEL_PRIVATE")
		case *tg.ContactsResolveUsernameRequest:
			return transient
		}
		return engine.ErrNotSupported
	})))
	_, err := e.api.UpdatesGetChannelDifference(context.Background(), &tg.UpdatesGetChannelDifferenceRequest{Channel: &tg.InputChannel{ChannelID: 42, AccessHash: 111}, Filter: &tg.ChannelMessagesFilterEmpty{}, Pts: 1, Limit: 10})
	if !errors.Is(err, transient) || tgerr.Is(err, "CHANNEL_PRIVATE") {
		t.Fatalf("transient failure became access loss: %v", err)
	}
	pts, ok, err := s.LoadChannelPts(context.Background(), barrierAccount, 42)
	if err != nil || !ok || pts != 1 {
		t.Fatalf("channel checkpoint discarded: %d %v %v", pts, ok, err)
	}
}

func TestR6TooLongEvidencePrecedesCheckpointReset(t *testing.T) {
	for _, channel := range []bool{false, true} {
		t.Run(map[bool]string{false: "global", true: "channel"}[channel], func(t *testing.T) {
			var armed atomic.Bool
			s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"), store.FaultAt(func(stage string) error {
				if armed.Load() && stage == "apply_before_commit" {
					return errors.New("synthetic disk failure")
				}
				return nil
			}))
			r6SeedGlobal(t, s)
			if err := s.SaveChannelPts(context.Background(), barrierAccount, 42, 1); err != nil {
				t.Fatal(err)
			}
			e := newBarrierEngine(t, s)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			e.active = ctx
			e.api = tg.NewClient(e.durableDifference(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.UpdatesGetDifferenceRequest:
					armed.Store(true)
					out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceTooLong{Pts: 100}
				case *tg.UpdatesGetChannelDifferenceRequest:
					armed.Store(true)
					out.(*tg.UpdatesChannelDifferenceBox).ChannelDifference = &tg.UpdatesChannelDifferenceTooLong{Dialog: &tg.Dialog{Pts: 100}}
				default:
					return engine.ErrNotSupported
				}
				return nil
			})))
			var err error
			if channel {
				_, err = e.api.UpdatesGetChannelDifference(ctx, &tg.UpdatesGetChannelDifferenceRequest{Channel: &tg.InputChannel{ChannelID: 42, AccessHash: 42}, Filter: &tg.ChannelMessagesFilterEmpty{}, Pts: 1, Limit: 10})
			} else {
				_, err = e.api.UpdatesGetDifference(ctx, &tg.UpdatesGetDifferenceRequest{Pts: 1, Date: 1000})
			}
			if err == nil || !e.held.Load() {
				t.Fatalf("reset escaped failed evidence write: %v held=%v", err, e.held.Load())
			}
			adapter := stateAdapter{p: s, acc: barrierAccount, e: e}
			if channel {
				if err := adapter.SetChannelPts(ctx, 99, 42, 100); !errors.Is(err, engine.ErrCheckpointHeld) {
					t.Fatal(err)
				}
			} else {
				if err := adapter.SetPts(ctx, 99, 100); !errors.Is(err, engine.ErrCheckpointHeld) {
					t.Fatal(err)
				}
			}
			st, _, _ := s.LoadUpdatesState(context.Background(), barrierAccount)
			pts, _, _ := s.LoadChannelPts(context.Background(), barrierAccount, 42)
			if st.Pts != 1 || pts != 1 {
				t.Fatalf("reset advanced ahead of durable warning: %+v channel=%d", st, pts)
			}
		})
	}
}

func TestR6TooLongChannelSnapshotIngestedBeforeCheckpoint(t *testing.T) {
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	seedBarrier(t, s)
	e := newBarrierEngine(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e.active = ctx
	e.api = tg.NewClient(e.durableDifference(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.UpdatesGetDifferenceRequest:
			out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1001}
		case *tg.UpdatesGetChannelDifferenceRequest:
			dialog := &tg.Dialog{}
			dialog.SetPts(100)
			out.(*tg.UpdatesChannelDifferenceBox).ChannelDifference = &tg.UpdatesChannelDifferenceTooLong{Final: true, Dialog: dialog, Messages: []tg.MessageClass{&tg.Message{ID: 9, PeerID: &tg.PeerChannel{ChannelID: 42}, Date: 1001, Message: "offline truncated snapshot marker"}}}
		default:
			t.Errorf("forbidden recovery RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	})))
	done := make(chan error, 1)
	go func() { done <- e.manager.Run(ctx, e.api, 99, updates.AuthOptions{}) }()
	waitBarrier(t, func() bool {
		pts, ok, _ := s.LoadChannelPts(context.Background(), barrierAccount, 42)
		return ok && pts == 100
	})
	cancel()
	<-done
	_, found, err := s.Message(context.Background(), engine.MessageKey{Account: barrierAccount, Peer: barrierPeer, ID: 9})
	if err != nil || !found {
		t.Fatalf("TooLong advanced PTS without ingesting its available snapshot: found=%v err=%v", found, err)
	}
	gaps, err := s.PendingGaps(context.Background(), barrierAccount, &barrierPeer)
	if err != nil || len(gaps) != 1 || gaps[0].Reason != engine.GapChannelTooLong {
		t.Fatalf("snapshot falsely resolved dropped-stream warning: %+v %v", gaps, err)
	}
}

func TestR6GapFillFailureKeepsEvidenceAndRecoveryHold(t *testing.T) {
	var armed atomic.Bool
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"), store.FaultAt(func(stage string) error {
		if armed.Load() && stage == "apply_before_commit" {
			return errors.New("synthetic fill failure")
		}
		return nil
	}))
	r6SeedGlobal(t, s)
	e := newBarrierEngine(t, s)
	e.resetGeneration()
	e.api = tg.NewClient(e.durableDifference(invokeFunc(func(_ context.Context, _ bin.Encoder, out bin.Decoder) error {
		out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1001, Seq: 2}
		return nil
	})))
	if _, err := e.api.UpdatesGetDifference(context.Background(), &tg.UpdatesGetDifferenceRequest{Pts: 1, Date: 1000}); err != nil {
		t.Fatal(err)
	}
	armed.Store(true)
	err := (stateAdapter{p: s, acc: barrierAccount, e: e}).SetDateSeq(context.Background(), 99, 1001, 2)
	if err == nil || !e.Status().Updates.Held {
		t.Fatalf("fill failure lost hold: %v %+v", err, e.Status().Updates)
	}
	gaps, err := s.PendingGaps(context.Background(), barrierAccount, nil)
	if err != nil || len(gaps) != 1 {
		t.Fatalf("failed fill hid evidence: %+v %v", gaps, err)
	}
}
