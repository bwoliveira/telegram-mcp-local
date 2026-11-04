package daemon

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

type managedSubscriber struct {
	ch    chan engine.Event
	kinds []engine.EventKind
}

// managedController is a stable capability for the panel, tools and webhooks.
// A terminal gotd generation is joined before explicit login creates another.
// Neither mode nor credentials can be changed through the MCP Engine interface.
type managedController struct {
	op          sync.Mutex
	mu          sync.RWMutex
	current     engine.Controller
	modeFile    string
	factory     engine.Factory
	cfg         engine.Config
	parent      context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	changes     chan struct{}
	subscribers map[uint64]managedSubscriber
	next        uint64
	seq         uint64
}

func newManagedController(ctx context.Context, f engine.Factory, cfg engine.Config, c engine.Controller) *managedController {
	m := &managedController{factory: f, cfg: cfg, parent: ctx, current: c, changes: make(chan struct{}, 1), subscribers: map[uint64]managedSubscriber{}}
	m.start(c)
	return m
}
func (m *managedController) controller() engine.Controller {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}
func (m *managedController) notify() {
	select {
	case m.changes <- struct{}{}:
	default:
	}
}
func (m *managedController) start(c engine.Controller) {
	ctx, cancel := context.WithCancel(m.parent)
	m.cancel = cancel
	m.done = make(chan struct{})
	done := m.done
	events := c.Subscribe(ctx, engine.SubscribeOptions{Buffer: 256})
	go func() {
		bridgeDone := make(chan struct{})
		go func() {
			defer close(bridgeDone)
			changes := c.Changes()
			for {
				select {
				case <-ctx.Done():
					return
				case _, ok := <-changes:
					if !ok {
						changes = nil
					} else {
						m.notify()
					}
				case e, ok := <-events:
					if !ok {
						events = nil
					} else {
						m.publish(e)
					}
				}
			}
		}()
		err := c.Run(ctx)
		if err != nil && ctx.Err() == nil && m.cfg.Logger != nil {
			m.cfg.Logger.Error("account controller stopped", "error", err)
		}
		cancel()
		<-bridgeDone
		m.notify()
		close(done)
	}()
}
func (m *managedController) Run(ctx context.Context) error {
	<-ctx.Done()
	m.op.Lock()
	defer m.op.Unlock()
	m.cancel()
	<-m.done
	return ctx.Err()
}
func (m *managedController) Changes() <-chan struct{} { return m.changes }
func (m *managedController) StartLogin(ctx context.Context, r engine.LoginRequest) error {
	m.op.Lock()
	defer m.op.Unlock()
	if err := m.parent.Err(); err != nil {
		return err
	}
	c := m.controller()
	s := c.Status().State
	if s == engine.StateLoggedOut || s == engine.StateRevoked {
		m.cancel()
		<-m.done
		m.cfg.Mode = c.Mode()
		next, err := m.factory(m.cfg)
		if err != nil {
			return err
		}
		if next == nil {
			return engine.ErrInvalid
		}
		m.mu.Lock()
		m.current = next
		m.mu.Unlock()
		m.start(next)
		c = next
		m.notify()
	}
	if ready, ok := c.(engine.ReadyWaiter); ok {
		waitCtx, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(m.parent, cancel)
		go func(done <-chan struct{}) {
			select {
			case <-done:
				cancel()
			case <-waitCtx.Done():
			}
		}(m.done)
		err := ready.WaitReady(waitCtx)
		stop()
		cancel()
		if err != nil {
			return err
		}
	}
	return c.StartLogin(ctx, r)
}
func (m *managedController) LogOut(ctx context.Context) error {
	m.op.Lock()
	defer m.op.Unlock()
	return m.controller().LogOut(ctx)
}
func (m *managedController) SetMode(ctx context.Context, mode engine.Mode) error {
	m.op.Lock()
	defer m.op.Unlock()
	if mode != engine.ModeReadOnly && mode != engine.ModeReadWrite {
		return engine.ErrInvalid
	}
	if mode == engine.ModeReadOnly {
		if err := m.persistMode(mode); err != nil {
			_ = m.controller().SetMode(context.WithoutCancel(ctx), mode)
			return err
		}
		return m.controller().SetMode(context.WithoutCancel(ctx), mode)
	}
	if err := m.controller().SetMode(ctx, mode); err != nil {
		return err
	}
	if err := m.persistMode(mode); err != nil {
		_ = m.controller().SetMode(context.WithoutCancel(ctx), engine.ModeReadOnly)
		return err
	}
	return nil
}
func (m *managedController) Subscribe(ctx context.Context, o engine.SubscribeOptions) <-chan engine.Event {
	n := o.Buffer
	if n <= 0 {
		n = 64
	}
	if n > 4096 {
		n = 4096
	}
	c := make(chan engine.Event, n)
	m.mu.Lock()
	m.next++
	id := m.next
	m.subscribers[id] = managedSubscriber{c, append([]engine.EventKind(nil), o.Kinds...)}
	m.mu.Unlock()
	go func() { <-ctx.Done(); m.mu.Lock(); delete(m.subscribers, id); close(c); m.mu.Unlock() }()
	return c
}
func (m *managedController) publish(e engine.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	e.Seq = m.seq
	for _, s := range m.subscribers {
		match := len(s.kinds) == 0
		for _, k := range s.kinds {
			if e.Kind == k {
				match = true
			}
		}
		if !match {
			continue
		}
		select {
		case s.ch <- e:
		default:
			select {
			case <-s.ch:
			default:
			}
			select {
			case s.ch <- e:
			default:
			}
		}
	}
}
func (m *managedController) publishTTL(a engine.Applied) {
	for _, k := range a.Deleted {
		p := k.Peer
		now := time.Now()
		m.publish(engine.Event{Kind: engine.EventDelete, ID: fmt.Sprintf("ttl/%s/%s:%d/%d", k.Account, p.Type, p.ID, k.ID), Account: k.Account, OccurredAt: now, ReceivedAt: now, Source: engine.Source("ttl"), Peer: &p, Delete: &engine.Deletion{Peer: &p, IDs: []int{k.ID}}})
	}
}

var _ engine.Controller = (*managedController)(nil)
