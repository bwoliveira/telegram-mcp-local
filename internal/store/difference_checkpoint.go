package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// SaveDifferenceCheckpoint advances the replay chain and checkpoint atomically.
// It is called only after the whole authoritative response crossed the data
// barrier. A final empty difference is useful ONLY at the proven chain cursor.
func (s *Store) SaveDifferenceCheckpoint(ctx context.Context, a engine.AccountID, p engine.DifferenceCheckpoint) (engine.Applied, error) {
	var ap engine.Applied
	if err := s.checkAccount(a); err != nil {
		return ap, err
	}
	if p.Channel < 0 {
		return ap, engine.ErrInvalid
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return ap, err
	}
	defer tx.Rollback()
	if p.Channel == 0 {
		_, err = tx.ExecContext(ctx, `INSERT INTO updates_state(account,pts,qts,date,seq,updated_at) VALUES(?,?,?,?,?,unixepoch()) ON CONFLICT(account) DO UPDATE SET pts=excluded.pts,qts=excluded.qts,date=excluded.date,seq=excluded.seq`, a, p.State.Pts, p.State.Qts, p.State.Date, p.State.Seq)
	} else {
		_, err = tx.ExecContext(ctx, `INSERT INTO channel_state(account,channel_id,pts,updated_at) VALUES(?,?,?,unixepoch()) ON CONFLICT(account,channel_id) DO UPDATE SET pts=excluded.pts`, a, p.Channel, p.State.Pts)
	}
	if err != nil {
		return ap, err
	}
	var pt, pi any
	if p.Channel != 0 {
		pt, pi = engine.PeerChannel, p.Channel
	}
	var blocked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gaps WHERE account=? AND peer_type IS ? AND peer_id IS ? AND filled_at IS NULL AND reason IN ('difference_too_long','channel_too_long'))`, a, pt, pi).Scan(&blocked); err != nil {
		return ap, err
	}
	for _, g := range p.Gaps {
		if blocked || g.Reason != engine.GapStateLost || g.RecoveryAnchor == nil || g.RecoveryCursor == nil {
			continue
		}
		var anchor, cursor sql.NullString
		var cause engine.GapRecoveryCause
		err := tx.QueryRowContext(ctx, `SELECT recovery_anchor,recovery_cursor,`+gapRecoveryCauseSQL+` FROM gaps g WHERE id=? AND account=? AND peer_type IS ? AND peer_id IS ? AND reason='state_lost' AND filled_at IS NULL`, g.RecoveryID, a, pt, pi).Scan(&anchor, &cursor, &cause)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return ap, err
		}
		if terminalGapCause(cause) || !anchor.Valid || !cursor.Valid {
			continue
		}
		var original, progress engine.UpdatesState
		if err := json.Unmarshal([]byte(anchor.String), &original); err != nil {
			return ap, err
		}
		if err := json.Unmarshal([]byte(cursor.String), &progress); err != nil {
			return ap, err
		}
		// Compare real request counters, not wall time or only PTS. Seq is absent
		// from getDifference; channel differences have only PTS as their baseline.
		matches := progress.Pts == p.From.Pts
		forward := p.State.Pts >= progress.Pts
		if p.Channel == 0 {
			matches = matches && progress.Qts == p.From.Qts && progress.Date == p.From.Date
			forward = forward && p.State.Qts >= progress.Qts
		}
		if original != *g.RecoveryAnchor || progress != *g.RecoveryCursor || !matches || !forward {
			continue
		}
		var filled any
		if p.Final {
			filled = s.now().Unix()
		}
		if _, err := tx.ExecContext(ctx, `UPDATE gaps SET recovery_cursor=?,filled_at=? WHERE id=? AND account=?`, encode(p.State), filled, g.RecoveryID, a); err != nil {
			return ap, err
		}
		if p.Final {
			at := s.now()
			g.Filled = &at
			ap.Gaps = append(ap.Gaps, g)
		}
	}
	if err := s.fail("difference_checkpoint_before_commit"); err != nil {
		return engine.Applied{}, err
	}
	// Retain the existing fill fault boundary for callers that exercise it.
	if len(ap.Gaps) > 0 {
		if err := s.fail("apply_before_commit"); err != nil {
			return engine.Applied{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return engine.Applied{}, err
	}
	return ap, nil
}
