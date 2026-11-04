package fake

import (
	"context"
	"runtime"
	"sync"
	"time"
	"weak"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

type triageStore interface {
	ReadTriageMarks(context.Context, engine.AccountID) (map[engine.PeerRef]store.TriageMark, error)
	MarkTriageHandled(context.Context, engine.AccountID, engine.PeerRef, string, time.Time) error
	SnoozeTriage(context.Context, engine.AccountID, engine.PeerRef, string, time.Time, time.Time) error
	ClearTriageMark(context.Context, engine.AccountID, engine.PeerRef) error
}

// Values contain no Controller pointers. Each value is protected by its owner's
// mu; the weak key and cleanup let standalone state die with the Controller.
var triageRegistry sync.Map // weak.Pointer[Controller] -> map[engine.PeerRef]store.TriageMark

func (f *Controller) triageMarksLocked() map[engine.PeerRef]store.TriageMark {
	key := weak.Make(f)
	if value, ok := triageRegistry.Load(key); ok {
		return value.(map[engine.PeerRef]store.TriageMark)
	}
	marks := make(map[engine.PeerRef]store.TriageMark)
	triageRegistry.Store(key, marks)
	runtime.AddCleanup(f, func(key weak.Pointer[Controller]) { triageRegistry.Delete(key) }, key)
	return marks
}

func (f *Controller) triageScopeLocked(ctx context.Context, account engine.AccountID, peer *engine.PeerRef) error {
	if ctx == nil {
		return engine.ErrInvalid
	}
	if err := f.localReadLocked(ctx, account); err != nil {
		return err
	}
	if peer != nil {
		if !validLocalPeer(*peer) {
			return engine.ErrInvalid
		}
		if _, exists := f.peers[*peer]; !exists {
			return engine.ErrPeerNotFound
		}
	}
	return nil
}

func cloneTriageMarks(marks map[engine.PeerRef]store.TriageMark) map[engine.PeerRef]store.TriageMark {
	out := make(map[engine.PeerRef]store.TriageMark, len(marks))
	for peer, mark := range marks {
		if mark.HandledAt != nil {
			at := *mark.HandledAt
			mark.HandledAt = &at
		}
		if mark.SnoozedAt != nil {
			at := *mark.SnoozedAt
			mark.SnoozedAt = &at
		}
		if mark.SnoozeUntil != nil {
			until := *mark.SnoozeUntil
			mark.SnoozeUntil = &until
		}
		out[peer] = mark
	}
	return out
}

// ReadTriageMarks returns detached local decisions without consulting Telegram.
func (f *Controller) ReadTriageMarks(ctx context.Context, account engine.AccountID) (map[engine.PeerRef]store.TriageMark, error) {
	f.mu.Lock()
	if err := f.triageScopeLocked(ctx, account, nil); err != nil {
		f.mu.Unlock()
		return nil, err
	}
	if durable, ok := f.cfg.Store.(triageStore); ok {
		f.mu.Unlock()
		marks, err := durable.ReadTriageMarks(ctx, account)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return cloneTriageMarks(marks), nil
	}
	defer f.mu.Unlock()
	return cloneTriageMarks(f.triageMarksLocked()), nil
}

// MarkTriageHandled records a local decision, never a Telegram write.
func (f *Controller) MarkTriageHandled(ctx context.Context, account engine.AccountID, peer engine.PeerRef, note string, at time.Time) error {
	f.mu.Lock()
	if err := f.triageScopeLocked(ctx, account, &peer); err != nil {
		f.mu.Unlock()
		return err
	}
	if durable, ok := f.cfg.Store.(triageStore); ok {
		f.mu.Unlock()
		return durable.MarkTriageHandled(ctx, account, peer, note, at)
	}
	defer f.mu.Unlock()
	marks := f.triageMarksLocked()
	if note == "" {
		note = marks[peer].Note
	}
	at = at.UTC()
	marks[peer] = store.TriageMark{Peer: peer, HandledAt: &at, Note: note}
	return nil
}

// SnoozeTriage replaces a handled decision, retaining an exactly empty note.
func (f *Controller) SnoozeTriage(ctx context.Context, account engine.AccountID, peer engine.PeerRef, note string, at, until time.Time) error {
	f.mu.Lock()
	if err := f.triageScopeLocked(ctx, account, &peer); err != nil {
		f.mu.Unlock()
		return err
	}
	if durable, ok := f.cfg.Store.(triageStore); ok {
		f.mu.Unlock()
		return durable.SnoozeTriage(ctx, account, peer, note, at, until)
	}
	defer f.mu.Unlock()
	marks := f.triageMarksLocked()
	if note == "" {
		note = marks[peer].Note
	}
	at, until = at.UTC(), until.UTC()
	marks[peer] = store.TriageMark{Peer: peer, SnoozedAt: &at, SnoozeUntil: &until, Note: note}
	return nil
}

// ClearTriageMark forgets only this peer's local decision.
func (f *Controller) ClearTriageMark(ctx context.Context, account engine.AccountID, peer engine.PeerRef) error {
	f.mu.Lock()
	if err := f.triageScopeLocked(ctx, account, &peer); err != nil {
		f.mu.Unlock()
		return err
	}
	if durable, ok := f.cfg.Store.(triageStore); ok {
		f.mu.Unlock()
		return durable.ClearTriageMark(ctx, account, peer)
	}
	defer f.mu.Unlock()
	delete(f.triageMarksLocked(), peer)
	return nil
}
