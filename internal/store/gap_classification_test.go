package store

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func TestGapRecoveryClassificationSurfacesAndScopes(t *testing.T) {
	for _, tc := range []struct {
		name, reason, scope, blocker, cause, recovery string
		anchor, cursor                                bool
	}{
		{"legacy", "state_lost", "global", "", "unknown_baseline", "terminal", false, false},
		{"partial_provenance", "state_lost", "global", "", "unknown_baseline", "terminal", true, false},
		{"replay", "state_lost", "global", "", "replay_pending", "recoverable", true, true},
		{"channel_replay", "state_lost", "channel", "", "replay_pending", "recoverable", true, true},
		{"account_truncation", "difference_too_long", "global", "", "difference_too_long", "terminal", false, false},
		{"channel_truncation", "channel_too_long", "channel", "", "channel_too_long", "terminal", false, false},
		{"inaccessible", "access_lost", "channel", "", "access_lost", "terminal", false, false},
		{"blocked_chain", "state_lost", "global", "global", "stream_truncated", "terminal", true, true},
		{"blocked_channel", "state_lost", "channel", "channel", "stream_truncated", "terminal", true, true},
		{"other_channel", "state_lost", "channel", "other_channel", "replay_pending", "recoverable", true, true},
		{"other_scope", "state_lost", "global", "channel", "replay_pending", "recoverable", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := confOpen(t)
			var peer *engine.PeerRef
			var pt, pi any
			if tc.scope == "channel" {
				peer = &engine.PeerRef{Type: engine.PeerChannel, ID: 6}
				pt, pi = peer.Type, peer.ID
			}
			var anchor, cursor any
			if tc.anchor {
				anchor = encode(engine.UpdatesState{Pts: 1, Date: 100})
			}
			if tc.cursor {
				cursor = anchor
			}
			querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at,recovery_anchor,recovery_cursor) VALUES(?,?,?,?,100,?,?)`, confAccount, pt, pi, tc.reason, anchor, cursor)
			if tc.blocker != "" {
				var bt, bi any
				reason := "difference_too_long"
				if tc.blocker != "global" {
					bt, bi, reason = engine.PeerChannel, 6, "channel_too_long"
				}
				if tc.blocker == "other_channel" {
					bi = 7
				}
				querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at) VALUES(?,?,?,?,101)`, confAccount, bt, bi, reason)
			}
			// Other-account and filled truncation evidence cannot change readiness.
			querySQL(t, s, `INSERT INTO accounts(account,updated_at) VALUES('other',100)`)
			querySQL(t, s, `INSERT INTO gaps(account,reason,detected_at) VALUES('other','difference_too_long',100)`)
			querySQL(t, s, `INSERT INTO gaps(account,peer_type,peer_id,reason,detected_at,filled_at) VALUES(?,?,?,'difference_too_long',100,101)`, confAccount, pt, pi)
			_, status, err := s.ReadStatus(context.Background(), confAccount)
			if err != nil {
				t.Fatal(err)
			}
			wantOpen := 0
			if tc.recovery == "recoverable" {
				wantOpen = 1
			}
			total := 1
			if tc.blocker != "" {
				total++
			}
			wire, err := json.Marshal(status)
			if err != nil {
				t.Fatal(err)
			}
			var counts struct {
				CoverageGaps int            `json:"coverage_gaps"`
				TerminalGaps int            `json:"terminal_gaps"`
				GapCauses    map[string]int `json:"gap_causes"`
			}
			if err := json.Unmarshal(wire, &counts); err != nil {
				t.Fatal(err)
			}
			if status.OpenGaps != wantOpen || counts.CoverageGaps != total || counts.TerminalGaps != total-wantOpen || counts.GapCauses[tc.cause] == 0 {
				t.Errorf("classification status=%s want open=%d total=%d cause=%s", wire, wantOpen, total, tc.cause)
			}
			pending, err := s.PendingGaps(context.Background(), confAccount, peer)
			if err != nil || len(pending) == 0 {
				t.Fatalf("pending=%+v err=%v", pending, err)
			}
			coverage, err := s.StatsCoverage(context.Background(), confAccount, peer)
			if err != nil || coverage.Complete || len(coverage.Gaps) == 0 {
				t.Fatalf("coverage=%+v err=%v", coverage, err)
			}
			for _, gap := range []engine.GapMark{pending[0], coverage.Gaps[0]} {
				wire, err := json.Marshal(gap)
				if err != nil {
					t.Fatal(err)
				}
				var got struct {
					Recovery string `json:"recovery"`
					Cause    string `json:"recovery_cause"`
				}
				if err := json.Unmarshal(wire, &got); err != nil {
					t.Fatal(err)
				}
				if got.Recovery != tc.recovery || got.Cause != tc.cause {
					t.Errorf("gap classification=%s want %s/%s", wire, tc.recovery, tc.cause)
				}
			}
		})
	}
}

func TestLegacyGapStatusIsTerminalWithoutInventingCoverage(t *testing.T) {
	s := confOpen(t)
	querySQL(t, s, `INSERT INTO gaps(account,reason,detected_at) VALUES(?,'state_lost',100)`, confAccount)
	_, status, err := s.ReadStatus(context.Background(), confAccount)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		CoverageGaps int            `json:"coverage_gaps"`
		TerminalGaps int            `json:"terminal_gaps"`
		GapCauses    map[string]int `json:"gap_causes"`
	}
	if err := json.Unmarshal(wire, &got); err != nil {
		t.Fatal(err)
	}
	if status.OpenGaps != 0 || got.CoverageGaps != 1 || got.TerminalGaps != 1 || got.GapCauses["unknown_baseline"] != 1 {
		t.Fatalf("terminal unknown-baseline gap still blocks update readiness or lacks cause: %s", wire)
	}
	complete, _, err := s.ReadCoverage(context.Background(), confAccount, nil)
	if err != nil || complete {
		t.Fatalf("coverage=%v err=%v", complete, err)
	}
	gaps, err := s.PendingGaps(context.Background(), confAccount, nil)
	if err != nil || len(gaps) != 1 || gaps[0].Filled != nil || gaps[0].Reason != engine.GapStateLost {
		t.Fatalf("classification changed retained evidence: %+v %v", gaps, err)
	}
}
