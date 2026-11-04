package mcp

import (
	"context"
	"errors"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// An observed group is not an unknown peer just because nobody has requested its
// full membership yet. Keep ReadStore's strict snapshot contract and expose the
// metadata-only fallback explicitly, without triggering network access.
func (s *Server) localGroupSnapshot(ctx context.Context, account engine.AccountID, ref engine.PeerRef) (engine.Group, error) {
	g, err := s.readStore.ReadGroup(ctx, account, ref)
	if !errors.Is(err, engine.ErrPeerNotFound) {
		return g, err
	}
	peers, readErr := s.readStore.ReadPeers(ctx, account)
	if readErr != nil {
		return engine.Group{}, readErr
	}
	for _, p := range peers {
		if p.Ref == ref {
			return engine.Group{Peer: p, Size: p.Participants, Partial: true, Participants: []engine.Participant{}}, nil
		}
	}
	return engine.Group{}, err
}
