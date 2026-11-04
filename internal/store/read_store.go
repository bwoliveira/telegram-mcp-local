package store

import (
	"context"
	"database/sql"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// checkReadAccount gives the additive local-read seam its strict account
// contract without changing the older Persister APIs' validation semantics.
func (s *Store) checkReadAccount(ctx context.Context, account engine.AccountID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if account == "" || account != s.account {
		return engine.ErrAccountMismatch
	}
	return nil
}

// ReadPeers includes every observed peer, even without a contact or dialog.
// The public columns omit access hashes; each scan owns its nested values.
func (s *Store) ReadPeers(ctx context.Context, account engine.AccountID) ([]engine.Peer, error) {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+queryPeerColumns+` FROM peers p WHERE p.account=? ORDER BY p.peer_type,p.peer_id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []engine.Peer{}
	for rows.Next() {
		p, err := queryScanPeer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReadDialogs returns persisted server counters, not counts reconstructed from
// local messages. This snapshot seam deliberately has no presentation limit.
func (s *Store) ReadDialogs(ctx context.Context, account engine.AccountID) ([]engine.Dialog, error) {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT peer_type,peer_id,folder_id,pinned,top_message_id,read_inbox_max_id,read_outbox_max_id,unread_count,unread_mentions,unread_reactions,unread_mark,mute_until,ttl_period FROM dialogs WHERE account=? ORDER BY peer_type,peer_id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []engine.Dialog{}
	for rows.Next() {
		var d engine.Dialog
		var mute sql.NullInt64
		if err := rows.Scan(&d.Peer.Type, &d.Peer.ID, &d.FolderID, &d.Pinned, &d.TopMessageID, &d.ReadInboxMaxID, &d.ReadOutboxMaxID, &d.UnreadCount, &d.UnreadMentions, &d.UnreadReactions, &d.UnreadMark, &mute, &d.TTLPeriod); err != nil {
			return nil, err
		}
		d.MuteUntil = queryTime(mute.Int64, mute.Valid)
		out = append(out, d)
	}
	return out, rows.Err()
}
