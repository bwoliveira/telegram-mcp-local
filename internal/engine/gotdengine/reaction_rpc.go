package gotdengine

import (
	"context"
	"fmt"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

// reconcileMinimalReactionResult obtains an authoritative snapshot unless the
// reaction RPC carried a complete, target-scoped reaction update. An omitted
// update is no more authoritative than a minimal one; ordinary message copies
// deliberately cannot clear known reactions. Never infer counts from the request
// or erase other users' reactions from a partial acknowledgment.
func (e *Engine) reconcileMinimalReactionResult(ctx context.Context, raw tg.UpdatesClass, key engine.MessageKey, ip tg.InputPeerClass) error {
	var us []tg.UpdateClass
	switch u := raw.(type) {
	case *tg.Updates:
		us = u.Updates
	case *tg.UpdatesCombined:
		us = u.Updates
	case *tg.UpdateShort:
		us = []tg.UpdateClass{u.Update}
	}
	complete := false
	for _, u := range us {
		if r, ok := u.(*tg.UpdateMessageReactions); ok && peerRef(r.Peer) == key.Peer && r.MsgID == key.ID {
			// Last matching update wins, as it does during ingestion. A later
			// partial update still requires reconciliation.
			complete = !r.Reactions.Min
		}
	}
	if complete {
		return nil
	}
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: key.ID}}
	var got tg.MessagesMessagesClass
	var err error
	if ch, ok := ip.(*tg.InputPeerChannel); ok {
		got, err = e.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash}, ID: ids})
	} else {
		got, err = e.api.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return rpcError(err)
	}
	messages, users, chats := messagePage(got)
	for _, v := range messages {
		m, ok := v.(*tg.Message)
		if !ok || m.ID != key.ID || peerRef(m.PeerID) != key.Peer {
			continue
		}
		r, has := m.GetReactions()
		// In a full message, absence of the optional reactions object means none.
		if !has {
			r = tg.MessageReactions{}
		}
		// This targeted read is authoritative for message lifecycle and peers,
		// not just counts. Commit all evidence together so subscribers cannot
		// observe new reactions while protection/expiry is still stale.
		b := engine.Batch{Account: e.cfg.Account, Source: engine.SourceSend, Peers: peerUpserts(users, chats)}
		appendMessage(&b, m, false, engine.PeerRef{})
		for i := range b.Messages {
			// Apply the explicit snapshot exactly once below. In particular, a
			// still-minimal result must not hydrate guessed reaction state.
			b.Messages[i].Reactions = nil
			b.Messages[i].ReactionSnapshot = nil
		}
		if !r.Min {
			b.Reactions = []engine.ReactionUpdate{convertReactions(key, r)}
		}
		if _, err := e.apply(ctx, b); err != nil {
			return err
		}
		if r.Min {
			return fmt.Errorf("%w: reaction snapshot remains partial", engine.ErrNotSupported)
		}
		return nil
	}
	return engine.ErrMessageNotFound
}
