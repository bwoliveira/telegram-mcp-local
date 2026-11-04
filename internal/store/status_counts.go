package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// ReadStatus reads retained-history totals and the durable update checkpoint
// from one SQLite snapshot. It does not fetch Telegram or establish complete
// history coverage; ReadCoverage is the separate conservative proof for that.
// Conversations are typed peers with a dialog or a visible retained message,
// not every contact/participant observed in the peer cache. LastAt is the last
// local arrival of a retained message, never its historical Telegram date.
func (s *Store) ReadStatus(ctx context.Context, account engine.AccountID) (engine.HistoryProgress, engine.UpdatesStatus, error) {
	var history engine.HistoryProgress
	var updates engine.UpdatesStatus
	if err := s.checkReadAccount(ctx, account); err != nil {
		return history, updates, err
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return history, updates, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	var last sql.NullInt64
	err = tx.QueryRowContext(ctx, panelStatusCountsQuery, account, now, now, account).Scan(&history.Messages, &history.Conversations, &last)
	if err != nil {
		return engine.HistoryProgress{}, engine.UpdatesStatus{}, err
	}
	if last.Valid {
		history.LastAt = time.Unix(last.Int64, 0).UTC()
	}
	var running engine.PeerRef
	err = tx.QueryRowContext(ctx, `SELECT peer_type,peer_id FROM backfill_cursors WHERE account=? AND state='running' ORDER BY peer_type,peer_id LIMIT 1`, account).Scan(&running.Type, &running.ID)
	if err == nil {
		history.Running = &running
	} else if !errors.Is(err, sql.ErrNoRows) {
		return engine.HistoryProgress{}, engine.UpdatesStatus{}, err
	}
	var date int64
	var lastUpdate sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT pts,qts,seq,date,held,last_update_at FROM updates_state WHERE account=?`, account).Scan(&updates.Pts, &updates.Qts, &updates.Seq, &date, &updates.Held, &lastUpdate)
	if err == nil {
		updates.Date = time.Unix(date, 0).UTC()
		updates.LastUpdateAt = queryTime(lastUpdate.Int64, lastUpdate.Valid)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return engine.HistoryProgress{}, engine.UpdatesStatus{}, err
	}
	err = tx.QueryRowContext(ctx, `SELECT count(*) FROM channel_state WHERE account=?`, account).Scan(&updates.Channels)
	if err != nil {
		return engine.HistoryProgress{}, engine.UpdatesStatus{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+gapRecoveryCauseSQL+`,count(*) FROM gaps g WHERE g.account=? AND g.filled_at IS NULL GROUP BY 1`, account)
	if err != nil {
		return engine.HistoryProgress{}, engine.UpdatesStatus{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var cause engine.GapRecoveryCause
		var count int
		if err := rows.Scan(&cause, &count); err != nil {
			return engine.HistoryProgress{}, engine.UpdatesStatus{}, err
		}
		if updates.GapCauses == nil {
			updates.GapCauses = make(map[engine.GapRecoveryCause]int)
		}
		updates.GapCauses[cause] = count
		updates.CoverageGaps += count
		if terminalGapCause(cause) {
			updates.TerminalGaps += count
		} else {
			updates.OpenGaps += count
		}
		if cause == engine.GapRecoveryAccessLost {
			updates.InaccessibleGaps += count
		}
	}
	if err := rows.Err(); err != nil {
		return engine.HistoryProgress{}, engine.UpdatesStatus{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.HistoryProgress{}, engine.UpdatesStatus{}, err
	}
	return history, updates, nil
}
