package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// PendingGaps exposes exact-scope recovery evidence without coupling the engine
// to SQLite or to the user-facing history/coverage aggregation. A nil peer means
// account-wide gaps, not all channel gaps. Access-loss evidence is retained.
func (s *Store) PendingGaps(ctx context.Context, account engine.AccountID, peer *engine.PeerRef) ([]engine.GapMark, error) {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return nil, err
	}
	var pt, pi any
	if peer != nil {
		if !validPeer(*peer) {
			return nil, engine.ErrInvalid
		}
		pt, pi = peer.Type, peer.ID
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT g.id,g.reason,coalesce(g.from_id,0),coalesce(g.to_id,0),g.detected_at,g.recovery_anchor,g.recovery_cursor,`+gapRecoveryCauseSQL+` FROM gaps g WHERE g.account=? AND g.peer_type IS ? AND g.peer_id IS ? AND g.filled_at IS NULL ORDER BY g.detected_at,g.id`, account, pt, pi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []engine.GapMark{}
	for rows.Next() {
		var g engine.GapMark
		var at sql.NullInt64
		var anchor, cursor sql.NullString
		if err := rows.Scan(&g.RecoveryID, &g.Reason, &g.FromID, &g.ToID, &at, &anchor, &cursor, &g.RecoveryCause); err != nil {
			return nil, err
		}
		if anchor.Valid && cursor.Valid {
			var a, c engine.UpdatesState
			if err := json.Unmarshal([]byte(anchor.String), &a); err != nil {
				return nil, err
			}
			if err := json.Unmarshal([]byte(cursor.String), &c); err != nil {
				return nil, err
			}
			g.RecoveryAnchor, g.RecoveryCursor = &a, &c
		}
		classifyGap(&g)
		g.Peer = peer
		g.Detected = timeFromUnix(at.Int64)
		result = append(result, g)
	}
	return result, rows.Err()
}
