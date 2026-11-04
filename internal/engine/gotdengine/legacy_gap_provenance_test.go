package gotdengine

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewLegacyStateLostCannotProveDiscardedInterval(t *testing.T) {
	ctx := context.Background()
	s := barrierStore(t, filepath.Join(t.TempDir(), "legacy.db"))
	// Origin/main's OnLoadUserStateFailed recorded state_lost, then gotd
	// adopted getState's current head. The old interval has no checkpoint.
	if _, err := s.Apply(ctx, engine.Batch{Account: barrierAccount, Gaps: []engine.GapMark{{Reason: engine.GapStateLost, Detected: time.Unix(900, 0)}}}); err != nil {
		t.Fatal(err)
	}
	head := engine.UpdatesState{Pts: 100, Date: 1000, Seq: 1}
	if err := s.SaveUpdatesState(ctx, barrierAccount, head); err != nil {
		t.Fatal(err)
	}
	e := newBarrierEngine(t, s)
	e.recovering.Store(true)
	next := invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1001, Seq: 1}
		return nil
	})
	var result tg.UpdatesDifferenceBox
	if err := e.durableDifference(next)(ctx, &tg.UpdatesGetDifferenceRequest{Pts: 100, Date: 1000}, &result); err != nil {
		t.Fatal(err)
	}
	a := stateAdapter{p: s, acc: barrierAccount, e: e}
	if err := a.saveState(ctx, barrierAccount, engine.UpdatesState{Pts: 100, Date: 1001, Seq: 1}); err != nil {
		t.Fatal(err)
	}
	gaps, err := s.PendingGaps(ctx, barrierAccount, nil)
	if err != nil {
		t.Fatal(err)
	}
	status := e.Status().Updates
	if status.OpenGaps != 0 || status.TerminalGaps != 1 || status.CoverageGaps != 1 || status.GapCauses[engine.GapRecoveryUnknownBaseline] != 1 {
		t.Fatalf("adopted head hid terminal coverage or claimed ongoing recovery: %+v", status)
	}
	if len(gaps) != 1 || gaps[0].Recovery != engine.GapTerminal || gaps[0].RecoveryCause != engine.GapRecoveryUnknownBaseline {
		t.Fatal("legacy lost interval marked filled by empty diff from already-adopted head=100; no interval before PTS 100 was replayed")
	}
}
