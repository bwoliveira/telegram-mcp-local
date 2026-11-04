package gotdengine

import (
	"context"
	"sort"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

// appendMissingDialogs runs under commitMu so a concurrent dialog refresh
// cannot be overwritten with a synthetic zero-counter snapshot. Service-only
// ChatCreate/ChannelCreate updates open conversations even without content rows.
func (e *Engine) appendMissingDialogs(ctx context.Context, b *engine.Batch, updates []tg.UpdateClass) error {
	tops := map[engine.PeerRef]int{}
	titles := map[engine.PeerRef]string{}
	channelUpdates := map[engine.PeerRef]bool{}
	for _, raw := range updates {
		var message tg.MessageClass
		switch u := raw.(type) {
		case *tg.UpdateNewMessage:
			message = u.Message
		case *tg.UpdateNewChannelMessage:
			message = u.Message
		case *tg.UpdateChannel:
			if u.ChannelID > 0 {
				channelUpdates[engine.PeerRef{Type: engine.PeerChannel, ID: u.ChannelID}] = true
			}
		}
		if message == nil {
			continue
		}
		var ref engine.PeerRef
		switch m := message.(type) {
		case *tg.Message:
			ref = peerRef(m.PeerID)
		case *tg.MessageService:
			ref = peerRef(m.PeerID)
			switch a := m.Action.(type) {
			case *tg.MessageActionChatCreate:
				titles[ref] = a.Title
			case *tg.MessageActionChannelCreate:
				titles[ref] = a.Title
			}
		}
		if (ref.Type == engine.PeerChat || ref.Type == engine.PeerChannel) && ref.ID > 0 && message.GetID() > 0 {
			tops[ref] = max(tops[ref], message.GetID())
		}
	}
	// updateChannel can announce an empty channel. Only supplied metadata proves
	// membership; unknown/left channels are reconciled by the dialog refresh.
	supplied := map[engine.PeerRef]engine.Peer{}
	for _, p := range b.Peers {
		supplied[p.Peer.Ref] = p.Peer
	}
	for ref := range channelUpdates {
		if p, ok := supplied[ref]; ok && p.Access == engine.AccessOK {
			if _, ok := tops[ref]; !ok {
				tops[ref] = 0
			}
		}
	}
	if len(tops) == 0 {
		return nil
	}
	var dialogs []engine.Dialog
	var err error
	switch s := e.cfg.Store.(type) {
	case interface {
		ReadDialogs(context.Context, engine.AccountID) ([]engine.Dialog, error)
	}:
		dialogs, err = s.ReadDialogs(ctx, b.Account)
	case interface {
		Dialogs(context.Context, engine.AccountID) ([]engine.Dialog, error)
	}:
		dialogs, err = s.Dialogs(ctx, b.Account)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	known := map[engine.PeerRef]bool{}
	for _, d := range dialogs {
		known[d.Peer] = true
	}
	for _, d := range b.Dialogs {
		known[d.Peer] = true
	}
	refs := make([]engine.PeerRef, 0, len(tops))
	for ref := range tops {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Type != refs[j].Type {
			return refs[i].Type < refs[j].Type
		}
		return refs[i].ID < refs[j].ID
	})
	for _, ref := range refs {
		if !known[ref] {
			b.Dialogs = append(b.Dialogs, engine.Dialog{Peer: ref, TopMessageID: tops[ref]})
		}
		if _, ok := supplied[ref]; ok || titles[ref] == "" {
			continue
		}
		p, ok, err := e.cfg.Store.LookupPeer(ctx, b.Account, ref)
		if err != nil {
			return err
		}
		if ok && p.Title != "" {
			continue
		}
		if !ok {
			kind := engine.KindGroup
			if ref.Type == engine.PeerChannel {
				kind = engine.KindChannel
			}
			p = engine.Peer{Ref: ref, Kind: kind, Access: engine.AccessOK}
		}
		p.Title = titles[ref]
		b.Peers = append(b.Peers, engine.PeerUpsert{Peer: p})
	}
	return nil
}
