package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// telegramReadHandlers bypasses only the inherited index adapters. Other tools
// keep their existing routing until their owning fronts migrate them.
func (s *Server) telegramReadHandlers() map[string]func(context.Context, arguments) map[string]any {
	return map[string]func(context.Context, arguments) map[string]any{
		"list_chats": s.telegramChats, "get_chat_messages": s.telegramMessages,
		"search_messages": s.telegramSearch, "get_message_context": s.telegramContext,
		"list_contacts": s.telegramContacts, "list_groups": s.telegramGroups,
		"get_group": s.telegramGroup, "list_unread": s.telegramUnread,
	}
}

type readSnapshot struct {
	account  engine.AccountID
	peers    map[engine.PeerRef]engine.Peer
	messages []engine.Message
}

func (s *Server) readSnapshot(ctx context.Context) (readSnapshot, map[string]any) {
	acc, fail := s.peopleAccount(ctx)
	if fail != nil {
		return readSnapshot{}, fail
	}
	peers, err := s.readStore.ReadPeers(ctx, acc)
	if err != nil {
		return readSnapshot{}, engineError(err)
	}
	msgs, err := s.readStore.ReadMessages(ctx, acc)
	if err != nil {
		return readSnapshot{}, engineError(err)
	}
	out := readSnapshot{account: acc, peers: map[engine.PeerRef]engine.Peer{}, messages: make([]engine.Message, 0, len(msgs))}
	for _, p := range peers {
		out.peers[p.Ref] = p
	}
	for _, m := range msgs {
		// The store owns TTL exclusion; this second check protects presentation even
		// when an adapter has not yet swept an expired row.
		if m.Key.Account != acc {
			return readSnapshot{}, engineError(engine.ErrAccountMismatch)
		}
		if m.ExpiresAt != nil && !m.ExpiresAt.After(s.clock()) {
			continue
		}
		if m.TTLPeriod > 0 && !m.Date.Add(time.Duration(m.TTLPeriod)*time.Second).After(s.clock()) {
			continue
		}
		out.messages = append(out.messages, m)
	}
	return out, nil
}

// scope traverses migration links in both directions, preserving each message's
// original key. It never rewrites basic-group ids into channel ids (D4).
func (r readSnapshot) scope(peer *engine.PeerRef, migrated *bool) map[engine.PeerRef]bool {
	selected := map[engine.PeerRef]bool{}
	if peer == nil {
		for p := range r.peers {
			selected[p] = true
		}
		return selected
	}
	selected[*peer] = true
	if migrated != nil && !*migrated {
		return selected
	}
	for changed := true; changed; {
		changed = false
		for ref, p := range r.peers {
			if p.MigratedTo == nil {
				continue
			}
			if selected[ref] || selected[*p.MigratedTo] {
				if !selected[ref] || !selected[*p.MigratedTo] {
					changed = true
				}
				selected[ref] = true
				selected[*p.MigratedTo] = true
			}
		}
	}
	return selected
}
func (s *Server) readCoverage(ctx context.Context, r readSnapshot, peer *engine.PeerRef, scope map[engine.PeerRef]bool) (bool, *time.Time, error) {
	if peer == nil {
		return s.readStore.ReadCoverage(ctx, r.account, nil)
	}
	complete := true
	var oldest *time.Time
	for p := range scope {
		c, o, err := s.readStore.ReadCoverage(ctx, r.account, &p)
		if err != nil {
			return false, nil, err
		}
		complete = complete && c
		if o != nil && (oldest == nil || o.Before(*oldest)) {
			t := *o
			oldest = &t
		}
	}
	return complete, oldest, nil
}
func (s *Server) readPeer(ctx context.Context, a arguments, required bool) (*engine.PeerRef, error) {
	if a.ChatJID == "" {
		if required {
			return nil, fmt.Errorf("chat_id is required")
		}
		return nil, nil
	}
	p, err := s.resolvePeer(ctx, a.ChatJID)
	return &p, err
}
func (r readSnapshot) message(m engine.Message) message {
	out := engineMessage(m)
	if p, ok := r.peers[m.Key.Peer]; ok {
		if out.ChatName == "" {
			out.ChatName = peopleName(p)
		}
		out.Protected = out.Protected || p.Protected
	}
	if out.Protected && out.Media != nil {
		media := *out.Media
		media.Downloadable = false
		out.Media = &media
	}
	if m.Sender != nil && out.SenderName == "" {
		out.SenderName = peopleName(r.peers[*m.Sender])
	}
	return out
}
func readOrder(msgs []message, oldest bool) {
	sort.SliceStable(msgs, func(i, j int) bool {
		if !msgs[i].Timestamp.Equal(msgs[j].Timestamp) {
			if oldest {
				return msgs[i].Timestamp.Before(msgs[j].Timestamp)
			}
			return msgs[i].Timestamp.After(msgs[j].Timestamp)
		}
		// Stable across store/map iteration and equal timestamps, including migrations.
		if msgs[i].ChatJID != msgs[j].ChatJID {
			return msgs[i].ChatJID < msgs[j].ChatJID
		}
		ki, _ := parseMessageKey(arguments{ChatJID: msgs[i].ChatJID, MessageID: msgs[i].ID}, "")
		kj, _ := parseMessageKey(arguments{ChatJID: msgs[j].ChatJID, MessageID: msgs[j].ID}, "")
		if oldest {
			return ki.ID < kj.ID
		}
		return ki.ID > kj.ID
	})
}

func (s *Server) telegramChats(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	if a.Offset < 0 {
		return toolError("offset must be non-negative")
	}
	if a.Search == "" {
		a.Search = a.Query
	}
	sh, err := newShaping(a, chatFields)
	if err != nil {
		return toolError("%v", err)
	}
	r, fail := s.readSnapshot(ctx)
	if fail != nil {
		return fail
	}
	dialogs, err := s.readStore.ReadDialogs(ctx, r.account)
	if err != nil {
		return engineError(err)
	}
	// Dialog refresh is not the only way a conversation can become known.
	// Include observed groups/channels and message-only conversations, but not
	// contact-only users, without fabricating server unread counters.
	known := make(map[engine.PeerRef]bool, len(dialogs))
	for _, d := range dialogs {
		known[d.Peer] = true
	}
	add := func(ref engine.PeerRef) {
		if !known[ref] {
			known[ref] = true
			dialogs = append(dialogs, engine.Dialog{Peer: ref})
		}
	}
	for ref := range r.peers {
		if ref.Type == engine.PeerChat || ref.Type == engine.PeerChannel {
			add(ref)
		}
	}
	latest := map[engine.PeerRef]time.Time{}
	for _, m := range r.messages {
		add(m.Key.Peer)
		if m.Date.After(latest[m.Key.Peer]) {
			latest[m.Key.Peer] = m.Date
		}
	}
	sort.SliceStable(dialogs, func(i, j int) bool {
		if dialogs[i].Pinned != dialogs[j].Pinned {
			return dialogs[i].Pinned
		}
		if !latest[dialogs[i].Peer].Equal(latest[dialogs[j].Peer]) {
			return latest[dialogs[i].Peer].After(latest[dialogs[j].Peer])
		}
		return peerID(dialogs[i].Peer) < peerID(dialogs[j].Peer)
	})
	rows := make([]map[string]any, 0)
	for _, d := range dialogs {
		p := r.peers[d.Peer]
		// Keep predecessors available to typed history reads, but hide them
		// from chat discovery before search, totals and pagination. Filtering
		// only server dialogs is insufficient: peers/messages add them above.
		if p.MigratedTo != nil {
			continue
		}
		if !peoplePeerMatches(a.Search, p) {
			continue
		}
		kind := p.Kind
		if kind == engine.KindSelf {
			kind = engine.KindUser
		}
		row := map[string]any{"chat_id": peerID(d.Peer), "kind": kind, "name": peopleName(p), "last_message_ts": latest[d.Peer], "archived": d.FolderID == 1, "pinned": d.Pinned, "muted": d.MuteUntil != nil && d.MuteUntil.After(s.clock()), "muted_until": d.MuteUntil, "unread": d.UnreadMark || d.UnreadCount > 0, "unread_count": d.UnreadCount, "access": p.Access}
		if len(sh.fields) > 0 {
			row = sh.pick(row)
		}
		rows = append(rows, row)
	}
	total := len(rows)
	start := min(a.Offset, total)
	end := start + min(limit(a.Limit, 50), total-start)
	rows = rows[start:end]
	hasMore := end < total
	var nextOffset *int
	if hasMore {
		nextOffset = &end
	}
	complete, oldest, err := s.readStore.ReadCoverage(ctx, r.account, nil)
	if err != nil {
		return engineError(err)
	}
	return textResult(map[string]any{"chats": rows, "count": len(rows), "total": total, "offset": a.Offset, "next_offset": nextOffset, "has_more": hasMore, "history_since": oldest, "coverage_complete": complete, "partial": !complete, "truncated": hasMore, "untrusted_content": UntrustedContent}, false)
}

type readFilter struct {
	since, until     time.Time
	direction, media string
	excludeGroups    bool
	topic            int
}

func (s *Server) readFilter(a arguments) (readFilter, error) {
	f, err := s.filter(a)
	if err != nil {
		return readFilter{}, err
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && !f.Since.Before(f.Until) {
		return readFilter{}, fmt.Errorf("since must precede until")
	}
	return readFilter{since: f.Since, until: f.Until, direction: f.Direction, media: f.MediaType, excludeGroups: a.ExcludeGroups, topic: a.TopicID}, nil
}
func (f readFilter) matches(m engine.Message) bool {
	if !f.since.IsZero() && m.Date.Before(f.since) || !f.until.IsZero() && !m.Date.Before(f.until) {
		return false
	}
	if f.direction == "in" && m.Out || f.direction == "out" && !m.Out {
		return false
	}
	if f.excludeGroups && m.Key.Peer.Type != engine.PeerUser {
		return false
	}
	if f.topic != 0 && m.TopicID != f.topic {
		return false
	}
	media := ""
	if m.Media != nil {
		media = string(m.Media.Type)
	}
	switch f.media {
	case "":
	case "text":
		if media != "" {
			return false
		}
	case "any":
		if media == "" {
			return false
		}
	case "image":
		if media != "photo" {
			return false
		}
	default:
		if media != f.media {
			return false
		}
	}
	return true
}
func (s *Server) telegramMessages(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	p, err := s.readPeer(ctx, a, true)
	if err != nil {
		return toolError("%v", err)
	}
	if a.Order != "" && a.Order != "oldest" && a.Order != "newest" {
		return toolError("order must be newest or oldest")
	}
	sh, err := newShaping(a, messageFields)
	if err != nil {
		return toolError("%v", err)
	}
	f, err := s.readFilter(a)
	if err != nil {
		return toolError("%v", err)
	}
	r, fail := s.readSnapshot(ctx)
	if fail != nil {
		return fail
	}
	if _, ok := r.peers[*p]; !ok {
		return engineError(engine.ErrPeerNotFound)
	}
	scope := r.scope(p, a.IncludeMigrated)
	msgs := make([]message, 0)
	for _, m := range r.messages {
		if scope[m.Key.Peer] && f.matches(m) {
			msgs = append(msgs, r.message(m))
		}
	}
	complete, oldest, err := s.readCoverage(ctx, r, p, scope)
	if err != nil {
		return engineError(err)
	}
	body := map[string]any{"chat_id": peerID(*p), "chat_name": peopleName(r.peers[*p]), "history_since": oldest, "coverage_complete": complete, "partial": !complete, "untrusted_content": UntrustedContent}
	if a.CountOnly {
		body["count"] = len(msgs)
		return textResult(body, false)
	}
	readOrder(msgs, a.Order == "oldest")
	n := limit(a.Limit, 100)
	body["truncated"] = len(msgs) > n
	if len(msgs) > n {
		msgs = msgs[:n]
	}
	if err := s.attachReadTranscripts(ctx, r.account, msgs); err != nil {
		return engineError(err)
	}
	body["messages"] = sh.messages(msgs)
	body["count"] = len(msgs)
	return textResult(body, false)
}

// SearchIndex is optional: persistent stores implement it with FTS5 (text and
// transcripts). The snapshot fallback is deliberately labelled full_text:false.
// It must apply account isolation and TTL just like ReadMessages.
type SearchIndex interface {
	SearchLocal(context.Context, engine.AccountID, string) ([]engine.Message, error)
}

func (s *Server) telegramSearch(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	if strings.TrimSpace(a.Query) == "" {
		return toolError("query is required")
	}
	p, err := s.readPeer(ctx, a, false)
	if err != nil {
		return toolError("%v", err)
	}
	sh, err := newShaping(a, messageFields)
	if err != nil {
		return toolError("%v", err)
	}
	f, err := s.readFilter(a)
	if err != nil {
		return toolError("%v", err)
	}
	r, fail := s.readSnapshot(ctx)
	if fail != nil {
		return fail
	}
	if p != nil {
		if _, ok := r.peers[*p]; !ok {
			return engineError(engine.ErrPeerNotFound)
		}
	}
	scope := r.scope(p, a.IncludeMigrated)
	complete, oldest, err := s.readCoverage(ctx, r, p, scope)
	if err != nil {
		return engineError(err)
	}
	rows := r.messages
	fullText := false
	if idx, ok := s.readStore.(SearchIndex); ok {
		rows, err = idx.SearchLocal(ctx, r.account, a.Query)
		if err != nil {
			return engineError(err)
		}
		fullText = true
	}
	selected := map[engine.MessageKey]message{}
	// Only keys in the safe current snapshot can be returned by an index.
	safe := map[engine.MessageKey]engine.Message{}
	for _, m := range r.messages {
		safe[m.Key] = m
	}
	for _, m := range rows {
		current, ok := safe[m.Key]
		if !ok || !scope[m.Key.Peer] || !f.matches(current) {
			continue
		}
		out := r.message(current)
		one := []message{out}
		if err := s.attachReadTranscripts(ctx, r.account, one); err != nil {
			return engineError(err)
		}
		out = one[0]
		text := out.Text
		if out.Transcript != nil {
			text += " " + out.Transcript.Text
		}
		if fullText || strings.Contains(strings.ToLower(text), strings.ToLower(a.Query)) {
			selected[m.Key] = out
		}
	}
	usedRemote := a.Remote && !complete
	if usedRemote {
		searchPeers := []*engine.PeerRef{nil}
		if p != nil {
			searchPeers = nil
			for ref := range scope {
				ref := ref
				searchPeers = append(searchPeers, &ref)
			}
			sort.Slice(searchPeers, func(i, j int) bool { return peerID(*searchPeers[i]) < peerID(*searchPeers[j]) })
		}
		for _, remotePeer := range searchPeers {
			media := engine.MediaType(f.media)
			if f.media == "image" {
				media = engine.MediaPhoto
			}
			if f.media == "text" || f.media == "any" {
				media = ""
			}
			result, err := s.eng.SearchRemote(ctx, engine.SearchRequest{Query: a.Query, Peer: remotePeer, Since: f.since, Until: f.until, Media: media, Limit: limit(a.Limit, 50)})
			if err != nil {
				return engineError(err)
			}
			for _, m := range result.Messages {
				if m.Key.Account != r.account {
					return engineError(engine.ErrAccountMismatch)
				}
				if p != nil && !scope[m.Key.Peer] || !f.matches(m) {
					continue
				}
				if m.ExpiresAt != nil && !m.ExpiresAt.After(s.clock()) {
					continue
				}
				if m.TTLPeriod > 0 && !m.Date.Add(time.Duration(m.TTLPeriod)*time.Second).After(s.clock()) {
					continue
				}
				// A server result must not reintroduce a stale local edit that no longer matches.
				if _, exists := safe[m.Key]; exists {
					continue
				}
				// A server result must not overwrite a newer local edit.
				if _, ok := selected[m.Key]; !ok {
					selected[m.Key] = r.message(m)
				}
			}
		}
	}
	if usedRemote {
		// Engine return values are transport DTOs, not proof that persistence accepted
		// them. Tombstones, TTL and newer edits remain authoritative after Apply.
		current, fail := s.readSnapshot(ctx)
		if fail != nil {
			return fail
		}
		visible := map[engine.MessageKey]engine.Message{}
		var freshMatches map[engine.MessageKey]bool
		if idx, ok := s.readStore.(SearchIndex); ok {
			hits, err := idx.SearchLocal(ctx, r.account, a.Query)
			if err != nil {
				return engineError(err)
			}
			freshMatches = map[engine.MessageKey]bool{}
			for _, hit := range hits {
				freshMatches[hit.Key] = true
			}
		}
		for _, m := range current.messages {
			visible[m.Key] = m
		}
		for key := range selected {
			m, ok := visible[key]
			if !ok || !f.matches(m) {
				delete(selected, key)
				continue
			}
			one := []message{current.message(m)}
			if err := s.attachReadTranscripts(ctx, current.account, one); err != nil {
				return engineError(err)
			}
			if freshMatches != nil {
				if !freshMatches[key] {
					delete(selected, key)
					continue
				}
			} else {
				text := one[0].Text
				if one[0].Transcript != nil {
					text += " " + one[0].Transcript.Text
				}
				if !strings.Contains(strings.ToLower(text), strings.ToLower(a.Query)) {
					delete(selected, key)
					continue
				}
			}
			selected[key] = one[0]
		}
	}
	msgs := make([]message, 0, len(selected))
	for _, m := range selected {
		msgs = append(msgs, m)
	}
	readOrder(msgs, false)
	n := limit(a.Limit, 50)
	truncated := len(msgs) > n
	if truncated {
		msgs = msgs[:n]
	}
	return textResult(map[string]any{"query": a.Query, "messages": sh.messages(msgs), "count": len(msgs), "full_text": fullText, "history_since": oldest, "coverage_complete": complete, "partial": !complete || usedRemote, "remote_used": usedRemote, "truncated": truncated, "untrusted_content": UntrustedContent}, false)
}

func (s *Server) telegramContext(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	sh, err := newShaping(a, messageFields)
	if err != nil {
		return toolError("%v", err)
	}
	if a.ChatJID != "" {
		p, err := s.resolvePeer(ctx, a.ChatJID)
		if err != nil {
			return engineError(err)
		}
		a.ChatJID = peerID(p)
	}
	key, fail := s.key(ctx, a)
	if fail != nil {
		return fail
	}
	r, fail := s.readSnapshot(ctx)
	if fail != nil {
		return fail
	}
	before, after := 5, 5
	if a.Before != nil {
		before = min(max(*a.Before, 0), 50)
	}
	if a.After != nil {
		after = min(max(*a.After, 0), 50)
	}
	msgs := make([]message, 0)
	for _, m := range r.messages {
		if m.Key.Peer == key.Peer {
			msgs = append(msgs, r.message(m))
		}
	}
	at := -1
	readOrder(msgs, true)
	for i, m := range msgs {
		if m.ID == fmt.Sprint(key.ID) {
			at = i
			break
		}
	}
	complete, oldest, err := s.readStore.ReadCoverage(ctx, r.account, &key.Peer)
	if err != nil {
		return engineError(err)
	}
	remote := false
	missing := at < 0 || (!complete && (at < before || len(msgs)-at-1 < after))
	if missing {
		if _, err := s.eng.FetchWindow(ctx, engine.WindowRequest{Key: key, Before: before, After: after}); err != nil {
			return engineError(err)
		}
		// Re-read committed visibility: Apply may have rejected a tombstoned row.
		current, fail := s.readSnapshot(ctx)
		if fail != nil {
			return fail
		}
		r = current
		msgs = msgs[:0]
		at = -1
		for _, m := range r.messages {
			if m.Key.Peer == key.Peer {
				msgs = append(msgs, r.message(m))
			}
		}
		readOrder(msgs, true)
		for i, m := range msgs {
			if m.ID == fmt.Sprint(key.ID) {
				at = i
				break
			}
		}
		complete, oldest, err = s.readStore.ReadCoverage(ctx, r.account, &key.Peer)
		if err != nil {
			return engineError(err)
		}
		remote = true
	}
	if at < 0 {
		return engineError(engine.ErrMessageNotFound)
	}
	if err := s.attachReadTranscripts(ctx, r.account, msgs); err != nil {
		return engineError(err)
	}
	return textResult(map[string]any{"chat_id": peerID(key.Peer), "chat_name": peopleName(r.peers[key.Peer]), "before": sh.messages(msgs[max(0, at-before):at]), "message": sh.message(msgs[at]), "after": sh.messages(msgs[at+1 : min(len(msgs), at+1+after)]), "remote_used": remote, "coverage_complete": complete, "history_since": oldest, "partial": !complete, "untrusted_content": UntrustedContent}, false)
}
