package mcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// Structural local seams intentionally exclude every Telegram write/refresh API.
// A configured ReadStore must own marks; never fall back to legacy state.db.
type triageMarkStore interface {
	ReadTriageMarks(context.Context, engine.AccountID) (map[engine.PeerRef]store.TriageMark, error)
	MarkTriageHandled(context.Context, engine.AccountID, engine.PeerRef, string, time.Time) error
	SnoozeTriage(context.Context, engine.AccountID, engine.PeerRef, string, time.Time, time.Time) error
	ClearTriageMark(context.Context, engine.AccountID, engine.PeerRef) error
}
type triageMentionStore interface {
	ListMentions(context.Context, engine.AccountID, store.MentionOptions) (store.MentionPage, error)
}

func (s *Server) triageMarks(ctx context.Context, account engine.AccountID) (map[engine.PeerRef]store.TriageMark, error) {
	st, ok := s.readStore.(triageMarkStore)
	if !ok {
		return nil, fmt.Errorf("local triage persistence is not configured")
	}
	return st.ReadTriageMarks(ctx, account)
}
func (s *Server) triageSince(a arguments) (time.Time, error) {
	if a.Since != "" {
		return parseWhen(a.Since, "since")
	}
	return s.clock().AddDate(0, 0, -30), nil
}
func triageAfter(a, b engine.Message) bool {
	return a.Date.After(b.Date) || a.Date.Equal(b.Date) && a.Key.ID > b.Key.ID
}

type triageThread struct {
	account engine.AccountID
	peer    engine.PeerRef
	topic   int
}
type triageAnswers map[triageThread][]engine.Message

func triageAnswerIndex(messages []engine.Message) triageAnswers {
	index := triageAnswers{}
	for _, m := range messages {
		if m.Out {
			key := triageThread{m.Key.Account, m.Key.Peer, m.TopicID}
			index[key] = append(index[key], m)
		}
	}
	for _, out := range index {
		sort.Slice(out, func(i, j int) bool { return triageAfter(out[j], out[i]) })
	}
	return index
}
func (answers triageAnswers) after(m engine.Message) (*time.Time, *engine.MessageKey) {
	out := answers[triageThread{m.Key.Account, m.Key.Peer, m.TopicID}]
	i := sort.Search(len(out), func(i int) bool { return triageAfter(out[i], m) })
	if i == len(out) {
		return nil, nil
	}
	at, key := out[i].Date, out[i].Key
	return &at, &key
}

// Real stores use the durable historical entity index. The standalone fake uses
// the same entity/reply semantics, never a textual @number search. Recalculate
// answer links against the active snapshot so a just-expired answer cannot hide
// an otherwise visible mention before the janitor has run.
func (s *Server) triageHistoricalMentions(ctx context.Context, r readSnapshot, since time.Time) ([]store.Mention, error) {
	var result []store.Mention
	answers := triageAnswerIndex(r.messages)
	if st, ok := s.readStore.(triageMentionStore); ok {
		opts := store.MentionOptions{MessageOptions: store.MessageOptions{Limit: 500, Since: since}}
		active := make(map[engine.MessageKey]engine.Message, len(r.messages))
		for _, m := range r.messages {
			active[m.Key] = m
		}
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			page, err := st.ListMentions(ctx, r.account, opts)
			if err != nil {
				return nil, err
			}
			for _, m := range page.Mentions {
				if m.Key.Account != r.account {
					return nil, engine.ErrAccountMismatch
				}
				current, ok := active[m.Key]
				if !ok || current.Out || current.Date.Before(since) {
					continue
				}
				m.Message = current
				// Reply-derived mentions disappear with a deleted or expired target,
				// even before the janitor rebuilds the durable relation.
				if m.ByReply {
					m.ByReply = false
					if current.ReplyTo != nil && !current.ReplyTo.ToStory && (current.ReplyTo.Peer == nil || *current.ReplyTo.Peer == current.Key.Peer) {
						key := current.Key
						key.ID = current.ReplyTo.ID
						if target, ok := active[key]; ok && target.Out {
							m.ByReply = true
						}
					}
				}
				if !m.ByName && !m.ByUsername && !m.ByReply && !m.ByFlag {
					continue
				}
				m.AnsweredAt, m.AnsweredBy = answers.after(current)
				result = append(result, m)
			}
			if page.Next == nil {
				break
			}
			if opts.Before != nil && page.Next.Key == opts.Before.Key && page.Next.Date.Equal(opts.Before.Date) {
				return nil, fmt.Errorf("mention pagination did not advance")
			}
			next := *page.Next
			opts.Before = &next
		}
	} else {
		account, err := s.eng.Account(ctx)
		if err != nil {
			return nil, err
		}
		if account.ID != r.account {
			return nil, engine.ErrAccountMismatch
		}
		usernames := []string{account.Username}
		for _, p := range r.peers {
			if p.Kind == engine.KindSelf || (p.Ref.Type == engine.PeerUser && p.Ref.ID == account.SelfID) {
				usernames = append(usernames, p.Username)
				usernames = append(usernames, p.Usernames...)
			}
		}
		byKey := map[engine.MessageKey]engine.Message{}
		for _, m := range r.messages {
			byKey[m.Key] = m
		}
		for _, m := range r.messages {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if m.Out || m.Date.Before(since) {
				continue
			}
			mention := store.Mention{Message: m, ByFlag: m.Mentioned, ServerUnread: m.MediaUnread}
			units := utf16.Encode([]rune(m.Text))
			for _, entity := range m.Entities {
				if entity.Type == engine.EntityMentionName && account.SelfID > 0 && entity.UserID == account.SelfID {
					mention.ByName = true
				}
				if entity.Type == engine.EntityMention && entity.Offset >= 0 && entity.Length > 0 && entity.Offset <= len(units) && entity.Length <= len(units)-entity.Offset {
					name := strings.TrimPrefix(string(utf16.Decode(units[entity.Offset:entity.Offset+entity.Length])), "@")
					for _, username := range usernames {
						if username != "" && strings.EqualFold(name, strings.TrimPrefix(username, "@")) {
							mention.ByUsername = true
						}
					}
				}
			}
			if m.ReplyTo != nil && !m.ReplyTo.ToStory && (m.ReplyTo.Peer == nil || *m.ReplyTo.Peer == m.Key.Peer) {
				key := m.Key
				key.ID = m.ReplyTo.ID
				if out, ok := byKey[key]; ok && out.Out {
					mention.ByReply = true
				}
			}
			if !mention.ByName && !mention.ByUsername && !mention.ByReply && !mention.ByFlag {
				continue
			}
			mention.AnsweredAt, mention.AnsweredBy = answers.after(m)
			result = append(result, mention)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].Date.Equal(result[j].Date) {
			return result[i].Date.After(result[j].Date)
		}
		if result[i].Key.Peer != result[j].Key.Peer {
			return peerID(result[i].Key.Peer) < peerID(result[j].Key.Peer)
		}
		return result[i].Key.ID > result[j].Key.ID
	})
	return result, nil
}

func (s *Server) triageMentions(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	since, err := s.triageSince(a)
	if err != nil {
		return toolError("%v", err)
	}
	peer, err := s.readPeer(ctx, a, false)
	if err != nil {
		return toolError("%v", err)
	}
	r, fail := s.readSnapshot(ctx)
	if fail != nil {
		return fail
	}
	mentions, err := s.triageHistoricalMentions(ctx, r, since)
	if err != nil {
		return engineError(err)
	}
	found := make([]map[string]any, 0)
	for _, m := range mentions {
		if peer != nil && m.Key.Peer != *peer || a.OnlyUnanswered && m.AnsweredAt != nil {
			continue
		}
		row := map[string]any{"message": r.message(m.Message)}
		row["answered"] = m.AnsweredAt != nil
		row["server_unread"] = m.ServerUnread
		row["by_name"], row["by_username"], row["by_reply"], row["by_flag"] = m.ByName, m.ByUsername, m.ByReply, m.ByFlag
		if m.AnsweredAt != nil {
			row["answered_at"], row["answered_by"] = m.AnsweredAt, m.AnsweredBy
		}
		found = append(found, row)
	}
	n := min(limit(a.Limit, 50), 200)
	truncated := len(found) > n
	if truncated {
		found = found[:n]
	}
	return s.peopleCoverage(ctx, r.account, peer, map[string]any{"mentions": found, "count": len(found), "truncated": truncated, "since": since, "note": "historical local Telegram entity mentions; answered means a later outgoing message in the same peer and topic, independent of server_unread"})
}

// Marks are local user choices. They remain usable with read-only, disconnected,
// logged-out, revoked and inaccessible peers, without resolving anything remotely.
func (s *Server) triageMarkTarget(ctx context.Context, a arguments) (engine.AccountID, engine.Peer, triageMarkStore, map[string]any) {
	peer, err := s.readPeer(ctx, a, true)
	if err != nil {
		return "", engine.Peer{}, nil, toolError("%v", err)
	}
	account, fail := s.peopleAccount(ctx)
	if fail != nil {
		return "", engine.Peer{}, nil, fail
	}
	st, ok := s.readStore.(triageMarkStore)
	if !ok {
		return "", engine.Peer{}, nil, toolError("local triage persistence is not configured")
	}
	peers, err := s.readStore.ReadPeers(ctx, account)
	if err != nil {
		return "", engine.Peer{}, nil, engineError(err)
	}
	for _, p := range peers {
		if p.Ref == *peer {
			return account, p, st, nil
		}
	}
	return "", engine.Peer{}, nil, engineError(engine.ErrPeerNotFound)
}
func (s *Server) triageMarkHandled(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	account, target, st, fail := s.triageMarkTarget(ctx, a)
	peer := target.Ref
	if fail != nil {
		return fail
	}
	now := s.clock()
	var err error
	if a.Clear {
		err = st.ClearTriageMark(ctx, account, peer)
	} else {
		err = st.MarkTriageHandled(ctx, account, peer, strings.TrimSpace(a.Note), now)
	}
	if err != nil {
		return engineError(err)
	}
	marks, err := st.ReadTriageMarks(ctx, account)
	if err != nil {
		return engineError(err)
	}
	mark, exists := marks[peer]
	if a.Clear && exists || !a.Clear && (mark.HandledAt == nil || !mark.HandledAt.Equal(now) || mark.SnoozeUntil != nil) {
		return toolError("local triage mark verification failed")
	}
	return textResult(map[string]any{"chat_id": peerID(peer), "handled": !a.Clear, "name": peopleName(target), "note": "kept on this computer only: nothing is sent and the other side sees nothing. A new incoming message brings this chat back", "cleared": a.Clear, "source": "telegram_local_store", "untrusted_content": UntrustedContent}, false)
}
func (s *Server) triageSnooze(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var until time.Time
	var err error
	if !a.Clear {
		until, err = parseWhen(a.Until, "until")
		if err != nil {
			return toolError("%v", err)
		}
		if !until.After(s.clock()) {
			return toolError("until must be in the future, e.g. 2026-10-01T09:00:00-03:00")
		}
	}
	account, target, st, fail := s.triageMarkTarget(ctx, a)
	peer := target.Ref
	if fail != nil {
		return fail
	}
	now := s.clock()
	if a.Clear {
		err = st.ClearTriageMark(ctx, account, peer)
	} else {
		err = st.SnoozeTriage(ctx, account, peer, strings.TrimSpace(a.Note), now, until)
	}
	if err != nil {
		return engineError(err)
	}
	marks, err := st.ReadTriageMarks(ctx, account)
	if err != nil {
		return engineError(err)
	}
	mark, exists := marks[peer]
	if a.Clear && exists || !a.Clear && (mark.SnoozeUntil == nil || !mark.SnoozeUntil.Equal(until) || mark.SnoozedAt == nil || !mark.SnoozedAt.Equal(now) || mark.HandledAt != nil) {
		return toolError("local triage snooze verification failed")
	}
	body := map[string]any{"chat_id": peerID(peer), "snoozed": !a.Clear, "name": peopleName(target), "note": "kept on this computer only. The chat comes back at that moment, or earlier if someone writes in it", "cleared": a.Clear, "source": "telegram_local_store", "untrusted_content": UntrustedContent}
	if !a.Clear {
		body["until"] = until
	}
	return textResult(body, false)
}
