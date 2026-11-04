package mcp

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
	"sort"
	"strconv"
	"strings"
	"time"
)

// These adapters deliberately retain the inherited local index, not a second
// Telegram store. call attaches an explicit migration warning to every result.
func (s *Server) listChats(ctx context.Context, a arguments) map[string]any {
	sh, err := newShaping(a, chatFields)
	if err != nil {
		return toolError("%v", err)
	}
	for _, f := range a.Fields {
		if f == "muted_until" {
			return toolError("not implemented yet: the inherited index adapter exposes mute state but not its exact expiry")
		}
	}
	rows, err := s.index.RecentChats(ctx, 500)
	if err != nil {
		return toolError("%v", err)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Pinned != rows[j].Pinned {
			return rows[i].Pinned
		}
		return rows[i].At.After(rows[j].At)
	})
	out := []map[string]any{}
	for _, c := range rows {
		if a.Search != "" && !strings.Contains(strings.ToLower(c.Name), strings.ToLower(a.Search)) {
			continue
		}
		row := map[string]any{"chat_id": c.JID, "name": c.Name, "kind": "dm", "last_message_ts": c.At, "unread_count": c.Unread, "unread": c.Unread > 0, "pinned": c.Pinned}
		if c.Group {
			row["kind"] = "group"
		}
		if state, ok := s.index.Chat(ctx, c.JID); ok {
			row["archived"] = state.Archived
			row["muted"] = state.Muted
			row["unread"] = state.MarkedUnread || state.Unread > 0
		} else {
			row["archived"] = nil
			row["muted"] = nil
		}
		if len(sh.fields) > 0 {
			row = sh.pick(row)
		}
		out = append(out, row)
		if len(out) >= limit(a.Limit, 50) {
			break
		}
	}
	return textResult(map[string]any{"chats": out, "count": len(out), "partial": len(rows) == 500, "selection_window": 500, "unavailable_fields": []string{"muted_until"}}, false)
}
func (s *Server) chatMessages(ctx context.Context, a arguments) map[string]any {
	if a.ChatJID == "" {
		return toolError("chat_id is required")
	}
	if a.CountOnly {
		return s.countChatMessages(ctx, a)
	}
	if a.Order != "" && a.Order != "newest" && a.Order != "oldest" {
		return toolError("order must be newest or oldest")
	}
	sh, err := newShaping(a, messageFields)
	if err != nil {
		return toolError("%v", err)
	}
	f, err := s.filter(a)
	if err != nil {
		return toolError("%v", err)
	}
	n := limit(a.Limit, 100)
	rows := make([]index.Row, 0, n)
	cursor := 0
	err = s.index.EachMessage(ctx, f, func(r index.Row) error {
		if len(rows) < n {
			rows = append(rows, r)
		} else if a.Order != "oldest" {
			rows[cursor] = r
			cursor = (cursor + 1) % n
		}
		return nil
	})
	if err != nil {
		return toolError("%v", err)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if a.Order == "oldest" {
			return rows[i].Timestamp.Before(rows[j].Timestamp)
		}
		return rows[i].Timestamp.After(rows[j].Timestamp)
	})
	msgs := make([]message, 0, len(rows))
	for _, r := range rows {
		m := rowMessage(r)
		s.name(ctx, &m)
		msgs = append(msgs, m)
	}
	s.attachTranscripts(ctx, msgs)
	return textResult(map[string]any{"chat_id": a.ChatJID, "messages": sh.messages(msgs), "count": len(msgs), "untrusted_content": UntrustedContent}, false)
}

const gapThreshold = 10 * time.Hour

func (s *Server) gapsWithin(ctx context.Context, since, until string) []index.Gap {
	if s.index == nil {
		return nil
	}
	gaps, err := s.index.Gaps(ctx, gapThreshold, 90*24*time.Hour, 50)
	if err != nil {
		return nil
	}
	from, _ := parseWhen(since, "since")
	to, _ := parseWhen(until, "until")
	if to.IsZero() {
		to = time.Now()
	}
	var out []index.Gap
	for _, g := range gaps {
		if g.Until.After(from) && g.From.Before(to) {
			out = append(out, g)
		}
	}
	return out
}
func (s *Server) searchMessages(ctx context.Context, a arguments) map[string]any {
	if strings.TrimSpace(a.Query) == "" {
		return toolError("query is required")
	}
	// The inherited index has no Telegram coverage declaration (decision D5).
	if a.Remote {
		return toolError("not implemented yet: remote search requires the Telegram store coverage adapter")
	}
	sh, err := newShaping(a, messageFields)
	if err != nil {
		return toolError("%v", err)
	}
	f, err := s.filter(a)
	if err != nil {
		return toolError("%v", err)
	}
	n := limit(a.Limit, 50)
	msgs := make([]message, 0, n)
	cursor, matches := 0, 0
	seen := map[string]bool{}
	err = s.index.EachMessage(ctx, f, func(r index.Row) error {
		if strings.Contains(strings.ToLower(r.Text), strings.ToLower(a.Query)) {
			matches++
			m := rowMessage(r)
			if len(msgs) < n {
				msgs = append(msgs, m)
			} else {
				msgs[cursor] = m
				cursor = (cursor + 1) % n
			}
		}
		return nil
	})
	for i := range msgs {
		s.name(ctx, &msgs[i])
		seen[msgs[i].ChatJID+"/"+msgs[i].ID] = true
	}
	if err != nil {
		return toolError("%v", err)
	}
	if s.state != nil {
		if ts, err := s.state.SearchTranscripts(ctx, a.Query, a.ChatJID, n); err == nil {
			for _, t := range ts {
				if seen[t.ChatJID+"/"+t.MessageID] {
					continue
				}
				m, err := s.index.MessageByID(ctx, t.MessageID, t.ChatJID)
				if err != nil {
					continue
				}
				if !f.Since.IsZero() && m.Timestamp.Before(f.Since) || !f.Until.IsZero() && m.Timestamp.After(f.Until) {
					continue
				}
				msgs = append(msgs, message{ID: m.ID, ChatJID: m.ChatJID, ChatName: m.ChatName, Timestamp: m.Timestamp, FromMe: m.FromMe, MediaType: m.MediaType, Transcript: &t})
			}
		}
	}
	sort.SliceStable(msgs, func(i, j int) bool { return msgs[i].Timestamp.After(msgs[j].Timestamp) })
	truncated := matches > n || len(msgs) > n
	if len(msgs) > n {
		msgs = msgs[:n]
	}
	s.attachTranscripts(ctx, msgs)
	return textResult(map[string]any{"query": a.Query, "messages": sh.messages(msgs), "count": len(msgs), "full_text": false, "truncated": truncated, "untrusted_content": UntrustedContent}, false)
}
func (s *Server) listContacts(ctx context.Context, a arguments) map[string]any {
	rows, err := s.index.Contacts(ctx, a.Search, limit(a.Limit, 100))
	if err != nil {
		return toolError("%v", err)
	}
	if rows == nil {
		rows = []index.Contact{}
	}
	return textResult(map[string]any{"contacts": rows, "count": len(rows)}, false)
}
func (s *Server) listGroups(ctx context.Context, a arguments) map[string]any {
	rows, err := s.index.Groups(ctx, a.Search, limit(a.Limit, 100))
	if err != nil {
		return toolError("%v", err)
	}
	if rows == nil {
		rows = []index.Group{}
	}
	return textResult(map[string]any{"groups": rows, "count": len(rows)}, false)
}

// message is local-only. Remote operations use key, never an ID-only fallback.
func (s *Server) message(ctx context.Context, a arguments) (index.Message, map[string]any) {
	if s.index == nil {
		return index.Message{}, toolError("not implemented yet: inherited local index is not configured")
	}
	// Local records retain legacy identifiers until the store migration. They
	// may be used for cached transcripts/corrections, never remote operations.
	chat, id := a.ChatJID, a.MessageID
	if strings.Contains(id, "/") {
		k, err := parseMessageKey(a, "")
		if err != nil {
			return index.Message{}, toolError("%v", err)
		}
		chat, id = peerID(k.Peer), strconv.Itoa(k.ID)
	}
	if chat == "" || id == "" {
		return index.Message{}, toolError("chat_id and message_id are required for local lookup")
	}
	m, err := s.index.MessageByID(ctx, id, chat)
	if err != nil {
		return m, toolError("%v", err)
	}
	return m, nil
}
