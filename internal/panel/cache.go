package panel

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
)

const panelSnapshotTTL = 1500 * time.Millisecond

var errSnapshotInvalidated = errors.New("panel snapshot changed during refresh")

// snapshotCache retains telemetry only, never messages, login credentials or
// authorization decisions. Invalidations fence in-flight work as well as hits.
// Loads are single-flight; failures have the same short backoff as successes.
type snapshotCache[T any] struct {
	mu         sync.Mutex
	value      T
	err        error
	at         time.Time
	generation uint64
	flight     chan struct{}
}

func (c *snapshotCache[T]) version() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

func (c *snapshotCache[T]) invalidate() {
	c.mu.Lock()
	c.generation++
	c.at = time.Time{}
	c.mu.Unlock()
}
func (c *snapshotCache[T]) peek() (T, time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value, c.at, c.err == nil && !c.at.IsZero() && time.Since(c.at) < panelSnapshotTTL
}
func (c *snapshotCache[T]) get(ctx context.Context, load func(context.Context) (T, error)) (T, error) {
	for {
		var zero T
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		c.mu.Lock()
		if !c.at.IsZero() && time.Since(c.at) < panelSnapshotTTL {
			v, err := c.value, c.err
			c.mu.Unlock()
			return v, err
		}
		if wait := c.flight; wait != nil {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-wait:
				continue
			}
		}
		c.flight = make(chan struct{})
		generation := c.generation
		c.mu.Unlock()
		return c.load(ctx, generation, load)
	}
}
func (c *snapshotCache[T]) load(ctx context.Context, generation uint64, load func(context.Context) (T, error)) (T, error) {
	value, err := load(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if ctx.Err() != nil {
		err = ctx.Err()
	} else if c.generation != generation {
		err = errSnapshotInvalidated
	} else {
		c.value, c.err, c.at = value, err, time.Now()
	}
	close(c.flight)
	c.flight = nil
	if err != nil {
		var zero T
		return zero, err
	}
	return value, nil
}

type cachedHealth struct {
	Sync           engine.Status
	Health         mcp.Health
	Diagnostics    engine.Diagnostics
	DiagnosticsErr error
}
type panelCache struct {
	health        snapshotCache[cachedHealth]
	details       snapshotCache[statusView]
	mu            sync.Mutex
	ctx           context.Context
	cancel        context.CancelFunc
	lifecycleMu   sync.Mutex
	lifecycle     string
	authObserved  bool
	authenticated bool
	done          chan struct{}
	workers       sync.WaitGroup
}

// Start binds the sole event subscription and all speculative health refreshes
// to the daemon lifetime. Register alone deliberately starts no goroutines.
// Call once before serving; cancellation unsubscribes and drains owned work.
func (p *Panel) Start(ctx context.Context) {
	p.cache.mu.Lock()
	defer p.cache.mu.Unlock()
	if p.cache.ctx != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	p.cache.ctx = ctx
	p.cache.cancel = cancel
	p.cache.done = make(chan struct{})
	var events <-chan engine.Event
	if p.Engine != nil {
		events = p.Engine.Subscribe(ctx, engine.SubscribeOptions{Buffer: 64})
	}
	go func() {
		defer func() {
			p.cache.mu.Lock()
			cancel()
			p.cache.mu.Unlock()
			p.cache.workers.Wait()
			close(p.cache.done)
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				relevant := p.cacheEvent(event)
				// Drain bursts into one invalidation, without retaining event payloads.
			drain:
				for range 64 {
					select {
					case next, ok := <-events:
						if !ok {
							events = nil
							break drain
						}
						relevant = relevant || p.cacheEvent(next)
					default:
						break drain
					}
				}
				if relevant {
					p.invalidateSnapshots()
				}
			}
		}
	}()
}
func (p *Panel) cacheEvent(event engine.Event) bool {
	// Empty-account lifecycle events describe this controller, not another slot.
	return event.Account == p.AccountID || (event.Account == "" && (event.Kind == engine.EventConnection || event.Kind == engine.EventAuth))
}
func (p *Panel) invalidateSnapshots() { p.cache.health.invalidate(); p.cache.details.invalidate() }
func (p *Panel) loadHealth(ctx context.Context) (cachedHealth, error) {
	if p.Engine == nil || p.Server == nil {
		return cachedHealth{}, errors.New("panel telemetry unavailable")
	}
	var h cachedHealth
	h.Health, h.Sync, h.Diagnostics, h.DiagnosticsErr = p.Server.HealthSnapshot(ctx, 0)
	// Compatibility for an unconfigured in-memory Server used by embedded hosts
	// and older tests. A configured production server returns its lifecycle state.
	if h.Sync.State == "" {
		h.Sync = p.Engine.Status()
		h.Diagnostics, h.DiagnosticsErr = p.Engine.Diagnostics(ctx)
	}
	return h, ctx.Err()
}
func (p *Panel) refreshHealth() {
	p.cache.mu.Lock()
	defer p.cache.mu.Unlock()
	ctx := p.cache.ctx
	if ctx == nil || ctx.Err() != nil {
		return
	}
	c := &p.cache.health
	c.mu.Lock()
	if c.flight != nil || (!c.at.IsZero() && time.Since(c.at) < panelSnapshotTTL) {
		c.mu.Unlock()
		return
	}
	c.flight = make(chan struct{})
	generation := c.generation
	c.mu.Unlock()
	p.cache.workers.Add(1)
	go func() { defer p.cache.workers.Done(); _, _ = c.load(ctx, generation, p.loadHealth) }()
}

func (p *Panel) lightStatus() engine.Status {
	if p.Engine == nil {
		return engine.Status{Mode: engine.ModeReadOnly}
	}
	if light, ok := p.Engine.(interface{ LightStatus() engine.Status }); ok {
		s := light.LightStatus()
		p.observeLifecycle(s)
		return s
	}
	// Legacy/fake controllers have no durable status enrichment. The production
	// engine and managed proxy both implement LightStatus explicitly.
	s := p.Engine.Status()
	p.observeLifecycle(s)
	return s
}
