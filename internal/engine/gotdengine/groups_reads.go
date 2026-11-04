package gotdengine

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// RefreshGroup reads Telegram metadata and the visible membership, never read
// receipts. A limited/hidden member list is a partial snapshot, not a missing peer.
func (e *Engine) RefreshGroup(ctx context.Context, req engine.GroupRequest) (engine.Group, error) {
	var g engine.Group
	if err := ctx.Err(); err != nil {
		return g, err
	}
	if req.Group.ID <= 0 || (req.Group.Type != engine.PeerChat && req.Group.Type != engine.PeerChannel) || req.Limit < 0 {
		return g, engine.ErrInvalid
	}
	if err := e.require(); err != nil {
		return g, err
	}
	ip, err := e.inputPeer(ctx, req.Group)
	if err != nil {
		return g, err
	}
	search := strings.TrimSpace(req.Search)
	channelSearch := req.Group.Type == engine.PeerChannel && search != ""
	var full *tg.MessagesChatFull
	if req.Group.Type == engine.PeerChat {
		full, err = e.api.MessagesGetFullChat(ctx, req.Group.ID)
	} else {
		full, err = e.api.ChannelsGetFullChannel(ctx, groupInputChannel(ip))
	}
	if err != nil {
		if saveErr := e.persistMetadataAccessLoss(ctx, req.Group, err); saveErr != nil {
			return g, saveErr
		}
		method := "messages.getFullChat"
		if req.Group.Type == engine.PeerChannel {
			method = "channels.getFullChannel"
		}
		return g, groupRPCError(err, method)
	}
	if full == nil {
		return g, engine.ErrInvalid
	}
	batch := engine.Batch{Account: e.cfg.Account, Source: engine.SourceRefresh, Peers: peerUpserts(full.Users, full.Chats)}
	appendFullChatMigration(&batch, full.FullChat)
	// Counts are group-read metadata; the common peer mapper intentionally
	// remains shared with update ingestion and discovery.
	for i := range batch.Peers {
		for _, raw := range full.Chats {
			switch c := raw.(type) {
			case *tg.Chat:
				if batch.Peers[i].Peer.Ref == (engine.PeerRef{Type: engine.PeerChat, ID: c.ID}) {
					batch.Peers[i].Peer.Participants = c.ParticipantsCount
				}
			case *tg.Channel:
				if batch.Peers[i].Peer.Ref == (engine.PeerRef{Type: engine.PeerChannel, ID: c.ID}) {
					batch.Peers[i].Peer.Participants = c.ParticipantsCount
				}
			}
		}
	}
	g = engine.Group{Peer: engine.Peer{Ref: req.Group}, SnapshotAt: e.now(), Participants: []engine.Participant{}}
	for _, up := range batch.Peers {
		if up.Peer.Ref == req.Group {
			g.Peer = up.Peer
		}
	}
	if g.Peer.Kind == "" {
		return g, engine.ErrInvalid
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, 10000)
	switch v := full.FullChat.(type) {
	case *tg.ChatFull:
		if req.Group.Type != engine.PeerChat || v.ID != req.Group.ID {
			return g, engine.ErrInvalid
		}
		g.About = v.About
		g.Size = g.Peer.Participants
		switch p := v.Participants.(type) {
		case *tg.ChatParticipants:
			for _, raw := range p.Participants {
				var member engine.Participant
				switch m := raw.(type) {
				case *tg.ChatParticipantCreator:
					member = groupMember(m.UserID, "creator", 0)
				case *tg.ChatParticipantAdmin:
					member = groupMember(m.UserID, "admin", m.Date)
				case *tg.ChatParticipant:
					member = groupMember(m.UserID, "member", m.Date)
				default:
					g.Partial = true
					continue
				}
				g.Participants = append(g.Participants, member)
			}
			g.Size = max(g.Size, len(g.Participants))
		case *tg.ChatParticipantsForbidden:
			g.Partial = true
		default:
			g.Partial = true
		}
	case *tg.ChannelFull:
		if req.Group.Type != engine.PeerChannel || v.ID != req.Group.ID {
			return g, engine.ErrInvalid
		}
		g.About = v.About
		g.Size = max(v.ParticipantsCount, g.Peer.Participants)
		if v.LinkedChatID > 0 {
			linked := engine.PeerRef{Type: engine.PeerChannel, ID: v.LinkedChatID}
			g.LinkedChat = &linked
		}
		// Do not assume an absent capability flag means an empty group. Some
		// accounts can still request a restricted recent-members view.
		g.Partial = v.ParticipantsHidden || channelSearch
		var filter tg.ChannelParticipantsFilterClass = &tg.ChannelParticipantsRecent{}
		if channelSearch {
			filter = &tg.ChannelParticipantsSearch{Q: search}
		}
		// Collection has its own bound; the caller's limit shapes only the response.
		const collectionLimit = 10000
		seen := map[engine.PeerRef]bool{}
		offset := 0
		for len(g.Participants) < collectionLimit {
			if err := ctx.Err(); err != nil {
				return engine.Group{}, err
			}
			got, err := e.api.ChannelsGetParticipants(ctx, &tg.ChannelsGetParticipantsRequest{Channel: groupInputChannel(ip), Filter: filter, Offset: offset, Limit: min(200, collectionLimit-len(g.Participants))})
			if err != nil {
				if tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
					g.Partial = true
					break
				}
				// A later definitive refusal supersedes full-channel metadata
				// and every collected page. Persist only the access lifecycle.
				if saveErr := e.persistMetadataAccessLoss(ctx, req.Group, err); saveErr != nil {
					return engine.Group{}, saveErr
				}
				return engine.Group{}, groupRPCError(err, "channels.getParticipants")
			}
			page, ok := got.(*tg.ChannelsChannelParticipants)
			if !ok {
				g.Partial = true
				break
			}
			batch.Peers = append(batch.Peers, peerUpserts(page.Users, page.Chats)...)
			g.Size = max(g.Size, page.Count)
			before := len(g.Participants)
			for _, raw := range page.Participants {
				member, ok := channelGroupMember(raw)
				if !ok {
					g.Partial = true
					continue
				}
				if !seen[member.Peer] {
					seen[member.Peer] = true
					g.Participants = append(g.Participants, member)
				}
			}
			offset += len(page.Participants)
			if len(page.Participants) == 0 || len(g.Participants) == before {
				g.Partial = g.Partial || len(g.Participants) < g.Size
				break
			}
			if offset >= page.Count {
				break
			}
		}
	default:
		return g, engine.ErrInvalid
	}
	account, err := e.Account(ctx)
	if err != nil {
		return engine.Group{}, err
	}
	var selfRights *engine.Participant
	for _, raw := range full.Chats {
		switch c := raw.(type) {
		case *tg.Chat:
			if req.Group.Type == engine.PeerChat && c.ID == req.Group.ID {
				g.AmAdmin = c.Creator || !c.AdminRights.Zero()
				g.CreatedAt = groupDate(c.Date)
				selfRights = groupSelfRights(&g, account.SelfID, c.Creator, c.AdminRights, tg.ChatBannedRights{})
			}
		case *tg.Channel:
			if req.Group.Type == engine.PeerChannel && c.ID == req.Group.ID {
				g.AmAdmin = c.Creator || !c.AdminRights.Zero()
				g.CreatedAt = groupDate(c.Date)
				if !channelSearch && !c.Min {
					selfRights = groupSelfRights(&g, account.SelfID, c.Creator, c.AdminRights, c.BannedRights)
				}
			}
		}
	}
	byPeer := map[engine.PeerRef]engine.Peer{}
	for _, p := range batch.Peers {
		byPeer[p.Peer.Ref] = p.Peer
	}
	for i := range g.Participants {
		p := &g.Participants[i]
		meta := byPeer[p.Peer]
		p.Name = strings.TrimSpace(meta.FirstName + " " + meta.LastName)
		if meta.Title != "" {
			p.Name = meta.Title
		}
		p.Username = meta.Username
		if p.Role == "creator" {
			owner := p.Peer
			g.Owner = &owner
		}
	}
	g.Partial = g.Partial || len(g.Participants) < g.Size
	// Snapshot selection and application share the engine commit lock so a
	// concurrent complete refresh cannot be replaced by a partial collection.
	_, err = func() (engine.Applied, error) {
		e.commitMu.Lock()
		defer e.commitMu.Unlock()
		// Server-filtered searches are never membership snapshots. Unfiltered
		// partial collections may initialize a cache, but not downgrade a
		// retained complete snapshot or its metadata and timestamp. Explicit
		// actor permissions are independent of member collection completeness.
		applySnapshot := !channelSearch
		mergeSelfRights := false
		if applySnapshot && g.Partial {
			if reader, ok := e.cfg.Store.(interface {
				ReadGroup(context.Context, engine.AccountID, engine.PeerRef) (engine.Group, error)
			}); ok {
				cached, readErr := reader.ReadGroup(ctx, e.cfg.Account, req.Group)
				if readErr != nil && !errors.Is(readErr, engine.ErrPeerNotFound) {
					return engine.Applied{}, readErr
				}
				applySnapshot = readErr != nil || cached.Partial
				mergeSelfRights = !applySnapshot && selfRights != nil
			}
		}
		if applySnapshot {
			batch.Participants = []engine.ParticipantsSnapshot{{Group: req.Group, Participants: g.Participants, Size: g.Size, Partial: g.Partial, About: g.About, Owner: g.Owner, CreatedAt: g.CreatedAt, LinkedChat: g.LinkedChat, AmAdmin: g.AmAdmin, At: g.SnapshotAt}}
		}
		applied, applyErr := e.applyLocked(ctx, batch)
		if applyErr != nil || !mergeSelfRights {
			return applied, applyErr
		}
		if merger, ok := e.cfg.Store.(interface {
			RefreshGroupSelfRights(context.Context, engine.AccountID, engine.PeerRef, engine.Participant, bool) error
		}); ok {
			return applied, merger.RefreshGroupSelfRights(ctx, e.cfg.Account, req.Group, *selfRights, g.AmAdmin)
		}
		// A store retaining complete groups must also support explicit rights
		// refresh; silently retaining old permissions would authorize stale writes.
		return applied, engine.ErrInvalid
	}()
	if err != nil {
		return engine.Group{}, err
	}
	if search != "" && !channelSearch {
		filtered := []engine.Participant{}
		q := strings.ToLower(search)
		for _, p := range g.Participants {
			if strings.Contains(strings.ToLower(p.Name+" "+p.Username+" "+string(p.Peer.Type)+":"+itoa(p.Peer.ID)), q) {
				filtered = append(filtered, p)
			}
		}
		g.Partial = g.Partial || len(filtered) < len(g.Participants)
		g.Participants = filtered
	}
	if len(g.Participants) > limit {
		g.Participants = g.Participants[:limit]
		g.Partial = true
	}
	return g, nil
}

// Full-chat metadata contains the actor's rights even when a recent-members
// page omits the actor. Keep that explicit evidence in the cached membership.
func groupSelfRights(g *engine.Group, selfID int64, creator bool, admin tg.ChatAdminRights, banned tg.ChatBannedRights) *engine.Participant {
	if selfID <= 0 {
		return nil
	}
	admin.SetFlags()
	banned.SetFlags()
	ref := engine.PeerRef{Type: engine.PeerUser, ID: selfID}
	known := engine.Participant{Peer: ref, Role: "member", AdminRights: uint32(admin.Flags), BannedRights: uint32(banned.Flags), BannedUntil: banned.UntilDate}
	switch {
	case creator:
		known.Role = "creator"
	case admin.Flags != 0:
		known.Role = "admin"
	case banned.ViewMessages:
		known.Role = "banned"
	case banned.Flags != 0:
		known.Role = "restricted"
	}
	index := -1
	for i, member := range g.Participants {
		if member.Peer == ref {
			index = i
			break
		}
	}
	if index < 0 {
		if !creator && admin.Flags == 0 && banned.Flags == 0 {
			return &known
		}
		g.Participants = append(g.Participants, engine.Participant{Peer: ref, Role: "member"})
		index = len(g.Participants) - 1
	}
	member := &g.Participants[index]
	// A creator participant constructor is positive ownership evidence even if
	// the full-chat metadata omits Creator. Preserve it in both persistence paths.
	if member.Role == "creator" {
		known.Role = "creator"
	}
	// Reconcile role and rights together, including demotion and restriction
	// clearing, without replacing unrelated member metadata.
	member.Role = known.Role
	member.AdminRights, member.BannedRights, member.BannedUntil = known.AdminRights, known.BannedRights, known.BannedUntil
	return &known
}

func groupDate(date int) *time.Time {
	if date <= 0 {
		return nil
	}
	v := time.Unix(int64(date), 0).UTC()
	return &v
}
func groupMember(id int64, role string, date int) engine.Participant {
	return engine.Participant{Peer: engine.PeerRef{Type: engine.PeerUser, ID: id}, Role: role, JoinedAt: groupDate(date)}
}
func channelGroupMember(raw tg.ChannelParticipantClass) (engine.Participant, bool) {
	switch p := raw.(type) {
	case *tg.ChannelParticipant:
		return groupMember(p.UserID, "member", p.Date), true
	case *tg.ChannelParticipantSelf:
		return groupMember(p.UserID, "member", p.Date), true
	case *tg.ChannelParticipantCreator:
		member := groupMember(p.UserID, "creator", 0)
		p.AdminRights.SetFlags()
		member.AdminRights = uint32(p.AdminRights.Flags)
		return member, true
	case *tg.ChannelParticipantAdmin:
		member := groupMember(p.UserID, "admin", p.Date)
		p.AdminRights.SetFlags()
		member.AdminRights, member.CanEdit = uint32(p.AdminRights.Flags), p.CanEdit
		return member, true
	case *tg.ChannelParticipantBanned:
		role := "restricted"
		if p.Left || p.BannedRights.ViewMessages {
			role = "banned"
		}
		p.BannedRights.SetFlags()
		return engine.Participant{Peer: peerRef(p.Peer), Role: role, JoinedAt: groupDate(p.Date), BannedRights: uint32(p.BannedRights.Flags), BannedUntil: p.BannedRights.UntilDate}, true
	case *tg.ChannelParticipantLeft:
		return engine.Participant{Peer: peerRef(p.Peer), Role: "left"}, true
	default:
		return engine.Participant{}, false
	}
}
