package webhook

import (
	"sync"
	"time"
)

// Limiter owns webhook resources that are shared by every account in one
// daemon process. A standalone Manager gets its own limiter for compatibility;
// the daemon injects one limiter into all account managers.
type Limiter struct {
	mu sync.Mutex
	// startMu serializes Manager.Start across accounts. Startup may disable a
	// newer hook owned by another manager, which takes that manager's configMu;
	// serializing the reconciliations prevents two of them from waiting on each
	// other. Lock order: startMu, then any configMu, then mu.
	startMu sync.Mutex

	maxEnabled     int
	enabled        map[*Manager]map[string]time.Time
	maxQueuedBytes int64
	queuedBytes    int64
	dialSem        chan struct{}
}

// NewLimiter creates the process-wide production limits for webhook managers.
func NewLimiter() *Limiter {
	return newLimiter(maxEnabledWebhooks, maxQueuedWebhookBytes, maxConcurrentWebhookDials)
}

func newLimiter(maxEnabled int, maxQueuedBytes int64, maxDials int) *Limiter {
	return &Limiter{
		maxEnabled:     maxEnabled,
		enabled:        make(map[*Manager]map[string]time.Time),
		maxQueuedBytes: maxQueuedBytes,
		dialSem:        make(chan struct{}, maxDials),
	}
}

// hookOlder reports whether hook a precedes hook b in the process-wide
// retention order: creation time, then ID. The order is a property of the
// stored hooks and does not depend on which account starts first.
func hookOlder(aCreated time.Time, aID string, bCreated time.Time, bID string) bool {
	if !aCreated.Equal(bCreated) {
		return aCreated.Before(bCreated)
	}
	return aID < bID
}

func (l *Limiter) reserveHook(m *Manager, id string, created time.Time) bool {
	ok, _, _ := l.reserveOrNewest(m, id, created, false)
	return ok
}

// reserveOrNewest reserves the hook when there is room. When the limit is
// full and preempt is set, it returns the newest reservation held by another
// manager if the candidate is older than it; the caller disables that hook and
// retries. Exact ties keep the reservation already held.
func (l *Limiter) reserveOrNewest(m *Manager, id string, created time.Time, preempt bool) (bool, *Manager, string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	owned := l.enabled[m]
	if _, ok := owned[id]; ok {
		return true, nil, ""
	}
	total := 0
	for _, hooks := range l.enabled {
		total += len(hooks)
	}
	if total < l.maxEnabled {
		if owned == nil {
			owned = make(map[string]time.Time)
			l.enabled[m] = owned
		}
		owned[id] = created
		return true, nil, ""
	}
	if !preempt {
		return false, nil, ""
	}
	var victim *Manager
	var victimID string
	var victimCreated time.Time
	for owner, hooks := range l.enabled {
		if owner == m {
			continue
		}
		for hookID, hookCreated := range hooks {
			if victim == nil || hookOlder(victimCreated, victimID, hookCreated, hookID) {
				victim, victimID, victimCreated = owner, hookID, hookCreated
			}
		}
	}
	if victim == nil || !hookOlder(created, id, victimCreated, victimID) {
		return false, nil, ""
	}
	return false, victim, victimID
}

func (l *Limiter) hasHook(m *Manager, id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.enabled[m][id]
	return ok
}

func (l *Limiter) releaseHook(m *Manager, id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	owned := l.enabled[m]
	delete(owned, id)
	if len(owned) == 0 {
		delete(l.enabled, m)
	}
}

func (l *Limiter) releaseHooks(m *Manager) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.enabled, m)
}

// replaceQueued atomically replaces oldBytes with newBytes in the aggregate
// budget. oldBytes is zero when adding a delivery without evicting one.
func (l *Limiter) replaceQueued(oldBytes, newBytes int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if oldBytes < 0 || newBytes < 0 || oldBytes > l.queuedBytes {
		panic("webhook: global queued byte accounting underflow")
	}
	projected := l.queuedBytes - oldBytes + newBytes
	if newBytes > l.maxQueuedBytes || projected > l.maxQueuedBytes {
		return false
	}
	l.queuedBytes = projected
	return true
}

func (l *Limiter) releaseQueued(size int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if size < 0 || size > l.queuedBytes {
		panic("webhook: global queued byte accounting underflow")
	}
	l.queuedBytes -= size
}
