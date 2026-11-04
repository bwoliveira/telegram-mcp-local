package mcp

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// peopleAccount deliberately checks only the configured local account identity.
// Offline/unauthenticated accounts can still read their retained snapshots.
func (s *Server) peopleAccount(ctx context.Context) (engine.AccountID, map[string]any) {
	if s.eng == nil {
		return "", toolError("not implemented yet: Telegram engine is not configured")
	}
	if s.readStore == nil {
		return "", toolError("not implemented yet: Telegram local ReadStore is not configured")
	}
	acc, err := s.eng.Account(ctx)
	if err != nil {
		return "", engineError(err)
	}
	if acc.ID == "" {
		return "", engineError(engine.ErrAccountMismatch)
	}
	return acc.ID, nil
}

func peopleName(p engine.Peer) string {
	if p.Title != "" {
		return p.Title
	}
	return strings.TrimSpace(p.FirstName + " " + p.LastName)
}

func peopleMatches(query string, values ...string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return true
	}
	for _, v := range values {
		if strings.Contains(strings.ToLower(v), query) {
			return true
		}
	}
	return false
}

func peoplePeerMatches(query string, p engine.Peer) bool {
	return peopleMatches(query, append([]string{peopleName(p), peerID(p.Ref), p.Username, p.Phone}, p.Usernames...)...)
}

func peopleSort(peers []engine.Peer) {
	sort.SliceStable(peers, func(i, j int) bool {
		a, b := strings.ToLower(peopleName(peers[i])), strings.ToLower(peopleName(peers[j]))
		if a != b {
			return a < b
		}
		return peerID(peers[i].Ref) < peerID(peers[j].Ref)
	})
}

func (s *Server) peopleCoverage(ctx context.Context, account engine.AccountID, peer *engine.PeerRef, body map[string]any) map[string]any {
	complete, oldest, err := s.readStore.ReadCoverage(ctx, account, peer)
	if err != nil {
		return engineError(err)
	}
	body["coverage_complete"] = complete
	if _, ok := body["partial"]; !ok {
		body["partial"] = !complete || body["truncated"] == true
	}
	body["source"] = "telegram_local_store"
	body["untrusted_content"] = UntrustedContent
	if oldest != nil {
		body["local_history_since"] = oldest
	}
	return textResult(body, false)
}

func (s *Server) telegramGroups(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	account, fail := s.peopleAccount(ctx)
	if fail != nil {
		return fail
	}
	peers, err := s.readStore.ReadPeers(ctx, account)
	if err != nil {
		return engineError(err)
	}
	selected := make([]engine.Peer, 0, len(peers))
	for _, p := range peers {
		if (p.Ref.Type == engine.PeerChat || p.Ref.Type == engine.PeerChannel) && peoplePeerMatches(a.Search, p) {
			selected = append(selected, p)
		}
	}
	peopleSort(selected)
	n := limit(a.Limit, 100)
	truncated := len(selected) > n
	if truncated {
		selected = selected[:n]
	}
	rows := make([]map[string]any, 0, len(selected))
	for _, p := range selected {
		rows = append(rows, map[string]any{"group_id": peerID(p.Ref), "chat_id": peerID(p.Ref), "name": peopleName(p), "kind": p.Kind, "size": p.Participants, "access": p.Access})
	}
	return s.peopleCoverage(ctx, account, nil, map[string]any{"groups": rows, "count": len(rows), "truncated": truncated})
}

func peopleGroupShape(g engine.Group, a arguments) map[string]any {
	participants := make([]map[string]any, 0, len(g.Participants))
	partial := g.Partial
	for _, p := range g.Participants {
		if !peopleMatches(a.Search, p.Name, p.Username, peerID(p.Peer)) {
			partial = true
			continue
		}
		participants = append(participants, map[string]any{"chat_id": peerID(p.Peer), "name": p.Name, "username": p.Username, "role": p.Role, "joined_at": p.JoinedAt})
	}
	n := limit(a.Limit, 100)
	if len(participants) > n {
		participants = participants[:n]
		partial = true
	}
	owner := ""
	if g.Owner != nil {
		owner = peerID(*g.Owner)
	}
	body := map[string]any{"group_id": peerID(g.Peer.Ref), "chat_id": peerID(g.Peer.Ref), "name": peopleName(g.Peer), "kind": g.Peer.Kind, "access": g.Peer.Access, "owner": owner, "created_at": g.CreatedAt, "participants": participants, "size": g.Size, "snapshot_at": g.SnapshotAt, "partial": partial, "about": g.About, "am_admin": g.AmAdmin}
	if g.LinkedChat != nil {
		body["linked_chat_id"] = peerID(*g.LinkedChat)
	}
	return body
}

func (s *Server) telegramGroup(ctx context.Context, a arguments) map[string]any {
	id := a.GroupJID
	if id == "" {
		return toolError("group_id is required")
	}
	timeout := readTimeout
	if a.Live != nil && *a.Live {
		timeout = liveTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	account, fail := s.peopleAccount(ctx)
	if fail != nil {
		return fail
	}
	p, err := s.resolvePeer(ctx, id)
	if err != nil {
		return engineError(err)
	}
	if p.Type == engine.PeerUser {
		return toolError("group_id must identify a chat or channel")
	}
	var g engine.Group
	if a.Live != nil && *a.Live {
		g, err = s.eng.RefreshGroup(ctx, engine.GroupRequest{Group: p, Search: a.Search, Limit: limit(a.Limit, 100)})
	} else {
		g, err = s.localGroupSnapshot(ctx, account, p)
	}
	if err != nil {
		return engineError(err)
	}
	body := peopleGroupShape(g, a)
	body["snapshot_available"] = !g.SnapshotAt.IsZero()
	if g.SnapshotAt.IsZero() {
		body["note"] = "Only cached group metadata is available; no full participant snapshot has been fetched. Use live:true to refresh."
	} else if g.Partial {
		body["note"] = "Participant snapshot is partial due to Telegram visibility restrictions or the requested limit."
	}
	body["live"] = a.Live != nil && *a.Live
	return s.peopleCoverage(ctx, account, &p, body)
}

// peopleMessage projects the public MCP shape, not engine storage objects or
// opaque media references. Unread previews are still untrusted message content.
func peopleMessage(m engine.Message, name string) map[string]any {
	row := map[string]any{"id": strconv.Itoa(m.Key.ID), "chat_id": peerID(m.Key.Peer), "chat_name": name, "timestamp": m.Date, "from_me": m.Out, "text": m.Text}
	if m.Sender != nil {
		row["sender_id"] = peerID(*m.Sender)
	}
	if m.SenderName != "" {
		row["sender_name"] = m.SenderName
	}
	if m.Media != nil {
		row["media_type"] = m.Media.Type
	}
	if m.TopicID != 0 {
		row["topic_id"] = m.TopicID
	}
	return row
}

func (s *Server) telegramUnread(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	account, fail := s.peopleAccount(ctx)
	if fail != nil {
		return fail
	}
	dialogs, err := s.readStore.ReadDialogs(ctx, account)
	if err != nil {
		return engineError(err)
	}
	peers, err := s.readStore.ReadPeers(ctx, account)
	if err != nil {
		return engineError(err)
	}
	messages, err := s.readStore.ReadMessages(ctx, account)
	if err != nil {
		return engineError(err)
	}
	// Snapshot-only adapters cannot create triage marks, but remain readable.
	// When local triage is configured, use only this account's durable marks
	// and propagate persistence errors instead of falling back to legacy state.
	var marks map[engine.PeerRef]store.TriageMark
	if _, ok := s.readStore.(triageMarkStore); ok {
		marks, err = s.triageMarks(ctx, account)
		if err != nil {
			return engineError(err)
		}
	}
	byPeer := make(map[engine.PeerRef]engine.Peer, len(peers))
	for _, p := range peers {
		byPeer[p.Ref] = p
	}
	now := time.Now()
	if s.clock != nil {
		now = s.clock()
	}
	incoming := make(map[engine.PeerRef][]engine.Message)
	latest := make(map[engine.PeerRef]time.Time)
	for _, m := range messages {
		if m.Key.Account != account || (m.ExpiresAt != nil && !m.ExpiresAt.After(now)) {
			continue
		}
		if m.Date.After(latest[m.Key.Peer]) {
			latest[m.Key.Peer] = m.Date
		}
		if !m.Out {
			incoming[m.Key.Peer] = append(incoming[m.Key.Peer], m)
		}
	}
	for p, msgs := range incoming {
		sort.SliceStable(msgs, func(i, j int) bool {
			if !msgs[i].Date.Equal(msgs[j].Date) {
				return msgs[i].Date.After(msgs[j].Date)
			}
			return msgs[i].Key.ID > msgs[j].Key.ID
		})
		incoming[p] = msgs
	}
	includeMuted := a.IncludeMuted == nil || *a.IncludeMuted
	selected := make([]engine.Dialog, 0, len(dialogs))
	for _, d := range dialogs {
		// Only a known bot user conversation is excluded. Unknown peers and
		// groups with bot senders or participants remain eligible.
		if !a.IncludeBots && d.Peer.Type == engine.PeerUser && byPeer[d.Peer].Kind == engine.KindBot {
			continue
		}
		if d.UnreadCount <= 0 && !d.UnreadMark {
			continue
		}
		if d.FolderID == 1 && !a.IncludeArchived {
			continue
		}
		if !includeMuted && d.MuteUntil != nil && d.MuteUntil.After(now) {
			continue
		}
		// Suppress before sorting/limiting and counting. Only a newer active
		// incoming message lifts a decision; outgoing activity cannot do so.
		var lastIncoming time.Time
		if msgs := incoming[d.Peer]; len(msgs) != 0 {
			lastIncoming = msgs[0].Date
		}
		if marks[d.Peer].Hides(lastIncoming, now) {
			continue
		}
		selected = append(selected, d)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		a, b := latest[selected[i].Peer], latest[selected[j].Peer]
		if !a.Equal(b) {
			return a.After(b)
		}
		return peerID(selected[i].Peer) < peerID(selected[j].Peer)
	})
	n := min(limit(a.Limit, 20), 100)
	truncated := len(selected) > n
	if truncated {
		selected = selected[:n]
	}
	perChat := min(limit(a.PerChat, 5), 20)
	chats := make([]map[string]any, 0, len(selected))
	total := 0
	for _, d := range selected {
		p := byPeer[d.Peer]
		name := peopleName(p)
		msgs := incoming[d.Peer]
		// The server counter is authoritative but does not prove which locally
		// retained messages remain unread. Previews are latest received, not receipts.
		previewLimit := perChat
		if d.UnreadCount > 0 {
			previewLimit = min(previewLimit, d.UnreadCount)
		}
		if len(msgs) > previewLimit {
			msgs = msgs[:previewLimit]
		}
		preview := make([]map[string]any, 0, len(msgs))
		for _, m := range msgs {
			preview = append(preview, peopleMessage(m, name))
		}
		total += max(d.UnreadCount, 0)
		muted := d.MuteUntil != nil && d.MuteUntil.After(now)
		chats = append(chats, map[string]any{"chat_id": peerID(d.Peer), "name": name, "kind": p.Kind, "unread_count": d.UnreadCount, "marked_unread": d.UnreadMark, "unread_mark": d.UnreadMark, "muted": muted, "muted_until": d.MuteUntil, "archived": d.FolderID == 1, "last_message_ts": latest[d.Peer], "messages": preview})
	}
	return s.peopleCoverage(ctx, account, nil, map[string]any{"chats": chats, "count": len(chats), "unread_messages": total, "truncated": truncated, "note": "Unread counts are cached Telegram dialog counters and can differ from local history until the next sync. Messages are latest locally retained received previews, not an exact unread set. Reading does not mark messages read."})
}

func (s *Server) telegramContacts(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	account, fail := s.peopleAccount(ctx)
	if fail != nil {
		return fail
	}
	peers, err := s.readStore.ReadPeers(ctx, account)
	if err != nil {
		return engineError(err)
	}
	selected := make([]engine.Peer, 0, len(peers))
	for _, p := range peers {
		if p.Ref.Type == engine.PeerUser && peoplePeerMatches(a.Search, p) {
			selected = append(selected, p)
		}
	}
	peopleSort(selected)
	n := limit(a.Limit, 100)
	truncated := len(selected) > n
	if truncated {
		selected = selected[:n]
	}
	rows := make([]map[string]any, 0, len(selected))
	for _, p := range selected {
		rows = append(rows, map[string]any{"chat_id": peerID(p.Ref), "kind": p.Kind, "name": peopleName(p), "username": p.Username, "phone": p.Phone, "is_contact": p.IsContact, "access": p.Access})
	}
	return s.peopleCoverage(ctx, account, nil, map[string]any{"contacts": rows, "count": len(rows), "truncated": truncated})
}
