package gotdengine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestRecoveryChannelSlicesKeepOriginalAnchorAcrossRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.db")
	s := barrierStore(t, path)
	if err := s.SaveChannelPts(ctx, barrierAccount, barrierPeer.ID, 1); err != nil {
		t.Fatal(err)
	}
	e := newBarrierEngine(t, s)
	e.active = ctx
	e.gap(&barrierPeer, engine.GapStateLost)
	var box tg.UpdatesChannelDifferenceBox
	request := func(pts int) *tg.UpdatesGetChannelDifferenceRequest {
		return &tg.UpdatesGetChannelDifferenceRequest{Channel: &tg.InputChannel{ChannelID: barrierPeer.ID, AccessHash: 123}, Pts: pts, Limit: 100}
	}
	slice := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		box.ChannelDifference = &tg.UpdatesChannelDifference{Pts: 2}
		return nil
	})
	if err := e.durableDifference(slice)(ctx, request(1), &box); err != nil {
		t.Fatal(err)
	}
	if err := (stateAdapter{p: s, acc: barrierAccount, e: e}).saveChannel(ctx, barrierAccount, barrierPeer.ID, 2); err != nil {
		t.Fatal(err)
	}
	gaps, err := s.PendingGaps(ctx, barrierAccount, &barrierPeer)
	if err != nil || len(gaps) != 1 || gaps[0].RecoveryAnchor == nil || gaps[0].RecoveryAnchor.Pts != 1 || gaps[0].RecoveryCursor.Pts != 2 {
		t.Fatalf("channel slice provenance=%+v %v", gaps, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = barrierStore(t, path)
	e = newBarrierEngine(t, s)
	final := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		box.ChannelDifference = &tg.UpdatesChannelDifferenceEmpty{Pts: 2, Final: true}
		return nil
	})
	if err := e.durableDifference(final)(ctx, request(2), &box); err != nil {
		t.Fatal(err)
	}
	if err := (stateAdapter{p: s, acc: barrierAccount, e: e}).saveChannel(ctx, barrierAccount, barrierPeer.ID, 2); err != nil {
		t.Fatal(err)
	}
	gaps, err = s.PendingGaps(ctx, barrierAccount, &barrierPeer)
	if err != nil || len(gaps) != 0 {
		t.Fatalf("persisted channel chain failed to close: %+v %v", gaps, err)
	}
}

func TestRecoveryMissingBaselineDoesNotSuppressDifference(t *testing.T) {
	ctx := context.Background()
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	e := newBarrierEngine(t, s)
	e.active = ctx
	e.gap(nil, engine.GapStateLost)
	// gotd may adopt getState's current head and still fetch a difference. That
	// request is useful for future updates, not evidence for the unknown past.
	if err := s.SaveUpdatesState(ctx, barrierAccount, engine.UpdatesState{Pts: 100, Date: 1000}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var box tg.UpdatesDifferenceBox
	rpc := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		calls++
		box.Difference = &tg.UpdatesDifferenceEmpty{Date: 1001}
		return nil
	})
	if err := e.durableDifference(rpc)(ctx, &tg.UpdatesGetDifferenceRequest{Pts: 100, Date: 1000}, &box); err != nil {
		t.Fatal(err)
	}
	if err := (stateAdapter{p: s, acc: barrierAccount, e: e}).saveState(ctx, barrierAccount, engine.UpdatesState{Pts: 100, Date: 1001}); err != nil {
		t.Fatal(err)
	}
	gaps, err := s.PendingGaps(ctx, barrierAccount, nil)
	if calls != 1 || err != nil || len(gaps) != 1 || gaps[0].RecoveryAnchor != nil {
		t.Fatalf("missing baseline calls=%d gaps=%+v err=%v", calls, gaps, err)
	}
}

type recoveryNonAtomicStore struct{ engine.Persister }

func (s recoveryNonAtomicStore) PendingGaps(ctx context.Context, a engine.AccountID, p *engine.PeerRef) ([]engine.GapMark, error) {
	return s.Persister.(pendingGapReader).PendingGaps(ctx, a, p)
}

func TestRecoveryNonAtomicPersisterAllowsReadinessButCannotFill(t *testing.T) {
	ctx := context.Background()
	s := barrierStore(t, filepath.Join(t.TempDir(), "store.db"))
	r6SeedGlobal(t, s)
	wrapped := recoveryNonAtomicStore{Persister: s}
	e := newBarrierEngine(t, s)
	e.cfg.Store = wrapped
	e.recovering.Store(true)
	var box tg.UpdatesDifferenceBox
	rpc := invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		box.Difference = &tg.UpdatesDifferenceEmpty{Date: 1001}
		return nil
	})
	if err := e.durableDifference(rpc)(ctx, &tg.UpdatesGetDifferenceRequest{Pts: 1, Date: 1000}, &box); err != nil {
		t.Fatal(err)
	}
	if err := (stateAdapter{p: wrapped, acc: barrierAccount, e: e}).saveState(ctx, barrierAccount, engine.UpdatesState{Pts: 1, Date: 1001}); err != nil {
		t.Fatal(err)
	}
	gaps, err := s.PendingGaps(ctx, barrierAccount, nil)
	if err != nil || len(gaps) != 1 || e.recovering.Load() {
		t.Fatalf("non-atomic fallback gaps=%+v recovering=%v err=%v", gaps, e.recovering.Load(), err)
	}
}
