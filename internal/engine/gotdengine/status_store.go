package gotdengine

import (
	"context"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// A structural, optional seam keeps the frozen Persister compatible with the
// offline spike stores. The production store implements this aggregate read.
// Do not synthesize retained counts from events: replay, deletion, TTL and
// controller restarts would make those counters diverge from the local index.
type statusStore interface {
	ReadStatus(context.Context, engine.AccountID) (engine.HistoryProgress, engine.UpdatesStatus, error)
}

func (e *Engine) withStoreStatus(s engine.Status) engine.Status {
	r, ok := e.cfg.Store.(statusStore)
	if !ok {
		return s
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	history, updates, err := r.ReadStatus(ctx, e.cfg.Account)
	if err != nil {
		// Status has no error return. Make incomplete local evidence visible
		// and prevent callers from treating an unreadable checkpoint as ready.
		s.LastError = "local store status is unavailable"
		s.Updates.Held = true
		return s
	}
	s.History, s.Updates = history, updates
	return s
}
