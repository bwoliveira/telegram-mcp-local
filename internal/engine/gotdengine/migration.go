package gotdengine

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// A migration is a peer link, not a deletion: old message keys must survive and
// history queries can merge them with the replacement channel.
func appendMigration(b *engine.Batch, chatID, channelID int64) {
	if chatID <= 0 || channelID <= 0 {
		return
	}
	next := engine.PeerRef{Type: engine.PeerChannel, ID: channelID}
	b.Peers = append(b.Peers, engine.PeerUpsert{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChat, ID: chatID}, Kind: engine.KindGroup, MigratedTo: &next}})
}

func appendMessageMigration(b *engine.Batch, raw tg.MessageClass) {
	m, ok := raw.(*tg.MessageService)
	if !ok {
		return
	}
	switch a := m.Action.(type) {
	case *tg.MessageActionChatMigrateTo:
		if p, ok := m.PeerID.(*tg.PeerChat); ok {
			appendMigration(b, p.ChatID, a.ChannelID)
		}
	case *tg.MessageActionChannelMigrateFrom:
		if p, ok := m.PeerID.(*tg.PeerChannel); ok {
			appendMigration(b, a.ChatID, p.ChannelID)
		}
	}
}

func appendFullChatMigration(b *engine.Batch, full tg.ChatFullClass) {
	if c, ok := full.(*tg.ChannelFull); ok {
		appendMigration(b, c.MigratedFromChatID, c.ID)
	}
}

type migrationDiscoveryCacheKey struct{}
type migrationDiscoveryBudgetKey struct{}
type migrationDiscoveryDeferredKey struct{}

const migrationProbeLimit = 100

type migrationDiscoveryState struct {
	checked map[engine.PeerRef]bool
	after   int64
	// The serial worker keeps this RPC-specific deadline across dialog refreshes.
	// A full-channel FloodWait must not delay messages.getDialogs.
	retryAt time.Time
}

// migrationPeers prefers the account-scoped persisted candidate query, including
// peers-only basic groups, while retaining compatibility with older persisters.
func (e *Engine) migrationPeers(ctx context.Context) ([]engine.Peer, error) {
	if s, ok := e.cfg.Store.(interface {
		ReadMigrationPeers(context.Context, engine.AccountID) ([]engine.Peer, error)
	}); ok {
		return s.ReadMigrationPeers(ctx, e.cfg.Account)
	}
	var dialogs []engine.Dialog
	var err error
	switch s := e.cfg.Store.(type) {
	case interface {
		ReadDialogs(context.Context, engine.AccountID) ([]engine.Dialog, error)
	}:
		dialogs, err = s.ReadDialogs(ctx, e.cfg.Account)
	case interface {
		Dialogs(context.Context, engine.AccountID) ([]engine.Dialog, error)
	}:
		dialogs, err = s.Dialogs(ctx, e.cfg.Account)
	default:
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var peers []engine.Peer
	for _, d := range dialogs {
		if d.Peer.Type != engine.PeerChat && d.Peer.Type != engine.PeerChannel {
			continue
		}
		p, ok, err := e.cfg.Store.LookupPeer(ctx, e.cfg.Account, d.Peer)
		if err != nil {
			return nil, err
		}
		if ok {
			peers = append(peers, p)
		}
	}
	return peers, nil
}

// Inspect indexed joined supergroups too: the worker's recent dialog page is
// not an exhaustive membership inventory. Local candidate scans never trigger
// pagination or history RPCs, and full metadata probes share a per-refresh cap.
func (e *Engine) discoverLegacyMigrations(ctx context.Context, chats []tg.ChatClass) error {
	if deferred, _ := ctx.Value(migrationDiscoveryDeferredKey{}).(bool); deferred {
		return nil
	}
	state, _ := ctx.Value(migrationDiscoveryCacheKey{}).(*migrationDiscoveryState)
	if state == nil {
		state = &migrationDiscoveryState{checked: make(map[engine.PeerRef]bool)}
	}
	if e.now().Before(state.retryAt) {
		return nil
	}
	budget, _ := ctx.Value(migrationDiscoveryBudgetKey{}).(*int)
	if budget == nil {
		n := migrationProbeLimit
		budget = &n
	}
	peers, err := e.migrationPeers(ctx)
	if err != nil {
		return err
	}
	unlinked := map[engine.PeerRef]bool{}
	linked := map[engine.PeerRef]bool{}
	var candidates []engine.PeerRef
	for _, p := range peers {
		if p.Ref.Type == engine.PeerChat {
			if p.MigratedTo != nil {
				linked[*p.MigratedTo] = true
			} else {
				unlinked[p.Ref] = true
			}
		} else if p.Ref.Type == engine.PeerChannel && p.Kind == engine.KindSupergroup && p.Access == engine.AccessOK {
			candidates = append(candidates, p.Ref)
		}
	}
	// A newer min/left constructor on this page cannot authorize a full read
	// merely because an older usable access hash remains in the index.
	excluded := map[engine.PeerRef]bool{}
	for _, raw := range chats {
		if c, ok := raw.(*tg.Channel); ok && (c.Min || c.Left || !c.Megagroup) {
			excluded[engine.PeerRef{Type: engine.PeerChannel, ID: c.ID}] = true
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
	start := sort.Search(len(candidates), func(i int) bool { return candidates[i].ID > state.after })
	for i := 0; i < len(candidates) && len(unlinked) > 0 && *budget > 0; i++ {
		ref := candidates[(start+i)%len(candidates)]
		if linked[ref] || state.checked[ref] || excluded[ref] {
			continue
		}
		state.after = ref.ID
		h, ok, err := e.cfg.Store.AccessHash(ctx, e.cfg.Account, ref)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		*budget--
		got, err := e.api.ChannelsGetFullChannel(ctx, &tg.InputChannel{ChannelID: ref.ID, AccessHash: h})
		if err != nil {
			// Commit definitive access loss before rotating past stale memberships.
			// A failed commit must stop discovery and preserve its checkpoint hold.
			if saveErr := e.persistMetadataAccessLoss(ctx, ref, err); saveErr != nil {
				return saveErr
			}
			// CHANNEL_INVALID remains retryable metadata evidence, not access loss.
			// Rotate so >100 stale memberships cannot starve later candidates.
			if tgerr.Is(err, "CHANNEL_PRIVATE", "CHANNEL_INVALID", "CHAT_FORBIDDEN", "USER_BANNED_IN_CHANNEL") {
				continue
			}
			err = rpcError(err)
			var flood *engine.FloodWaitError
			if errors.As(err, &flood) {
				flood.Method = "channels.getFullChannel"
				state.retryAt = flood.Until
			}
			return err
		}
		if got == nil {
			return engine.ErrInvalid
		}
		full, ok := got.FullChat.(*tg.ChannelFull)
		if !ok || full.ID != ref.ID {
			return engine.ErrInvalid
		}
		batch := engine.Batch{Account: e.cfg.Account, Source: engine.SourceRefresh, Peers: peerUpserts(got.Users, got.Chats)}
		appendFullChatMigration(&batch, full)
		if _, err := e.apply(ctx, batch); err != nil {
			return err
		}
		state.checked[ref] = true
		delete(unlinked, engine.PeerRef{Type: engine.PeerChat, ID: full.MigratedFromChatID})
		linked[ref] = true
	}
	return nil
}
