package store

import "github.com/bwoliveira/telegram-mcp-local/internal/engine"

// Read-time classification preserves legacy evidence without inventing anchors
// or marking rows filled. The outer gaps table must use the alias g.
const gapRecoveryCauseSQL = `CASE
 WHEN g.reason='access_lost' THEN 'access_lost'
 WHEN g.reason='state_lost' AND (g.recovery_anchor IS NULL OR g.recovery_cursor IS NULL) THEN 'unknown_baseline'
 WHEN g.reason='state_lost' AND g.peer_type='channel' AND (EXISTS(SELECT 1 FROM peers p WHERE p.account=g.account AND p.peer_type=g.peer_type AND p.peer_id=g.peer_id AND p.access<>'ok') OR EXISTS(SELECT 1 FROM gaps b WHERE b.account=g.account AND b.peer_type IS g.peer_type AND b.peer_id IS g.peer_id AND b.reason='access_lost' AND b.detected_at>=g.detected_at)) THEN 'access_lost'
 WHEN g.reason='difference_too_long' THEN 'difference_too_long'
 WHEN g.reason='channel_too_long' THEN 'channel_too_long'
 WHEN EXISTS(SELECT 1 FROM gaps b WHERE b.account=g.account AND b.peer_type IS g.peer_type AND b.peer_id IS g.peer_id AND b.filled_at IS NULL AND b.reason IN ('difference_too_long','channel_too_long')) THEN 'stream_truncated'
 ELSE 'replay_pending' END`

func terminalGapCause(c engine.GapRecoveryCause) bool {
	return c != engine.GapRecoveryReplayPending
}

func classifyGap(g *engine.GapMark) {
	g.Recovery = engine.GapRecoverable
	if terminalGapCause(g.RecoveryCause) {
		g.Recovery = engine.GapTerminal
	}
}
