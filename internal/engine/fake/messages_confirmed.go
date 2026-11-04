package fake

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// DeleteConfirmed adapts the legacy daemon capability to the canonical request.
func (f *Controller) DeleteConfirmed(ctx context.Context, r engine.DeleteRequest, confirmed bool) (engine.WriteResult, error) {
	r.ConfirmRevoke = confirmed
	return f.Delete(ctx, r)
}
