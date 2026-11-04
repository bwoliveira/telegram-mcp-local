package fake

import (
	"context"
	"sort"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// localReadLocked validates only the local scope, never network/auth state.
// Callers hold mu, so cancellation is checked after waiting for the mutex too.
func (f *Controller) localReadLocked(ctx context.Context, account engine.AccountID) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if account == "" || account != f.cfg.Account {
		return engine.ErrAccountMismatch
	}
	return nil
}

// ReadPeers returns every seen peer, including noncontacts and lost-access peers.
func (f *Controller) ReadPeers(ctx context.Context, account engine.AccountID) ([]engine.Peer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.localReadLocked(ctx, account); err != nil {
		return nil, err
	}
	out := make([]engine.Peer, 0, len(f.peers))
	for _, p := range f.peers {
		out = append(out, copyOf(p))
	}
	sort.Slice(out, func(i, j int) bool { return peerText(out[i].Ref) < peerText(out[j].Ref) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadDialogs returns locally retained counters without refreshing Telegram.
func (f *Controller) ReadDialogs(ctx context.Context, account engine.AccountID) ([]engine.Dialog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.localReadLocked(ctx, account); err != nil {
		return nil, err
	}
	out := make([]engine.Dialog, 0, len(f.dialogs))
	for _, d := range f.dialogs {
		out = append(out, copyOf(d))
	}
	sort.Slice(out, func(i, j int) bool { return peerText(out[i].Peer) < peerText(out[j].Peer) })
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadMessages applies TTL at the read boundary, not just during Publish.
func (f *Controller) ReadMessages(ctx context.Context, account engine.AccountID) ([]engine.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.localReadLocked(ctx, account); err != nil {
		return nil, err
	}
	now := f.clock()
	out := make([]engine.Message, 0, len(f.messages))
	for key, m := range f.messages {
		if key.Account != account || m.Key.Account != account || f.tombstones[key] ||
			(key.Peer.Type != engine.PeerChannel && f.pendingDeletes[key.ID]) || expired(m, now) {
			continue
		}
		out = append(out, copyOf(m))
	}
	sortMessages(out)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadGroup returns a detached stored snapshot, even after access is lost.
func (f *Controller) ReadGroup(ctx context.Context, account engine.AccountID, peer engine.PeerRef) (engine.Group, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.localReadLocked(ctx, account); err != nil {
		return engine.Group{}, err
	}
	if !validLocalPeer(peer) {
		return engine.Group{}, engine.ErrInvalid
	}
	p, ok := f.peers[peer]
	if !ok {
		return engine.Group{}, engine.ErrPeerNotFound
	}
	if p.Kind != engine.KindGroup && p.Kind != engine.KindSupergroup && p.Kind != engine.KindChannel {
		return engine.Group{}, engine.ErrInvalid
	}
	g, ok := f.groups[peer]
	if !ok {
		return engine.Group{}, engine.ErrPeerNotFound
	}
	g.Peer = p // Keep the retained snapshot's access marker up to date.
	out := copyOf(g)
	if err := ctx.Err(); err != nil {
		return engine.Group{}, err
	}
	return out, nil
}

func validLocalPeer(peer engine.PeerRef) bool {
	return peer.ID > 0 && (peer.Type == engine.PeerUser || peer.Type == engine.PeerChat || peer.Type == engine.PeerChannel)
}

// localCoverageReader avoids an import of the consumer package. An attached
// store can prove gap-free coverage; the standalone mirror cannot, because
// applyLocked emits GapMarks but does not retain them or FilledGaps. A complete
// backfill cursor alone must therefore never make the standalone fake complete.
type localCoverageReader interface {
	ReadCoverage(context.Context, engine.AccountID, *engine.PeerRef) (bool, *time.Time, error)
}

// ReadCoverage reports only proven coverage. Unknown is deliberately false.
func (f *Controller) ReadCoverage(ctx context.Context, account engine.AccountID, peer *engine.PeerRef) (complete bool, oldest *time.Time, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.localReadLocked(ctx, account); err != nil {
		return false, nil, err
	}
	if peer != nil && !validLocalPeer(*peer) {
		return false, nil, engine.ErrInvalid
	}
	if store, ok := f.cfg.Store.(localCoverageReader); ok {
		complete, oldest, err = store.ReadCoverage(ctx, account, copyOf(peer))
		if err != nil {
			return false, nil, err
		}
		if err := ctx.Err(); err != nil {
			return false, nil, err
		}
		return complete, copyOf(oldest), nil
	}
	if peer != nil {
		if c, ok := f.cursors[*peer]; ok && c.OldestDate != nil && !c.OldestDate.IsZero() {
			oldest = copyOf(c.OldestDate)
		}
	} else {
		// Account coverage starts no earlier than its least-covered known peer.
		// Include every observed namespace, even one not present in dialogs.
		refs := make(map[engine.PeerRef]bool)
		for ref := range f.peers {
			refs[ref] = true
		}
		for ref := range f.dialogs {
			refs[ref] = true
		}
		for ref := range f.cursors {
			refs[ref] = true
		}
		for k := range f.messages {
			if k.Account == account {
				refs[k.Peer] = true
			}
		}
		for ref := range refs {
			c, ok := f.cursors[ref]
			if !ok || c.OldestDate == nil || c.OldestDate.IsZero() {
				oldest = nil
				break
			}
			if oldest == nil || c.OldestDate.After(*oldest) {
				oldest = copyOf(c.OldestDate)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return false, nil, err
	}
	return false, oldest, nil
}
