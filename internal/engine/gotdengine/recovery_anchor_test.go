package gotdengine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestRecoveryCallbackCapturesOnlyExistingOriginalBaseline(t *testing.T) {
	for _, channel := range []bool{false, true} {
		for _, present := range []bool{false, true} {
			t.Run(map[bool]string{false: "global", true: "channel"}[channel]+map[bool]string{false: "-missing", true: "-present"}[present], func(t *testing.T) {
				ctx := context.Background()
				s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
				e := newBarrierEngine(t, s)
				e.active = ctx
				baseline := engine.UpdatesState{Pts: 1, Date: 1000}
				var peer *engine.PeerRef
				if channel {
					peer = &barrierPeer
					baseline.Date = 0
				}
				if present {
					var err error
					if channel {
						err = s.SaveChannelPts(ctx, barrierAccount, barrierPeer.ID, 1)
					} else {
						err = s.SaveUpdatesState(ctx, barrierAccount, baseline)
					}
					if err != nil {
						t.Fatal(err)
					}
				}
				e.gap(peer, engine.GapStateLost)
				gaps, err := s.PendingGaps(ctx, barrierAccount, peer)
				if err != nil || len(gaps) != 1 {
					t.Fatalf("callback gaps=%+v err=%v", gaps, err)
				}
				if present {
					if gaps[0].RecoveryAnchor == nil || *gaps[0].RecoveryAnchor != baseline {
						t.Fatal("callback discarded a real durable baseline")
					}
				} else if gaps[0].RecoveryAnchor != nil {
					t.Fatal("callback invented a missing baseline")
				}
			})
		}
	}
}

func TestRecoverySlicesPersistAnchorAcrossRestartAndRPCError(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	s := barrierStore(t, path)
	e := newBarrierEngine(t, s)
	e.active = ctx
	baseline := engine.UpdatesState{Pts: 1, Date: 1000}
	if err := s.SaveUpdatesState(ctx, barrierAccount, baseline); err != nil {
		t.Fatal(err)
	}
	e.gap(nil, engine.GapStateLost)
	var box tg.UpdatesDifferenceBox
	slice := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		box.Difference = &tg.UpdatesDifferenceSlice{IntermediateState: tg.UpdatesState{Pts: 2, Date: 1001}}
		return nil
	})
	if err := e.durableDifference(slice)(ctx, &tg.UpdatesGetDifferenceRequest{Pts: 1, Date: 1000}, &box); err != nil {
		t.Fatal(err)
	}
	if err := (stateAdapter{p: s, acc: barrierAccount, e: e}).saveState(ctx, barrierAccount, engine.UpdatesState{Pts: 2, Date: 1001}); err != nil {
		t.Fatal(err)
	}
	gaps, err := s.PendingGaps(ctx, barrierAccount, nil)
	if err != nil || len(gaps) != 1 || gaps[0].RecoveryCursor == nil || gaps[0].RecoveryCursor.Pts != 2 {
		t.Fatalf("slice lost durable provenance: %+v %v", gaps, err)
	}
	offline := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { return engine.ErrNotSupported })
	if err := e.durableDifference(offline)(ctx, &tg.UpdatesGetDifferenceRequest{Pts: 2, Date: 1001}, &box); err == nil {
		t.Fatal("expected offline failure")
	}
	// Close the SQLite store, not just the Engine, to prove on-disk provenance.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = barrierStore(t, path)
	e = newBarrierEngine(t, s)
	final := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		box.Difference = &tg.UpdatesDifferenceEmpty{Date: 1002, Seq: 1}
		return nil
	})
	if err := e.durableDifference(final)(ctx, &tg.UpdatesGetDifferenceRequest{Pts: 2, Date: 1001}, &box); err != nil {
		t.Fatal(err)
	}
	if err := (stateAdapter{p: s, acc: barrierAccount, e: e}).saveState(ctx, barrierAccount, engine.UpdatesState{Pts: 2, Date: 1002, Seq: 1}); err != nil {
		t.Fatal(err)
	}
	gaps, err = s.PendingGaps(ctx, barrierAccount, nil)
	if err != nil || len(gaps) != 0 {
		t.Fatalf("validated original-baseline replay failed to close: %+v %v", gaps, err)
	}
}
