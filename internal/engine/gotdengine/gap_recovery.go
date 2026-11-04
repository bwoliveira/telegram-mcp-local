package gotdengine

import (
	"context"
	"fmt"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

type pendingGapReader interface {
	PendingGaps(context.Context, engine.AccountID, *engine.PeerRef) ([]engine.GapMark, error)
}

type differenceCheckpointStore interface {
	SaveDifferenceCheckpoint(context.Context, engine.AccountID, engine.DifferenceCheckpoint) (engine.Applied, error)
}

type differenceProof struct {
	gaps    []engine.GapMark
	from    engine.UpdatesState
	state   engine.UpdatesState
	channel int64
	final   bool
}

// Only an authoritative FINAL difference whose data crossed the response
// barrier AND whose checkpoint was saved can resolve lost update state. A
// history head scan cannot prove missed edits/deletes or account-wide coverage.
// Access loss is terminal/inaccessible evidence, not a retryable update hole.
func (e *Engine) recoveryGaps(ctx context.Context, p *engine.PeerRef) ([]engine.GapMark, error) {
	r, ok := e.cfg.Store.(pendingGapReader)
	if !ok {
		return nil, nil
	}
	gaps, err := r.PendingGaps(ctx, e.cfg.Account, p)
	if err != nil {
		return nil, err
	}
	var result []engine.GapMark
	for _, g := range gaps {
		// A too-long response discards part of the old update stream. Reaching its
		// new head later does not prove the discarded interval was reconstructed.
		if g.Reason == engine.GapDifferenceTooLong || g.Reason == engine.GapChannelTooLong {
			return nil, nil
		}
		if g.Recovery != engine.GapTerminal && g.Reason == engine.GapStateLost && g.RecoveryAnchor != nil && g.RecoveryCursor != nil {
			result = append(result, g)
		}
	}
	return result, nil
}

func (e *Engine) stageDifferenceProof(p *differenceProof) {
	e.commitMu.Lock()
	defer e.commitMu.Unlock()
	if e.held.Load() {
		return
	}
	if p.channel == 0 {
		e.globalProof = p
		return
	}
	if e.channelProofs == nil {
		e.channelProofs = make(map[int64]*differenceProof)
	}
	e.channelProofs[p.channel] = p
}

// Called under commitMu. The store transaction commits checkpoint AND proof
// progress/fill together; a failed commit leaves both available for safe replay.
func (e *Engine) finishDifferenceProof(ctx context.Context, p *differenceProof, ap engine.Applied) error {
	if p == nil {
		return nil
	}
	filled := e.now()
	for _, g := range ap.Gaps {
		g := g
		e.emit(engine.Event{Kind: engine.EventCoverage, ID: fingerprint(fmt.Sprintf("%s|%v|%s|%s|filled", e.cfg.Account, g.Peer, g.Reason, g.Detected)), Account: e.cfg.Account, Coverage: &g, Peer: g.Peer, ReceivedAt: filled, OccurredAt: filled, Source: engine.SourceDifference, CatchUp: true})
	}
	if !p.final {
		e.notify()
		return nil
	}
	// Only the final persisted global difference ends startup/reconnect recovery;
	// an arbitrary peer/hash/message batch must not announce readiness early.
	if p.channel == 0 {
		e.freshAccount.Store(false)
		if e.recovering.Swap(false) {
			e.transition(engine.StateConnected)
		}
	}
	e.notify()
	return nil
}

// recoverChannelHash uses only a known public username and verifies its current
// typed identity. Usernames can be reassigned: never attach another channel's
// hash to the stored ID, accept min hashes, or guess credentials with hash=0.
func (e *Engine) recoverChannelHash(ctx context.Context, id int64) (int64, bool, error) {
	p := engine.PeerRef{Type: engine.PeerChannel, ID: id}
	peer, ok, err := e.cfg.Store.LookupPeer(ctx, e.cfg.Account, p)
	if err != nil || !ok || peer.Username == "" {
		return 0, false, err
	}
	if err := e.require(); err != nil {
		return 0, false, err
	}
	got, err := e.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: peer.Username})
	if err != nil {
		if tgerr.Is(err, "USERNAME_NOT_OCCUPIED", "USERNAME_INVALID", "CHANNEL_PRIVATE") {
			return 0, false, nil // definitive identity failure: preserve inaccessible evidence
		}
		// A transient network/FloodWait/cancellation error is not proof of
		// access loss; the manager must retry without dropping channel PTS.
		return 0, false, err
	}
	if peerRef(got.Peer) != p {
		return 0, false, nil
	}
	for _, raw := range got.Chats {
		ch, ok := raw.(*tg.Channel)
		if !ok || ch.ID != id || ch.Min || ch.AccessHash == 0 {
			continue
		}
		if _, err := e.apply(ctx, engine.Batch{Account: e.cfg.Account, Source: engine.SourceRefresh, Peers: peerUpserts(got.Users, got.Chats)}); err != nil {
			return 0, false, err
		}
		return e.cfg.Store.AccessHash(ctx, e.cfg.Account, p)
	}
	return 0, false, nil
}
