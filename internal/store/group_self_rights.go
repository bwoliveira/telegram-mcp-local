package store

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// RefreshGroupSelfRights merges explicit actor permissions without replacing a
// retained complete membership snapshot or advancing its collection timestamp.
// The engine serializes this with its refresh/check/apply commit lock.
func (s *Store) RefreshGroupSelfRights(ctx context.Context, account engine.AccountID, group engine.PeerRef, self engine.Participant, amAdmin bool) error {
	if err := s.checkAccount(account); err != nil {
		return err
	}
	if !validPeer(group) || group.Type == engine.PeerUser || !validPeer(self.Peer) || self.Peer.Type != engine.PeerUser {
		return engine.ErrInvalid
	}
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE groups_full SET am_admin=? WHERE account=? AND peer_type=? AND peer_id=? AND partial=0`, amAdmin, account, group.Type, group.ID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tx.Commit()
	}
	// Full-chat metadata does not report CanEdit or JoinedAt. Keep both as
	// collected, and do not touch any other participant or peer metadata.
	result, err = tx.ExecContext(ctx, `UPDATE participants SET role=?,admin_rights=?,banned_rights=?,banned_until=?,updated_at=? WHERE account=? AND group_type=? AND group_id=? AND user_type=? AND user_id=?`, self.Role, self.AdminRights, self.BannedRights, self.BannedUntil, s.now().Unix(), account, group.Type, group.ID, self.Peer.Type, self.Peer.ID)
	if err != nil {
		return err
	}
	n, err = result.RowsAffected()
	if err != nil {
		return err
	}
	// Nonzero rights or creator status explicitly prove membership even if an
	// earlier collection omitted self. Empty rights alone do not prove presence.
	if n == 0 && (self.Role == "creator" || self.AdminRights != 0 || self.BannedRights != 0) {
		if err = ensurePeer(ctx, tx, account, self.Peer, s.now().Unix()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO participants(account,group_type,group_id,user_type,user_id,role,admin_rights,banned_rights,banned_until,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, account, group.Type, group.ID, self.Peer.Type, self.Peer.ID, self.Role, self.AdminRights, self.BannedRights, self.BannedUntil, s.now().Unix()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
