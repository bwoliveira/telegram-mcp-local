package mcp

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// telegramHistoryProgress prefers the account-scoped persister supplied as
// ReadStore. Production wraps engine.Controller, hiding optional engine methods;
// reading the store avoids requiring a change to that wrapper or engine.Engine.
func (s *Server) telegramHistoryProgress(ctx context.Context, account engine.AccountID, peer engine.PeerRef) (engine.BackfillResult, error) {
	result := engine.BackfillResult{Peer: peer}
	if local, ok := s.readStore.(engine.CursorReader); ok {
		cursor, found, err := local.Cursor(ctx, account, peer)
		if err != nil || !found {
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
	if progress, ok := s.eng.(engine.HistoryProgressReader); ok {
		return progress.HistoryProgress(ctx, peer)
	}
	return result, nil
}
