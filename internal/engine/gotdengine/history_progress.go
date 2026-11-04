package gotdengine

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// HistoryProgress reads the durable getHistory boundary without network access.
// This optional structural seam lets schedulers preserve progress on a failed
// first page after restart; it does not broaden engine.Engine. Fetched, Added
// and Rounds remain zero because they count this invocation, not cursor lifetime.
// Complete means accessible history start only, never gap-free store coverage.
func (e *Engine) HistoryProgress(ctx context.Context, peer engine.PeerRef) (engine.BackfillResult, error) {
	result := engine.BackfillResult{Peer: peer}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if peer.ID <= 0 || (peer.Type != engine.PeerUser && peer.Type != engine.PeerChat && peer.Type != engine.PeerChannel) {
		return result, engine.ErrInvalid
	}
	cursor, ok, err := e.cfg.Store.Cursor(ctx, e.cfg.Account, peer)
	if err != nil || !ok {
		return result, err
	}
	result.Complete = cursor.Complete
	result.OldestID = cursor.OldestID
	if cursor.OldestDate != nil {
		date := *cursor.OldestDate
		result.OldestDate = &date
	}
	if cursor.FloodUntil != nil {
		until := *cursor.FloodUntil
		result.FloodUntil = &until
	}
	return result, nil
}
