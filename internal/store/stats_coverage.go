package store

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// StatsCoverage adds open-gap/cursor evidence to ReadCoverage's conservative
// completeness and history bound. A peer scope is exact, matching StatsFilter;
// nil includes the whole account. Account-wide gaps affect every scope.
//
// Reuse the existing query helper for rich metadata, but never inherit its
// older optimistic completeness/minimum-date semantics: ReadCoverage checks
// all observed peers, held update state, cursor errors and access loss. This
// is local synchronization evidence, not a promise of Telegram completeness.
func (s *Store) StatsCoverage(ctx context.Context, account engine.AccountID, peer *engine.PeerRef) (Coverage, error) {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return Coverage{}, err
	}
	var peers []engine.PeerRef
	if peer != nil {
		if !validPeer(*peer) {
			return Coverage{}, engine.ErrInvalid
		}
		peers = []engine.PeerRef{*peer}
	}
	c, err := s.queryCoverage(ctx, account, peers)
	if err != nil {
		return c, err
	}
	complete, since, err := s.ReadCoverage(ctx, account, peer)
	if err != nil {
		return c, err
	}
	c.Complete = c.Complete && complete
	c.HistorySince = since
	return c, nil
}
