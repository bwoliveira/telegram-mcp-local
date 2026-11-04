package panel

import (
	"context"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Stop joins both speculative refreshes and HTTP-owned cache loads before the
// daemon closes the underlying account stores. It is safe before Start.
func (p *Panel) Stop() {
	p.cache.mu.Lock()
	cancel, done := p.cache.cancel, p.cache.done
	if cancel != nil {
		cancel()
	}
	p.cache.mu.Unlock()
	if done != nil {
		<-done
	}
}

// snapshotContext counts request-owned refreshes alongside speculative ones.
// Cancellation of either the navigation or daemon prevents publication.
func (p *Panel) snapshotContext(request context.Context) (context.Context, func(), error) {
	p.cache.mu.Lock()
	defer p.cache.mu.Unlock()
	if lifetime := p.cache.ctx; lifetime != nil && lifetime.Err() != nil {
		return nil, func() {}, lifetime.Err()
	}
	ctx, cancel := context.WithTimeout(request, 15*time.Second)
	if p.cache.ctx == nil {
		return ctx, cancel, nil
	}
	p.cache.workers.Add(1)
	stop := context.AfterFunc(p.cache.ctx, cancel)
	return ctx, func() { stop(); cancel(); p.cache.workers.Done() }, nil
}

// Changes() is a single-consumer wakeup already owned by the daemon watcher,
// not a broadcast. Checking the cheap live lifecycle also fences login, mode,
// revocation and generation changes without stealing that consumer's signals.
func (p *Panel) observeLifecycle(s engine.Status) {
	key := string(s.State) + "/" + string(s.Mode) + "/" + s.Since.Format(time.RFC3339Nano)
	p.cache.lifecycleMu.Lock()
	defer p.cache.lifecycleMu.Unlock()
	if old := p.cache.lifecycle; old != "" && old != key {
		p.invalidateSnapshots()
	}
	p.cache.lifecycle = key
}

func (p *Panel) observeAccount(a engine.Account) {
	p.cache.lifecycleMu.Lock()
	defer p.cache.lifecycleMu.Unlock()
	if p.cache.authObserved && p.cache.authenticated != a.Authenticated {
		p.invalidateSnapshots()
	}
	p.cache.authObserved = true
	p.cache.authenticated = a.Authenticated
}
