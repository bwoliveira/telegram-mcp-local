package gotdengine

import (
	"context"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

// mediaContext also covers filesystem work between RPCs: a paused/revoked
// transport must not publish a completed download after its lifetime ends.
func (e *Engine) mediaContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	e.mu.Lock()
	active := e.active
	e.mu.Unlock()
	if active == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(active, cancel)
	if active.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

func (e *Engine) checkMedia(ctx context.Context, key engine.MessageKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.require(); err != nil {
		return err
	}
	if key.Account != e.cfg.Account {
		return engine.ErrAccountMismatch
	}
	if key.ID <= 0 || key.Peer.ID <= 0 || (key.Peer.Type != engine.PeerUser && key.Peer.Type != engine.PeerChat && key.Peer.Type != engine.PeerChannel) {
		return engine.ErrInvalid
	}
	p, ok, err := e.cfg.Store.LookupPeer(ctx, key.Account, key.Peer)
	if err != nil {
		return err
	}
	if ok && p.Protected {
		return engine.ErrProtectedContent
	}
	m, ok, err := e.cfg.Store.Message(ctx, key)
	if err != nil {
		return err
	}
	if !ok {
		return engine.ErrMessageNotFound
	}
	if m.Protected || (m.Media != nil && m.Media.TTLSeconds > 0) || (m.ExpiresAt != nil && !m.ExpiresAt.After(e.now())) || (m.TTLPeriod > 0 && !m.Date.Add(time.Duration(m.TTLPeriod)*time.Second).After(e.now())) {
		return engine.ErrProtectedContent
	}
	if m.Media == nil {
		return engine.ErrNotSupported
	}
	return nil
}

func (e *Engine) checkRawMedia(m *tg.Message) error {
	if m.Noforwards || (m.TTLPeriod > 0 && !time.Unix(int64(m.Date), 0).Add(time.Duration(m.TTLPeriod)*time.Second).After(e.now())) {
		return engine.ErrProtectedContent
	}
	switch media := m.Media.(type) {
	case *tg.MessageMediaPhoto:
		if media.TTLSeconds > 0 || expiredMedia(media) {
			return engine.ErrProtectedContent
		}
	case *tg.MessageMediaDocument:
		if media.TTLSeconds > 0 || expiredMedia(media) {
			return engine.ErrProtectedContent
		}
	default:
		return engine.ErrNotSupported
	}
	return nil
}

// Re-fetch by exact peer/id, never via history or readHistory. In particular,
// non-channel message IDs are account-wide, so validate the returned peer too.
func (e *Engine) fetchMediaMessage(ctx context.Context, key engine.MessageKey) (*tg.Message, error) {
	if err := e.checkMedia(ctx, key); err != nil {
		return nil, err
	}
	var got tg.MessagesMessagesClass
	var err error
	ids := []tg.InputMessageClass{&tg.InputMessageID{ID: key.ID}}
	if key.Peer.Type == engine.PeerChannel {
		ip, peerErr := e.inputPeer(ctx, key.Peer)
		if peerErr != nil {
			return nil, peerErr
		}
		p, ok := ip.(*tg.InputPeerChannel)
		if !ok {
			return nil, engine.ErrInvalid
		}
		got, err = e.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash}, ID: ids})
	} else {
		got, err = e.api.MessagesGetMessages(ctx, ids)
	}
	if err != nil {
		return nil, rpcError(err)
	}
	messages, users, chats := messagePage(got)
	for _, p := range peerUpserts(users, chats) {
		if p.Peer.Ref == key.Peer && p.Peer.Protected {
			return nil, engine.ErrProtectedContent
		}
	}
	for _, raw := range messages {
		m, ok := raw.(*tg.Message)
		if !ok || m.ID != key.ID || peerRef(m.PeerID) != key.Peer {
			continue
		}
		if err := e.checkRawMedia(m); err != nil {
			return nil, err
		}
		return m, nil
	}
	return nil, engine.ErrMessageNotFound
}
