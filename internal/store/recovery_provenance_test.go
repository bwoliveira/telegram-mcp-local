package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestRecoveryMigrationV3KeepsLegacyBaselineUnknownAndRollsBack(t *testing.T) {
	ctx := context.Background()
	for _, version := range []int{1, 2} {
		t.Run(map[int]string{1: "v1", 2: "v2"}[version], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "store.db")
			raw, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = raw.Exec(schemaV1); err != nil {
				t.Fatal(err)
			}
			if _, err = raw.Exec(`INSERT INTO accounts(account,updated_at) VALUES('primary',1000); INSERT INTO gaps(account,reason,detected_at) VALUES('primary','state_lost',900); INSERT INTO updates_state(account,pts,qts,date,seq,updated_at) VALUES('primary',100,0,1000,1,1000)`); err != nil {
				t.Fatal(err)
			}
			if version == 2 {
				if _, err = raw.Exec(`PRAGMA user_version=2`); err != nil {
					t.Fatal(err)
				}
			}
			raw.Close()
			injected := errors.New("synthetic migration interruption")
			s, err := Open(ctx, path, "primary", FaultAt(func(stage string) error {
				if stage == "migration_v3_before_commit" {
					return injected
				}
				return nil
			}))
			if s != nil {
				s.Close()
				t.Fatal("faulted migration opened store")
			}
			if !errors.Is(err, injected) {
				t.Fatal(err)
			}
			raw, err = sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			var v, n int
			if err = raw.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil || v != 2 {
				t.Fatalf("v3 rollback version=%d err=%v", v, err)
			}
			if err = raw.QueryRow(`SELECT count(*) FROM pragma_table_info('gaps') WHERE name IN ('recovery_anchor','recovery_cursor')`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("partial migration columns=%d err=%v", n, err)
			}
			raw.Close()
			for restart := 0; restart < 2; restart++ {
				s, err = Open(ctx, path, "primary")
				if err != nil {
					t.Fatal(err)
				}
				gaps, err := s.PendingGaps(ctx, "primary", nil)
				if err != nil || len(gaps) != 1 || gaps[0].RecoveryAnchor != nil || gaps[0].RecoveryCursor != nil {
					t.Fatalf("legacy checkpoint fabricated provenance: %+v %v", gaps, err)
				}
				if _, err = s.SaveDifferenceCheckpoint(ctx, "primary", engine.DifferenceCheckpoint{From: engine.UpdatesState{Pts: 100, Date: 1000}, State: engine.UpdatesState{Pts: 100, Date: 1001, Seq: 1}, Gaps: gaps, Final: true}); err != nil {
					t.Fatal(err)
				}
				if n := confCount(t, s, `SELECT count(*) FROM gaps WHERE filled_at IS NOT NULL`); n != 0 {
					t.Fatal("adopted head closed legacy loss")
				}
				s.Close()
			}
		})
	}
}

func recoveryFixture(t *testing.T, s *Store, channel bool) (engine.UpdatesState, []engine.GapMark) {
	t.Helper()
	ctx := context.Background()
	base := engine.UpdatesState{Pts: 1, Qts: 2, Date: 1000, Seq: 3}
	var peer *engine.PeerRef
	if channel {
		p := engine.PeerRef{Type: engine.PeerChannel, ID: 42}
		peer = &p
		base = engine.UpdatesState{Pts: 1}
		if err := s.SaveChannelPts(ctx, confAccount, 42, 1); err != nil {
			t.Fatal(err)
		}
	} else if err := s.SaveUpdatesState(ctx, confAccount, base); err != nil {
		t.Fatal(err)
	}
	confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{{Peer: peer, Reason: engine.GapStateLost, Detected: time.Unix(900, 0), RecoveryAnchor: &base}}})
	gaps, err := s.PendingGaps(ctx, confAccount, peer)
	if err != nil || len(gaps) != 1 || gaps[0].RecoveryAnchor == nil {
		t.Fatalf("fixture lacks genuine anchor: %+v %v", gaps, err)
	}
	return base, gaps
}

func TestRecoveryCheckpointProgressAndFillAreAtomic(t *testing.T) {
	for _, channel := range []bool{false, true} {
		for _, final := range []bool{false, true} {
			t.Run(map[bool]string{false: "global", true: "channel"}[channel]+map[bool]string{false: "-slice", true: "-final"}[final], func(t *testing.T) {
				s := confOpen(t)
				base, gaps := recoveryFixture(t, s, channel)
				next := base
				next.Pts = 2
				next.Date++
				var ch int64
				if channel {
					ch = 42
					next.Date = 0
				}
				injected := errors.New("synthetic checkpoint transaction failure")
				s.fault = func(stage string) error {
					if stage == "difference_checkpoint_before_commit" {
						return injected
					}
					return nil
				}
				p := engine.DifferenceCheckpoint{From: base, State: next, Channel: ch, Gaps: gaps, Final: final}
				ap, err := s.SaveDifferenceCheckpoint(context.Background(), confAccount, p)
				if !errors.Is(err, injected) || len(ap.Gaps) != 0 {
					t.Fatalf("failed checkpoint=%+v %v", ap, err)
				}
				var actual engine.UpdatesState
				if channel {
					actual.Pts, _, err = s.LoadChannelPts(context.Background(), confAccount, ch)
				} else {
					actual, _, err = s.LoadUpdatesState(context.Background(), confAccount)
				}
				if err != nil || actual != base {
					t.Fatalf("failed proof advanced checkpoint: %+v %v", actual, err)
				}
				pending, err := s.PendingGaps(context.Background(), confAccount, gaps[0].Peer)
				if err != nil || len(pending) != 1 || *pending[0].RecoveryCursor != base {
					t.Fatalf("failed checkpoint advanced proof: %+v %v", pending, err)
				}
				s.fault = nil
				ap, err = s.SaveDifferenceCheckpoint(context.Background(), confAccount, p)
				if err != nil {
					t.Fatal(err)
				}
				pending, err = s.PendingGaps(context.Background(), confAccount, gaps[0].Peer)
				if err != nil {
					t.Fatal(err)
				}
				if final {
					if len(pending) != 0 || len(ap.Gaps) != 1 {
						t.Fatalf("final proof not committed atomically: %+v %+v", pending, ap)
					}
				} else if len(pending) != 1 || *pending[0].RecoveryCursor != next || *pending[0].RecoveryAnchor != base {
					t.Fatalf("slice lost original baseline: %+v", pending)
				}
			})
		}
	}
}

func TestRecoveryRejectsSkippedOrMismatchedRequestBaseline(t *testing.T) {
	for _, field := range []string{"pts", "qts", "date", "anchor", "cursor", "too-long"} {
		t.Run(field, func(t *testing.T) {
			s := confOpen(t)
			base, gaps := recoveryFixture(t, s, false)
			from := base
			switch field {
			case "pts":
				from.Pts++
			case "qts":
				from.Qts++
			case "date":
				from.Date++
			case "anchor":
				fake := base
				fake.Pts++
				gaps[0].RecoveryAnchor = &fake
			case "cursor":
				fake := base
				fake.Pts++
				gaps[0].RecoveryCursor = &fake
			case "too-long":
				confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{{Reason: engine.GapDifferenceTooLong, Detected: time.Unix(950, 0)}}})
			}
			next := from
			next.Date++
			if _, err := s.SaveDifferenceCheckpoint(context.Background(), confAccount, engine.DifferenceCheckpoint{From: from, State: next, Gaps: gaps, Final: true}); err != nil {
				t.Fatal(err)
			}
			if n := confCount(t, s, `SELECT count(*) FROM gaps WHERE reason='state_lost' AND filled_at IS NULL`); n != 1 {
				t.Fatal("unreplayed interval falsely filled")
			}
		})
	}
}

func TestRecoveryCannotUpgradeUnknownOrValidateInventedAnchor(t *testing.T) {
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent-checkpoint", true: "mismatched-checkpoint"}[known], func(t *testing.T) {
			s := confOpen(t)
			if known {
				if err := s.SaveUpdatesState(context.Background(), confAccount, engine.UpdatesState{Pts: 100, Date: 1000}); err != nil {
					t.Fatal(err)
				}
			}
			anchor := engine.UpdatesState{Pts: 1, Date: 900}
			confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{{Reason: engine.GapStateLost, RecoveryAnchor: &anchor}}})
			gaps, err := s.PendingGaps(context.Background(), confAccount, nil)
			if err != nil || len(gaps) != 1 || gaps[0].RecoveryAnchor != nil {
				t.Fatalf("unvalidated baseline accepted: %+v %v", gaps, err)
			}
			if err := s.SaveUpdatesState(context.Background(), confAccount, anchor); err != nil {
				t.Fatal(err)
			}
			confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{{Reason: engine.GapStateLost, RecoveryAnchor: &anchor}}})
			gaps, err = s.PendingGaps(context.Background(), confAccount, nil)
			if err != nil || len(gaps) != 1 || gaps[0].RecoveryAnchor != nil {
				t.Fatal("duplicate callback retroactively invented missing evidence")
			}
		})
	}
}
