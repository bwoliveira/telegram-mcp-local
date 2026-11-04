package store

import (
	"context"
	"database/sql"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Unfilled access-loss evidence is a terminal summary, not an event journal.
// Retain the newest observation so access restoration cannot make an older
// interrupted replay chain actionable again. Filled historical evidence and
// other causes are deliberately outside this uniqueness constraint.
const accessLossTerminalIndexSQL = `CREATE UNIQUE INDEX IF NOT EXISTS gaps_access_lost_terminal
 ON gaps(account,coalesce(peer_type,''),coalesce(peer_id,0),reason)
 WHERE reason='access_lost' AND filled_at IS NULL`

func (s *Store) migrateV9(ctx context.Context) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM gaps WHERE id IN (
  SELECT id FROM (
   SELECT id,row_number() OVER (
    PARTITION BY account,peer_type,peer_id,reason ORDER BY detected_at DESC,id DESC
   ) AS ordinal FROM gaps WHERE reason='access_lost' AND filled_at IS NULL
  ) WHERE ordinal>1
 )`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, accessLossTerminalIndexSQL); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version=9`); err != nil {
		return err
	}
	if err = s.fail("migration_v9_before_commit"); err != nil {
		return err
	}
	return tx.Commit()
}

// refreshAccessLoss coalesces observations across metadata refresh, callbacks
// and process restarts. A newer time is metadata on existing evidence, not a
// newly opened gap/event. The caller inserts only when no summary exists.
func refreshAccessLoss(ctx context.Context, tx *sql.Tx, a engine.AccountID, g engine.GapMark) (bool, error) {
	var pt, pi any
	if g.Peer != nil {
		pt, pi = g.Peer.Type, g.Peer.ID
	}
	var id, detected int64
	err := tx.QueryRowContext(ctx, `SELECT id,detected_at FROM gaps
 WHERE account=? AND peer_type IS ? AND peer_id IS ? AND reason='access_lost' AND filled_at IS NULL`, a, pt, pi).Scan(&id, &detected)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if detected >= g.Detected.Unix() {
		return true, nil
	}
	_, err = tx.ExecContext(ctx, `UPDATE gaps SET detected_at=? WHERE id=?`, g.Detected.Unix(), id)
	return true, err
}
