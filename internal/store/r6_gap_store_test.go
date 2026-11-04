package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func r6AssertGapStatus(t *testing.T, s *Store, open, inaccessible int) {
	t.Helper()
	_, status, err := s.ReadStatus(context.Background(), confAccount)
	if err != nil {
		t.Fatal(err)
	}
	// Check the wire contract too, without making the RED depend on a new field.
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	var gotInaccessible int
	if raw := fields["inaccessible_gaps"]; raw != nil {
		if err := json.Unmarshal(raw, &gotInaccessible); err != nil {
			t.Fatal(err)
		}
	}
	if status.OpenGaps != open || gotInaccessible != inaccessible {
		t.Errorf("gap status: open=%d inaccessible=%d; want %d %d", status.OpenGaps, gotInaccessible, open, inaccessible)
	}
}

func TestR6GapStatusSeparatesLegacyInaccessibleEvidence(t *testing.T) {
	s := confOpen(t)
	querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at,filled_at) VALUES
	 (?, 'channel', 1, 'access_lost', 100, NULL),
	 (?, 'channel', 4, 'access_lost', 101, NULL),
	 (?, NULL, NULL, 'state_lost', 102, NULL),
	 (?, 'channel', 2, 'channel_too_long', 103, 104),
	 (?, 'channel', 3, 'access_lost', 103, 104)`, confAccount, confAccount, confAccount, confAccount, confAccount)
	r6AssertGapStatus(t, s, 0, 2)
	_, status, err := s.ReadStatus(context.Background(), confAccount)
	if err != nil || status.CoverageGaps != 3 || status.TerminalGaps != 3 || status.GapCauses[engine.GapRecoveryUnknownBaseline] != 1 {
		t.Fatalf("missing terminal coverage evidence: %+v %v", status, err)
	}
	if n := confCount(t, s, `SELECT count(*) FROM gaps WHERE reason='access_lost' AND filled_at IS NULL`); n != 2 {
		t.Fatalf("status mutated inaccessible evidence: %d", n)
	}
}

func TestR6GapCallbacksDeduplicateUntilRecovery(t *testing.T) {
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 6}
	for _, reason := range []engine.GapReason{engine.GapStateLost, engine.GapDifferenceTooLong, engine.GapChannelTooLong, engine.GapAccessLost} {
		for _, scope := range []*engine.PeerRef{nil, &peer} {
			t.Run(string(reason)+"/"+func() string {
				if scope == nil {
					return "account"
				}
				return "peer"
			}(), func(t *testing.T) {
				s := confOpen(t)
				g := engine.GapMark{Peer: scope, Reason: reason, Detected: confTime}
				if ap := confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{g}}); len(ap.Gaps) != 1 {
					t.Fatalf("first callback: %+v", ap.Gaps)
				}
				g.Detected = confTime.Add(time.Minute)
				if ap := confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{g}}); len(ap.Gaps) != 0 {
					t.Errorf("repeat callback emitted new gap: %+v", ap.Gaps)
				}
				if n := confCount(t, s, `SELECT count(*) FROM gaps`); n != 1 {
					t.Errorf("duplicate unresolved rows: %d", n)
				}
				// Explicit FilledGaps cannot prove an unknown update baseline.
				if reason == engine.GapStateLost {
					if ap := confApply(t, s, engine.Batch{Account: confAccount, FilledGaps: []engine.GapMark{g}}); len(ap.Gaps) != 0 {
						t.Fatal("unknown baseline filled without an anchored difference")
					}
					return
				}
				// Other explicit gap fills retain their existing bounded contract.
				if ap := confApply(t, s, engine.Batch{Account: confAccount, FilledGaps: []engine.GapMark{g}}); len(ap.Gaps) != 1 || ap.Gaps[0].Filled == nil {
					t.Fatalf("scope recovery did not fill earlier detection: %+v", ap.Gaps)
				}
				if ap := confApply(t, s, engine.Batch{Account: confAccount, FilledGaps: []engine.GapMark{g}}); len(ap.Gaps) != 0 {
					t.Errorf("repeat recovery emitted gap: %+v", ap.Gaps)
				}
				g.Detected = confTime.Add(2 * time.Minute)
				if ap := confApply(t, s, engine.Batch{Account: confAccount, Gaps: []engine.GapMark{g}}); len(ap.Gaps) != 1 {
					t.Fatalf("new episode suppressed: %+v", ap.Gaps)
				}
			})
		}
	}
}

func TestR6AccessCallbacksPreserveSummaryWithoutInventedFill(t *testing.T) {
	s := confOpen(t)
	now := confTime
	s.now = func() time.Time { return now }
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 6}
	loss := func() engine.Batch {
		return engine.Batch{Account: confAccount,
			Access: []engine.AccessChange{{Peer: peer, State: engine.AccessLost, Reason: "CHANNEL_PRIVATE"}},
			Gaps:   []engine.GapMark{{Peer: &peer, Reason: engine.GapAccessLost, Detected: now}}}
	}
	confApply(t, s, loss())
	now = now.Add(time.Minute)
	if ap := confApply(t, s, loss()); len(ap.Gaps) != 0 || len(ap.Access) != 0 {
		t.Errorf("same access episode repeated: access=%d gaps=%d", len(ap.Access), len(ap.Gaps))
	}
	r6AssertGapStatus(t, s, 0, 1)
	confApply(t, s, engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: peer, State: engine.AccessOK}}})
	if n := confCount(t, s, `SELECT count(*) FROM gaps WHERE filled_at IS NOT NULL`); n != 0 {
		t.Fatalf("access return invented history proof: %d", n)
	}
	// A second loss updates the same terminal summary even at the same time.
	if ap := confApply(t, s, loss()); len(ap.Gaps) != 0 || len(ap.Access) != 1 {
		t.Errorf("second access episode: access=%d gaps=%d", len(ap.Access), len(ap.Gaps))
	}
	if ap := confApply(t, s, loss()); len(ap.Gaps) != 0 || len(ap.Access) != 0 {
		t.Errorf("second episode replay: access=%d gaps=%d", len(ap.Access), len(ap.Gaps))
	}
	confApply(t, s, engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: peer, State: engine.AccessKicked}}})
	r6AssertGapStatus(t, s, 0, 1)
	if n := confCount(t, s, `SELECT count(*) FROM gaps WHERE filled_at IS NOT NULL`); n != 0 {
		t.Errorf("inaccessible rows marked filled: %d", n)
	}
}

func TestR6AccessEvidenceStillWarnsAfterMetadataAccessOK(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(func() string {
			if legacy {
				return "legacy"
			}
			return "new"
		}(), func(t *testing.T) {
			s := confOpen(t)
			peer := engine.PeerRef{Type: engine.PeerChannel, ID: 6}
			if legacy {
				querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) VALUES(?,'channel',6,'access_lost',100)`, confAccount)
			} else {
				confApply(t, s, engine.Batch{Account: confAccount, Access: []engine.AccessChange{{Peer: peer, State: engine.AccessLost}}})
			}
			confApply(t, s, engine.Batch{Account: confAccount,
				Peers:  []engine.PeerUpsert{{Peer: engine.Peer{Ref: peer, Kind: engine.KindChannel, Access: engine.AccessOK}}},
				Cursor: &engine.Cursor{Peer: peer, State: "idle", Complete: true}})
			for _, scope := range []*engine.PeerRef{nil, &peer} {
				complete, _, err := s.ReadCoverage(context.Background(), confAccount, scope)
				if err != nil || complete {
					t.Errorf("access metadata erased warning: complete=%v err=%v", complete, err)
				}
				coverage, err := s.StatsCoverage(context.Background(), confAccount, scope)
				if err != nil || coverage.Complete || len(coverage.Gaps) != 1 || coverage.Gaps[0].Reason != engine.GapAccessLost {
					t.Errorf("missing explicit coverage warning: %+v %v", coverage, err)
				}
			}
			r6AssertGapStatus(t, s, 0, 1)
		})
	}
}

func TestR6LegacyStateLostCannotBeFilledByScopeTimestamp(t *testing.T) {
	s := confOpen(t)
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 6}
	querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) VALUES
	 (?, 'channel', 6, 'state_lost', 100), (?, 'channel', 6, 'state_lost', 101),
	 (?, 'channel', 6, 'state_lost', 103), (?, 'chat', 6, 'state_lost', 100),
	 (?, 'channel', 7, 'state_lost', 100), (?, NULL, NULL, 'state_lost', 100),
	 (?, 'channel', 6, 'access_lost', 100), (?, 'channel', 6, 'channel_too_long', 100)`,
		confAccount, confAccount, confAccount, confAccount, confAccount, confAccount, confAccount, confAccount)
	filled := time.Unix(104, 0).UTC()
	g := engine.GapMark{Peer: &peer, Reason: engine.GapStateLost, Detected: time.Unix(102, 0).UTC(), Filled: &filled}
	if ap := confApply(t, s, engine.Batch{Account: confAccount, FilledGaps: []engine.GapMark{g}}); len(ap.Gaps) != 0 {
		t.Errorf("legacy state loss emitted false recovery: %d", len(ap.Gaps))
	}
	if n := confCount(t, s, `SELECT count(*) FROM gaps WHERE filled_at=104`); n != 0 {
		t.Errorf("unknown baselines filled=%d; want 0", n)
	}
	if n := confCount(t, s, `SELECT count(*) FROM gaps WHERE filled_at IS NULL`); n != 8 {
		t.Errorf("missing evidence changed: unresolved=%d; want 8", n)
	}
}

func TestR6BoundedRecoveryDoesNotFillOutsideProof(t *testing.T) {
	s := confOpen(t)
	peer := engine.PeerRef{Type: engine.PeerChannel, ID: 6}
	querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,from_id,to_id,detected_at) VALUES
	 (?, 'channel', 6, 'channel_too_long', 10, 20, 100),
	 (?, 'channel', 6, 'channel_too_long', 1, 30, 100),
	 (?, 'channel', 6, 'channel_too_long', NULL, NULL, 100)`, confAccount, confAccount, confAccount)
	g := engine.GapMark{Peer: &peer, Reason: engine.GapChannelTooLong, FromID: 10, ToID: 20, Detected: time.Unix(101, 0)}
	confApply(t, s, engine.Batch{Account: confAccount, FilledGaps: []engine.GapMark{g}})
	if n := confCount(t, s, `SELECT count(*) FROM gaps WHERE filled_at IS NOT NULL`); n != 1 {
		t.Errorf("bounded proof filled %d rows; want only contained range", n)
	}
}
