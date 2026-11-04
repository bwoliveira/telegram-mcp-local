package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
	"strconv"
	"strings"
	"time"
)

func (e *Engine) Resolve(ctx context.Context, r engine.ResolveRequest) ([]engine.Candidate, error) {
	var peers []engine.Peer
	// Preserve rejection of unknown typed numeric identities without treating
	// every colon in a human title as a peer-token separator.
	if kind, id, ok := strings.Cut(r.Query, ":"); ok && !strings.ContainsAny(kind, " 	\r\n") {
		if _, err := strconv.ParseInt(id, 10, 64); err == nil && kind != "user" && kind != "chat" && kind != "channel" {
			return nil, engine.ErrInvalid
		}
	}
	if r.Query == "me" {
		id, err := e.cfg.Store.SelfID(ctx, e.cfg.Account)
		if err != nil {
			return nil, err
		}
		if id > 0 {
			p, ok, err := e.cfg.Store.LookupPeer(ctx, e.cfg.Account, engine.PeerRef{Type: engine.PeerUser, ID: id})
			if err != nil {
				return nil, err
			}
			if ok {
				peers = append(peers, p)
			}
		}
	} else if strings.HasPrefix(r.Query, "user:") || strings.HasPrefix(r.Query, "chat:") || strings.HasPrefix(r.Query, "channel:") {
		var kind string
		parts := strings.Split(r.Query, ":")
		if len(parts) != 2 {
			return nil, engine.ErrInvalid
		}
		kind = parts[0]
		var id int64
		var parseErr error
		id, parseErr = strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil || id <= 0 || (kind != string(engine.PeerUser) && kind != string(engine.PeerChat) && kind != string(engine.PeerChannel)) {
			return nil, engine.ErrInvalid
		}
		p, ok, err := e.cfg.Store.LookupPeer(ctx, e.cfg.Account, engine.PeerRef{Type: engine.PeerType(kind), ID: id})
		if err != nil {
			return nil, err
		}
		if ok {
			peers = append(peers, p)
		}
	} else {
		var err error
		peers, err = e.cfg.Store.FindPeers(ctx, e.cfg.Account, strings.TrimPrefix(r.Query, "@"), r.Limit)
		if err != nil {
			return nil, err
		}
	}
	if strings.HasPrefix(r.Query, "@") {
		// FindPeers is a fuzzy search over titles and usernames. An explicit
		// username must match exactly before it can suppress remote discovery.
		name := strings.TrimPrefix(r.Query, "@")
		exact := peers[:0]
		for _, p := range peers {
			match := strings.EqualFold(name, p.Username)
			for _, alias := range p.Usernames {
				match = match || strings.EqualFold(name, alias)
			}
			if match {
				exact = append(exact, p)
			}
		}
		peers = exact
	}
	if len(peers) == 0 && r.Network && strings.HasPrefix(r.Query, "@") {
		if err := e.require(); err != nil {
			return nil, err
		}
		got, err := e.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: strings.TrimPrefix(r.Query, "@")})
		if err != nil {
			return nil, rpcError(err)
		}
		b := engine.Batch{Account: e.cfg.Account, Source: engine.SourceRefresh, Peers: peerUpserts(got.Users, got.Chats)}
		if _, err := e.apply(ctx, b); err != nil {
			return nil, err
		}
		p, ok, err := e.cfg.Store.LookupPeer(ctx, e.cfg.Account, peerRef(got.Peer))
		if err != nil {
			return nil, err
		}
		if ok {
			peers = append(peers, p)
		}
	}
	var out []engine.Candidate
	for _, p := range peers {
		out = append(out, engine.Candidate{Peer: p, Exact: true, Source: "store"})
	}
	return out, nil
}
func (e *Engine) inputPeer(ctx context.Context, p engine.PeerRef) (tg.InputPeerClass, error) {
	a, _ := e.Account(ctx)
	if p.Type == engine.PeerUser && p.ID == a.SelfID && p.ID > 0 {
		return &tg.InputPeerSelf{}, nil
	}
	if p.Type == engine.PeerChat && p.ID > 0 {
		return &tg.InputPeerChat{ChatID: p.ID}, nil
	}
	h, ok, err := e.cfg.Store.AccessHash(ctx, e.cfg.Account, p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, engine.ErrPeerNotFound
	}
	switch p.Type {
	case engine.PeerUser:
		return &tg.InputPeerUser{UserID: p.ID, AccessHash: h}, nil
	case engine.PeerChannel:
		return &tg.InputPeerChannel{ChannelID: p.ID, AccessHash: h}, nil
	}
	return nil, engine.ErrInvalid
}
func (e *Engine) RefreshDialogs(ctx context.Context, r engine.RefreshRequest) (engine.RefreshResult, error) {
	if err := e.require(); err != nil {
		return engine.RefreshResult{}, err
	}
	if len(r.Peers) > 0 {
		return engine.RefreshResult{}, engine.ErrNotSupported
	}
	limit := r.Limit
	if limit <= 0 {
		limit = 100
	}
	// A generation owns cached results/rotation; each refresh owns its RPC cap,
	// shared across all dialog pages in this request.
	if ctx.Value(migrationDiscoveryCacheKey{}) == nil {
		ctx = context.WithValue(ctx, migrationDiscoveryCacheKey{}, &migrationDiscoveryState{checked: make(map[engine.PeerRef]bool)})
	}
	budget := migrationProbeLimit
	ctx = context.WithValue(ctx, migrationDiscoveryBudgetKey{}, &budget)
	var result engine.RefreshResult
	req := &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: min(limit, 100)}
	for result.Dialogs < limit {
		got, err := e.api.MessagesGetDialogs(ctx, req)
		if err != nil {
			return result, rpcError(err)
		}
		var dialogs []tg.DialogClass
		var users []tg.UserClass
		var chats []tg.ChatClass
		var messages []tg.MessageClass
		switch v := got.(type) {
		case *tg.MessagesDialogs:
			dialogs, users, chats, messages = v.Dialogs, v.Users, v.Chats, v.Messages
		case *tg.MessagesDialogsSlice:
			dialogs, users, chats, messages = v.Dialogs, v.Users, v.Chats, v.Messages
		default:
			return result, nil
		}
		b := engine.Batch{Account: e.cfg.Account, Source: engine.SourceRefresh, Peers: peerUpserts(users, chats)}
		for _, raw := range dialogs {
			if d, ok := raw.(*tg.Dialog); ok {
				b.Dialogs = append(b.Dialogs, engine.Dialog{Peer: peerRef(d.Peer), FolderID: d.FolderID, Pinned: d.Pinned, TopMessageID: d.TopMessage, ReadInboxMaxID: d.ReadInboxMaxID, ReadOutboxMaxID: d.ReadOutboxMaxID, UnreadCount: d.UnreadCount, UnreadMentions: d.UnreadMentionsCount, UnreadReactions: d.UnreadReactionsCount, UnreadMark: d.UnreadMark, TTLPeriod: d.TTLPeriod})
			}
		}
		// Service previews may be the only surviving migration evidence. Keep
		// their peer links without retaining preview message content: dialog
		// TTL/protection is needed before ordinary history conversion.
		for _, raw := range messages {
			appendMessageMigration(&b, raw)
		}
		if _, err := e.apply(ctx, b); err != nil {
			return result, err
		}
		result.Dialogs += len(b.Dialogs)
		result.Peers += len(b.Peers)
		if err := e.discoverLegacyMigrations(ctx, chats); err != nil {
			return result, err
		}
		if result.Dialogs >= limit || len(dialogs) < req.Limit || len(b.Dialogs) == 0 {
			return result, nil
		}
		last := b.Dialogs[len(b.Dialogs)-1]
		ip, err := e.inputPeer(ctx, last.Peer)
		if err != nil {
			return result, err
		}
		req.OffsetPeer = ip
		req.OffsetID = last.TopMessageID
		req.OffsetDate = 0
		for _, m := range messages {
			if m.GetID() != last.TopMessageID {
				continue
			}
			switch v := m.(type) {
			case *tg.Message:
				if peerRef(v.PeerID) == last.Peer {
					req.OffsetDate = v.Date
				}
			case *tg.MessageService:
				if peerRef(v.PeerID) == last.Peer {
					req.OffsetDate = v.Date
				}
			}
		}
		if req.OffsetDate == 0 {
			return result, fmt.Errorf("dialog page lacks top-message date: %w", engine.ErrInvalid)
		}
		req.SetExcludePinned(true)
		req.Limit = min(limit-result.Dialogs, 100)
	}
	return result, nil
}
func messagePage(got tg.MessagesMessagesClass) ([]tg.MessageClass, []tg.UserClass, []tg.ChatClass) {
	switch v := got.(type) {
	case *tg.MessagesMessages:
		return v.Messages, v.Users, v.Chats
	case *tg.MessagesMessagesSlice:
		return v.Messages, v.Users, v.Chats
	case *tg.MessagesChannelMessages:
		return v.Messages, v.Users, v.Chats
	}
	return nil, nil, nil
}

// messageDate uses raw metadata, including service messages omitted by the
// content converter. IDs and dates must describe the same pagination boundary.
func messageDate(raw tg.MessageClass) *time.Time {
	var seconds int
	switch m := raw.(type) {
	case *tg.Message:
		seconds = m.Date
	case *tg.MessageService:
		seconds = m.Date
	default:
		return nil
	}
	d := time.Unix(int64(seconds), 0)
	return &d
}

func (e *Engine) Backfill(ctx context.Context, r engine.BackfillRequest) (engine.BackfillResult, error) {
	result := engine.BackfillResult{Peer: r.Peer}
	if err := e.require(); err != nil {
		return result, err
	}
	select {
	case e.backfill <- struct{}{}:
		defer func() { <-e.backfill }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	ip, err := e.inputPeer(ctx, r.Peer)
	if err != nil {
		return result, err
	}
	cursor, _, err := e.cfg.Store.Cursor(ctx, e.cfg.Account, r.Peer)
	if err != nil {
		return result, err
	}
	cursor.Peer = r.Peer
	if cursor.FloodUntil != nil && cursor.FloodUntil.After(e.now()) {
		result.OldestID = cursor.OldestID
		result.OldestDate = cursor.OldestDate
		result.Complete = cursor.Complete
		result.FloodUntil = cursor.FloodUntil
		return result, &engine.FloodWaitError{Method: "messages.getHistory", Until: *cursor.FloodUntil}
	}
	count := r.Count
	if count <= 0 {
		count = 100
	}
	count = min(count, 100)
	rounds := r.Rounds
	if rounds <= 0 {
		rounds = 1
	}
	for n := 0; n < rounds && !cursor.Complete; n++ {
		got, err := e.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: ip, OffsetID: cursor.OldestID, Limit: count})
		if err != nil {
			err = rpcError(err)
			cursor.State = "error"
			cursor.LastError = "getHistory failed"
			cursor.FloodUntil = nil
			var rpc *engine.RPCError
			if errors.Is(err, engine.ErrNoAccess) || (errors.As(err, &rpc) && (rpc.Type == "CHANNEL_PRIVATE" || rpc.Type == "CHAT_FORBIDDEN" || rpc.Type == "USER_BANNED_IN_CHANNEL")) {
				cursor.State = "no_access"
				if rpc != nil {
					rpc.Kind = engine.ErrNoAccess
				}
			}
			if f, ok := err.(*engine.FloodWaitError); ok {
				cursor.State = "flood_wait"
				cursor.FloodUntil = &f.Until
				result.FloodUntil = &f.Until
			}
			_, saveErr := e.apply(ctx, engine.Batch{Account: e.cfg.Account, Source: engine.SourceBackfill, Cursor: &cursor})
			if saveErr != nil {
				return result, saveErr
			}
			return result, err
		}
		raw, users, chats := messagePage(got)
		b := engine.Batch{Account: e.cfg.Account, Source: engine.SourceBackfill, CatchUp: true, Peers: peerUpserts(users, chats)}
		cursor.State = "idle"
		cursor.LastError = ""
		cursor.FloodUntil = nil
		stopped := false
		reachedStart := len(raw) == 0
		previousOffset := cursor.OldestID
		for _, m := range raw {
			if m == nil || m.GetID() <= 0 {
				continue
			}
			date := messageDate(m)
			if r.Until != nil && date != nil && date.Before(*r.Until) {
				// Do not cover history that was excluded by the caller's
				// cutoff. A later unbounded call must still fetch it.
				stopped = true
				continue
			}
			if cursor.OldestID == 0 || m.GetID() < cursor.OldestID {
				cursor.OldestID = m.GetID()
				cursor.OldestDate = date
			}
			cursor.NewestID = max(cursor.NewestID, m.GetID())
			if service, ok := m.(*tg.MessageService); ok {
				switch service.Action.(type) {
				case *tg.MessageActionChatCreate, *tg.MessageActionChannelCreate:
					reachedStart = true
				}
			}
			appendMessage(&b, m, false, r.Peer)
		}
		if len(raw) > 0 && !stopped && cursor.OldestID == previousOffset {
			return result, fmt.Errorf("history page did not advance: %w", engine.ErrInvalid)
		}
		cursor.Fetched += int64(len(raw))
		cursor.Complete = reachedStart
		b.Cursor = &cursor
		applied, err := e.apply(ctx, b)
		if err != nil {
			return result, err
		}
		result.Fetched += len(raw)
		result.Added += len(applied.Inserted)
		result.Rounds++
		if len(raw) == 0 || stopped {
			break
		}
	}
	result.Complete = cursor.Complete
	result.OldestID = cursor.OldestID
	result.OldestDate = cursor.OldestDate
	return result, nil
}
