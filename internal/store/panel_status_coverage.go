package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// ConversationCoverage is durable coverage evidence for a saved dialog. Oldest
// is the cursor's known local lower bound, not a claim of complete history.
type ConversationCoverage struct {
	Peer     engine.PeerRef
	Name     string
	Complete bool
	Oldest   *time.Time
}

// Read the account checkpoint and group pending peer gaps once, rather than
// issuing ReadCoverage for every dialog. All evidence shares one SQLite snapshot.
// A global gap has NULL peer_type, exactly as in ReadCoverage(peer); access-loss
// gaps remain evidence too. A peer gap must never invalidate unrelated peers.
const panelStatusCoverageQuery = `WITH local_account(account) AS (VALUES (?)),
 account_proof AS (
 SELECT EXISTS(SELECT 1 FROM updates_state u WHERE u.account=(SELECT account FROM local_account) AND u.held<>0) AS held,
 EXISTS(SELECT 1 FROM gaps g WHERE g.account=(SELECT account FROM local_account) AND g.filled_at IS NULL AND g.peer_type IS NULL) AS global_gap
 ), peer_gaps AS (
 SELECT g.peer_type,g.peer_id,1 AS pending FROM gaps g
 WHERE g.account=(SELECT account FROM local_account) AND g.filled_at IS NULL AND g.peer_type IS NOT NULL
 GROUP BY g.peer_type,g.peer_id
 )
 SELECT d.peer_type,d.peer_id,coalesce(nullif(p.title,''),p.first_name||' '||p.last_name,''),
 coalesce(c.complete=1 AND c.state='idle' AND c.flood_until IS NULL AND c.last_error='' AND p.access='ok',0)
 AND NOT a.held AND NOT a.global_gap AND coalesce(g.pending,0)=0,
 c.oldest_date
 FROM dialogs d CROSS JOIN account_proof a
 LEFT JOIN peers p ON p.account=d.account AND p.peer_type=d.peer_type AND p.peer_id=d.peer_id
 LEFT JOIN backfill_cursors c ON c.account=d.account AND c.peer_type=d.peer_type AND c.peer_id=d.peer_id
 LEFT JOIN peer_gaps g ON g.peer_type=d.peer_type AND g.peer_id=d.peer_id
 WHERE d.account=(SELECT account FROM local_account)
 ORDER BY d.peer_type,d.peer_id`

// ReadConversationCoverage batches the per-peer ReadCoverage semantics for all
// saved dialogs. It deliberately excludes peer-cache-only conversations, just
// like the panel's previous ReadDialogs loop. No message content is projected.
func (s *Store) ReadConversationCoverage(ctx context.Context, account engine.AccountID) ([]ConversationCoverage, error) {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, panelStatusCoverageQuery, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConversationCoverage{}
	for rows.Next() {
		var row ConversationCoverage
		var oldest sql.NullInt64
		if err := rows.Scan(&row.Peer.Type, &row.Peer.ID, &row.Name, &row.Complete, &oldest); err != nil {
			return nil, err
		}
		if !validPeer(row.Peer) {
			return nil, engine.ErrInvalid
		}
		row.Oldest = queryTime(oldest.Int64, oldest.Valid)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
