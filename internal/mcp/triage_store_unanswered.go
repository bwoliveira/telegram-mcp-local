package mcp

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

type triageChatSnapshot struct {
	latest   engine.Message
	incoming []engine.Message
	lastSent engine.Message
}

func triageSnapshots(r readSnapshot) map[engine.PeerRef]*triageChatSnapshot {
	chats := map[engine.PeerRef]*triageChatSnapshot{}
	for _, m := range r.messages {
		c := chats[m.Key.Peer]
		if c == nil {
			c = &triageChatSnapshot{}
			chats[m.Key.Peer] = c
		}
		if c.latest.Key.ID == 0 || triageAfter(m, c.latest) {
			c.latest = m
		}
		if m.Out {
			if c.lastSent.Key.ID == 0 || triageAfter(m, c.lastSent) {
				c.lastSent = m
			}
		} else {
			c.incoming = append(c.incoming, m)
		}
	}
	for _, c := range chats {
		sort.Slice(c.incoming, func(i, j int) bool { return triageAfter(c.incoming[i], c.incoming[j]) })
	}
	return chats
}
func triageWaiting(c *triageChatSnapshot) (int, time.Time) {
	count := 0
	var first time.Time
	for _, m := range c.incoming {
		if c.lastSent.Key.ID != 0 && !triageAfter(m, c.lastSent) {
			continue
		}
		count++
		if first.IsZero() || m.Date.Before(first) {
			first = m.Date
		}
	}
	return count, first
}
func triageHidden(mark store.TriageMark, c *triageChatSnapshot, now time.Time) bool {
	if len(c.incoming) == 0 {
		return false
	}
	return mark.Hides(c.incoming[0].Date, now)
}
func triagePreview(r readSnapshot, m engine.Message) preview {
	msg := r.message(m)
	return preview{ID: msg.ID, Timestamp: msg.Timestamp, Sender: msg.SenderName, SenderJID: msg.SenderJID, Text: clip(msg.Text, 300), MediaType: msg.MediaType}
}
func (s *Server) triageUnanswered(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	since, err := s.triageSince(a)
	if err != nil {
		return toolError("%v", err)
	}
	if a.MinAgeHours < 0 || math.IsNaN(a.MinAgeHours) || math.IsInf(a.MinAgeHours, 0) {
		return toolError("min_age_hours must be finite and zero or positive")
	}
	r, fail := s.readSnapshot(ctx)
	if fail != nil {
		return fail
	}
	marks, err := s.triageMarks(ctx, r.account)
	if err != nil {
		return engineError(err)
	}
	dialogs, err := s.readStore.ReadDialogs(ctx, r.account)
	if err != nil {
		return engineError(err)
	}
	account, err := s.eng.Account(ctx)
	if err != nil {
		return engineError(err)
	}
	if account.ID != r.account {
		return engineError(engine.ErrAccountMismatch)
	}
	own := []string{strings.ToLower(account.Username)}
	for _, p := range r.peers {
		if p.Kind == engine.KindSelf || (p.Ref.Type == engine.PeerUser && p.Ref.ID == account.SelfID) {
			own = append(own, strings.ToLower(p.Username))
			for _, u := range p.Usernames {
				own = append(own, strings.ToLower(u))
			}
		}
	}
	states := map[engine.PeerRef]engine.Dialog{}
	for _, d := range dialogs {
		states[d.Peer] = d
	}
	chats := triageSnapshots(r)
	now := s.clock()
	includeMuted := a.IncludeMuted == nil || *a.IncludeMuted
	eligible := func(ref engine.PeerRef, c *triageChatSnapshot) bool {
		p := r.peers[ref]
		if !a.IncludeBots && ref.Type == engine.PeerUser && p.Kind == engine.KindBot {
			return false
		}
		if p.Kind == engine.KindSelf || p.Kind == engine.KindChannel || (ref.Type == engine.PeerUser && ref.ID == account.SelfID) {
			return false
		}
		d := states[ref]
		if !a.IncludeArchived && d.FolderID == 1 || !includeMuted && d.MuteUntil != nil && d.MuteUntil.After(now) {
			return false
		}
		return a.IncludeHandled || !triageHidden(marks[ref], c, now)
	}
	type waiting struct {
		row  map[string]any
		last time.Time
		peer engine.PeerRef
	}
	found := make([]waiting, 0)
	included := map[engine.PeerRef]bool{}
	add := func(ref engine.PeerRef, c *triageChatSnapshot, mention *engine.Message) {
		p, d := r.peers[ref], states[ref]
		count, first := triageWaiting(c)
		row := map[string]any{"chat_id": peerID(ref), "name": peopleName(p), "group": ref.Type != engine.PeerUser, "last_message": triagePreview(r, c.latest), "waiting_messages": count, "age_hours": roundHours(now.Sub(c.latest.Date)), "access": p.Access}
		if !first.IsZero() {
			row["waiting_since"] = first
		}
		if mention != nil {
			row["mention"] = triagePreview(r, *mention)
		}
		if d.MuteUntil != nil && d.MuteUntil.After(now) {
			row["muted"] = true
		}
		if triageHidden(marks[ref], c, now) {
			row["handled"] = true
		}
		found = append(found, waiting{row: row, last: c.latest.Date, peer: ref})
		included[ref] = true
	}
	refs := make([]engine.PeerRef, 0, len(chats))
	for ref := range chats {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := chats[refs[i]].latest, chats[refs[j]].latest
		if !a.Date.Equal(b.Date) {
			return a.Date.After(b.Date)
		}
		return peerID(refs[i]) < peerID(refs[j])
	})
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return engineError(err)
		}
		c := chats[ref]
		if c.latest.Date.Before(since) || c.latest.Out || ref.Type != engine.PeerUser && !a.IncludeGroups || !eligible(ref, c) {
			continue
		}
		media := ""
		if c.latest.Media != nil {
			media = string(c.latest.Media.Type)
		}
		if (a.IgnoreClosing == nil || *a.IgnoreClosing) && isClosing(index.Row{Text: c.latest.Text, MediaType: media}, own) {
			continue
		}
		if a.MinAgeHours > 0 && now.Sub(c.latest.Date).Hours() < a.MinAgeHours {
			continue
		}
		add(ref, c, nil)
	}
	if a.IncludeGroupMentions && !a.IncludeGroups {
		mentions, err := s.triageHistoricalMentions(ctx, r, since)
		if err != nil {
			return engineError(err)
		}
		for _, m := range mentions {
			ref := m.Key.Peer
			c := chats[ref]
			if c == nil || included[ref] || ref.Type == engine.PeerUser || m.AnsweredAt != nil || !eligible(ref, c) {
				continue
			}
			mention := m.Message
			add(ref, c, &mention)
		}
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].last.Equal(found[j].last) {
			return found[i].last.After(found[j].last)
		}
		return peerID(found[i].peer) < peerID(found[j].peer)
	})
	n := min(limit(a.Limit, 30), 200)
	truncated := len(found) > n
	if truncated {
		found = found[:n]
	}
	rows := make([]map[string]any, 0, len(found))
	for _, item := range found {
		rows = append(rows, item.row)
	}
	return s.peopleCoverage(ctx, r.account, nil, map[string]any{"chats": rows, "count": len(rows), "truncated": truncated, "since": since, "note": "a chat waits when its latest message came from the other side. mark_handled or snooze_chat take one off this list until someone writes in it again"})
}
