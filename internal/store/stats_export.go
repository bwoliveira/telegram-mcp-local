package store

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// StreamExportMessages calls fn in ascending (date,peer_type,peer_id,msg_id)
// order without a row cap or a materialized message slice. It propagates the
// first callback, scanner, database, or context failure and closes its cursor.
//
// Export policy is stricter than ordinary visibility: all TTL messages/media
// and every expires_at value (even future ones) are omitted, as are pending
// deletes/tombstones. The shared public scanner never restores Media.Remote.
// Protection is effective message OR peer protection, resolved by messageColumns
// in the same query snapshot. Protected text remains marked Protected, but its
// Media is omitted before invoking the callback (D8).
// This read-only API does not create/register an export file; its caller owns
// NDJSON encoding, atomic publication, permissions and lifecycle registration.
func (s *Store) StreamExportMessages(ctx context.Context, account engine.AccountID, f StatsFilter, fn func(engine.Message) error) error {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return err
	}
	if fn == nil {
		return engine.ErrInvalid
	}
	w, a, err := statsWhere(account, s.now(), f)
	if err != nil {
		return err
	}
	w += ` AND m.ttl_period<=0 AND m.expires_at IS NULL AND m.media_ttl<=0`
	rows, err := s.reader.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages m WHERE `+w+` ORDER BY m.date,m.peer_type,m.peer_id,m.msg_id`, a...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		m, err := scanMessage(rows)
		if err != nil {
			return err
		}
		if m.Protected {
			m.Media = nil
			m.Location = nil
			m.Poll = nil
		}
		if err := fn(m); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return rows.Err()
}
