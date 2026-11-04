package store

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// ReadMigrationPeers is an optional discovery seam, not a presentation query.
// Include every basic peer (even without a dialog) so peers-only predecessors
// visible in ListChats can be linked. Destinations require an indexed dialog,
// joined supergroup metadata, and a usable non-min hash: incidental peers must
// not cause network reads. The caller bounds and rotates remote probes.
func (s *Store) ReadMigrationPeers(ctx context.Context, account engine.AccountID) ([]engine.Peer, error) {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT `+queryPeerColumns+` FROM peers p
 WHERE p.account=? AND (p.peer_type='chat' OR (
 p.peer_type='channel' AND p.kind='supergroup' AND p.access='ok'
 AND p.access_hash IS NOT NULL AND p.hash_min=0
 AND EXISTS (SELECT 1 FROM dialogs d WHERE d.account=p.account AND d.peer_type=p.peer_type AND d.peer_id=p.peer_id)))
 ORDER BY p.peer_type,p.peer_id`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	peers := []engine.Peer{}
	for rows.Next() {
		p, err := queryScanPeer(rows)
		if err != nil {
			return nil, err
		}
		peers = append(peers, p)
	}
	return peers, rows.Err()
}
