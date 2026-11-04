package gotdengine

import (
	"context"
	"errors"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

// Dialog discovery is independent of message traffic: a newly created empty
// broadcast might not produce a content-bearing update. Keep refreshing the
// latest dialog page, without receipts, and coalesce channel metadata signals.
// Cancellation joins the worker before this transport generation is released.
func (e *Engine) startDialogRefresh(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	// Full-channel migration origins are immutable for a channel ID. Remember
	// successful negative reads for this worker generation too, so ordinary
	// basic groups do not cause a full-RPC fan-out on every minute tick. The
	// cache belongs to this serial worker, not to global or durable peer state.
	ctx = context.WithValue(ctx, migrationDiscoveryCacheKey{}, &migrationDiscoveryState{checked: make(map[engine.PeerRef]bool)})
	done := make(chan struct{})
	interval := e.dialogRefreshEvery
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var backoff dialogRefreshBackoff
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-e.dialogRefresh:
			}
			backoff.refresh(ctx, interval, e.now, func(ctx context.Context) error {
				_, err := e.RefreshDialogs(ctx, engine.RefreshRequest{Limit: 100})
				// Migration metadata has its own per-generation FloodWait gate.
				// Keep accepting independent dialog ticks/signals; only a failure
				// of getDialogs itself may impose its FloodWait on this gate.
				var flood *engine.FloodWaitError
				if errors.As(err, &flood) && flood.Method == "channels.getFullChannel" {
					return nil
				}
				return err
			})
		}
	}()
	return func() { cancel(); <-done }
}

// dialogRefreshBackoff is owned by the refresh worker. Both ticks and channel
// signals pass through the same gate; errors cannot turn signals into a retry
// loop. Waiting never blocks the worker's cancellation or signal coalescing.
type dialogRefreshBackoff struct {
	retryAt time.Time
}

func (b *dialogRefreshBackoff) refresh(ctx context.Context, interval time.Duration, now func() time.Time, refresh func(context.Context) error) {
	if ctx.Err() != nil || now().Before(b.retryAt) {
		return
	}
	err := refresh(ctx)
	if err == nil {
		b.retryAt = time.Time{}
		return
	}
	// Measure the ordinary retry delay from completion, not request start.
	b.retryAt = now().Add(interval)
	// The middleware only waits up to FloodWaitMax. Longer waits must be
	// respected here, including when the typed error is wrapped.
	var flood *engine.FloodWaitError
	if errors.As(err, &flood) && flood.Until.After(b.retryAt) {
		b.retryAt = flood.Until
	}
}

func (e *Engine) requestDialogDiscovery(updates []tg.UpdateClass) {
	for _, raw := range updates {
		discovery := false
		switch u := raw.(type) {
		case *tg.UpdateChannel:
			discovery = true
		case *tg.UpdateNewChannelMessage:
			if m, ok := u.Message.(*tg.MessageService); ok {
				_, discovery = m.Action.(*tg.MessageActionChannelCreate)
			}
		}
		if discovery {
			select {
			case e.dialogRefresh <- struct{}{}:
			default:
			}
			return
		}
	}
}
