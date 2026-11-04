package daemon

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Forward confirmation as a separate capability through the stable controller;
// embedding only Engine would silently hide the production implementation.
func (m *managedController) DeleteConfirmed(ctx context.Context, req engine.DeleteRequest, confirmed bool) (engine.WriteResult, error) {
	c := m.controller()
	if d, ok := c.(engine.ConfirmedDeleter); ok {
		return d.DeleteConfirmed(ctx, req, confirmed)
	}
	if req.Revoke && !req.DryRun {
		return engine.WriteResult{}, engine.ErrPermission
	}
	return c.Delete(ctx, req)
}

func (m *managedController) HistoryProgress(ctx context.Context, peer engine.PeerRef) (engine.BackfillResult, error) {
	if reader, ok := m.controller().(engine.HistoryProgressReader); ok {
		return reader.HistoryProgress(ctx, peer)
	}
	return engine.BackfillResult{Peer: peer}, engine.ErrNotSupported
}

var _ engine.ConfirmedDeleter = (*managedController)(nil)
var _ engine.HistoryProgressReader = (*managedController)(nil)
