package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// ReadCoverage intersects the proof for every peer in scope. A single SQL
// statement keeps cursor/access/gap/checkpoint evidence in one SQLite snapshot.
// All observed peers are included globally, even those without a cursor/dialog;
// an unknown conversation must not inherit another conversation's completeness.
func (s *Store) ReadCoverage(ctx context.Context, account engine.AccountID, peer *engine.PeerRef) (bool, *time.Time, error) {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return false, nil, err
	}
	scope := `SELECT peer_type,peer_id FROM peers WHERE account=(SELECT account FROM local_account)
 UNION SELECT peer_type,peer_id FROM dialogs WHERE account=(SELECT account FROM local_account)
 UNION SELECT peer_type,peer_id FROM messages WHERE account=(SELECT account FROM local_account)
 UNION SELECT peer_type,peer_id FROM backfill_cursors WHERE account=(SELECT account FROM local_account)
 UNION SELECT peer_type,peer_id FROM gaps WHERE account=(SELECT account FROM local_account) AND filled_at IS NULL AND peer_type IS NOT NULL AND peer_id IS NOT NULL`
	gapFilter := ""
	args := []any{account}
	if peer != nil {
		if !validPeer(*peer) {
			return false, nil, engine.ErrInvalid
		}
		scope = `SELECT ?,?`
		args = append(args, peer.Type, peer.ID)
		gapFilter = ` AND (g.peer_type IS NULL OR EXISTS(SELECT 1 FROM scope s WHERE s.peer_type=g.peer_type AND s.peer_id=g.peer_id))`
	}
	var complete, held, gaps bool
	var oldest sql.NullInt64
	err := s.reader.QueryRowContext(ctx, `WITH local_account(account) AS (VALUES (?)),scope(peer_type,peer_id) AS (`+scope+`)
 SELECT coalesce(min(coalesce(c.complete=1 AND c.state='idle' AND c.flood_until IS NULL AND c.last_error='' AND p.access='ok',0)),0),
 CASE WHEN count(*)=count(c.oldest_date) THEN max(c.oldest_date) END,
 EXISTS(SELECT 1 FROM updates_state u WHERE u.account=(SELECT account FROM local_account) AND u.held<>0),
 EXISTS(SELECT 1 FROM gaps g WHERE g.account=(SELECT account FROM local_account) AND g.filled_at IS NULL`+gapFilter+`)
 FROM scope s
 LEFT JOIN backfill_cursors c ON c.account=(SELECT account FROM local_account) AND c.peer_type=s.peer_type AND c.peer_id=s.peer_id
 LEFT JOIN peers p ON p.account=(SELECT account FROM local_account) AND p.peer_type=s.peer_type AND p.peer_id=s.peer_id`, args...).Scan(&complete, &oldest, &held, &gaps)
	if err != nil {
		return false, nil, err
	}
	return complete && !held && !gaps, queryTime(oldest.Int64, oldest.Valid), nil
}

// ReadGroup requires an actual saved snapshot (unlike the legacy Group helper,
// which may synthesize a partial placeholder). A read-only transaction prevents
// mixing peer metadata and participants from different Apply transactions.
func (s *Store) ReadGroup(ctx context.Context, account engine.AccountID, ref engine.PeerRef) (engine.Group, error) {
	var g engine.Group
	if err := s.checkReadAccount(ctx, account); err != nil {
		return g, err
	}
	if !validPeer(ref) || ref.Type == engine.PeerUser {
		return g, engine.ErrInvalid
	}
	tx, err := s.reader.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return g, err
	}
	defer tx.Rollback()
	g.Peer, err = queryScanPeer(tx.QueryRowContext(ctx, `SELECT `+queryPeerColumns+` FROM peers p WHERE p.account=? AND p.peer_type=? AND p.peer_id=?`, account, ref.Type, ref.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return engine.Group{}, engine.ErrPeerNotFound
	}
	if err != nil {
		return engine.Group{}, err
	}
	if g.Peer.Kind != engine.KindGroup && g.Peer.Kind != engine.KindSupergroup && g.Peer.Kind != engine.KindChannel {
		return engine.Group{}, engine.ErrInvalid
	}
	var owner, created, linkedID sql.NullInt64
	var linkedType sql.NullString
	var at int64
	err = tx.QueryRowContext(ctx, `SELECT about,owner_id,created_at,linked_type,linked_id,size,partial,am_admin,snapshot_at FROM groups_full WHERE account=? AND peer_type=? AND peer_id=?`, account, ref.Type, ref.ID).Scan(&g.About, &owner, &created, &linkedType, &linkedID, &g.Size, &g.Partial, &g.AmAdmin, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.Group{}, engine.ErrPeerNotFound
	}
	if err != nil {
		return engine.Group{}, err
	}
	g.CreatedAt = queryTime(created.Int64, created.Valid)
	g.SnapshotAt = time.Unix(at, 0).UTC()
	if owner.Valid {
		g.Owner = &engine.PeerRef{Type: engine.PeerUser, ID: owner.Int64}
	}
	if linkedType.Valid && linkedID.Valid {
		g.LinkedChat = &engine.PeerRef{Type: engine.PeerType(linkedType.String), ID: linkedID.Int64}
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.user_type,t.user_id,coalesce(nullif(p.title,''),trim(p.first_name||' '||p.last_name),''),coalesce(p.username,''),t.role,t.joined_at,t.admin_rights,t.banned_rights,t.banned_until,t.can_edit FROM participants t LEFT JOIN peers p ON p.account=t.account AND p.peer_type=t.user_type AND p.peer_id=t.user_id WHERE t.account=? AND t.group_type=? AND t.group_id=? ORDER BY t.user_type,t.user_id`, account, ref.Type, ref.ID)
	if err != nil {
		return engine.Group{}, err
	}
	defer rows.Close()
	g.Participants = []engine.Participant{}
	for rows.Next() {
		var p engine.Participant
		var joined sql.NullInt64
		if err := rows.Scan(&p.Peer.Type, &p.Peer.ID, &p.Name, &p.Username, &p.Role, &joined, &p.AdminRights, &p.BannedRights, &p.BannedUntil, &p.CanEdit); err != nil {
			return engine.Group{}, err
		}
		p.JoinedAt = queryTime(joined.Int64, joined.Valid)
		g.Participants = append(g.Participants, p)
	}
	if err := rows.Err(); err != nil {
		return engine.Group{}, err
	}
	if err := rows.Close(); err != nil {
		return engine.Group{}, err
	}
	if err := tx.Commit(); err != nil {
		return engine.Group{}, err
	}
	return g, nil
}
