package gotdengine

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"sort"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

const writePreviewTTL = 10 * time.Minute

// writePeer resolves exclusively from persisted data. In particular, previews
// never resolve usernames on the network or silently follow migrated groups.
func (e *Engine) writePeer(ctx context.Context, ref engine.PeerRef) (engine.Peer, tg.InputPeerClass, error) {
	if ref.ID <= 0 || (ref.Type != engine.PeerUser && ref.Type != engine.PeerChat && ref.Type != engine.PeerChannel) {
		return engine.Peer{}, nil, engine.ErrInvalid
	}
	p, ok, err := e.cfg.Store.LookupPeer(ctx, e.cfg.Account, ref)
	if err != nil {
		return engine.Peer{}, nil, err
	}
	a, err := e.Account(ctx)
	if err != nil {
		return engine.Peer{}, nil, err
	}
	if !ok {
		if ref.Type != engine.PeerUser || ref.ID != a.SelfID {
			return engine.Peer{}, nil, engine.ErrPeerNotFound
		}
		p = engine.Peer{Ref: ref, Kind: engine.KindSelf, FirstName: a.FirstName, LastName: a.LastName, Username: a.Username, Access: engine.AccessOK}
	}
	if p.Ref != ref {
		return engine.Peer{}, nil, engine.ErrPeerNotFound
	}
	if p.Access != "" && p.Access != engine.AccessOK {
		return engine.Peer{}, nil, engine.ErrNoAccess
	}
	ip, err := e.inputPeer(ctx, ref)
	if err != nil {
		return engine.Peer{}, nil, err
	}
	return p, ip, nil
}

// checkWritePreview retains expired and consumed fingerprints as tombstones.
// Deleting or overwriting them would let the same deterministic preview be
// revived by another dry run. A new intent may use a new idempotency key.
func (e *Engine) checkWritePreview(opts engine.WriteOptions, p *engine.Preview) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if opts.DryRun {
		if e.status.Mode == engine.ModeReadOnly {
			p.Warnings = append(p.Warnings, "read-only mode: this action would be refused")
		}
		if e.previews == nil {
			e.previews = make(map[string]time.Time)
		}
		if _, exists := e.previews[p.Fingerprint]; !exists {
			e.previews[p.Fingerprint] = e.now().Add(writePreviewTTL)
		}
		return true, nil
	}
	if e.status.Mode == engine.ModeReadOnly {
		return false, engine.ErrReadOnly
	}
	if opts.ExpectFingerprint == "" || opts.ExpectFingerprint != p.Fingerprint {
		return false, engine.ErrPreviewMismatch
	}
	expires, ok := e.previews[p.Fingerprint]
	if !ok || !expires.After(e.now()) {
		return false, engine.ErrPreviewMismatch
	}
	// Consume before the RPC: an ambiguous network failure must not grant a second
	// authorization. The idempotency key still protects a newly approved retry.
	e.previews[p.Fingerprint] = time.Time{}
	return false, nil
}

func canonicalWriteFingerprint(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", engine.ErrInvalid
	}
	return fingerprint(string(b)), nil
}

func writeUTF16Len(s string) int      { return len(utf16.Encode([]rune(s))) }
func validWriteMessageID(id int) bool { return id >= 0 && int64(id) <= 1<<31-1 }

type resolvedMention struct {
	Peer   engine.Peer
	Input  tg.InputUserClass
	Entity engine.Entity
}

func (e *Engine) writeMentions(ctx context.Context, text string, mentions []engine.Mention) (string, []resolvedMention, error) {
	var resolved []resolvedMention
	// Claim distinct non-overlapping spans, including when equal labels refer to
	// different users. Never send overlapping mention_name entities.
	type span struct{ start, end int }
	var claimed []span
	for _, m := range mentions {
		if m.User.Type != engine.PeerUser {
			return "", nil, engine.ErrInvalid
		}
		p, ip, err := e.writePeer(ctx, m.User)
		if err != nil {
			return "", nil, err
		}
		var user tg.InputUserClass
		switch v := ip.(type) {
		case *tg.InputPeerSelf:
			user = &tg.InputUserSelf{}
		case *tg.InputPeerUser:
			user = &tg.InputUser{UserID: v.UserID, AccessHash: v.AccessHash}
		default:
			return "", nil, engine.ErrInvalid
		}
		label := m.Text
		if label == "" {
			if p.Username != "" {
				label = "@" + p.Username
			} else {
				label = strings.TrimSpace(p.FirstName + " " + p.LastName)
			}
		}
		if label == "" || !utf8.ValidString(label) {
			return "", nil, engine.ErrInvalid
		}
		index := -1
		for from := 0; from <= len(text); {
			next := strings.Index(text[from:], label)
			if next < 0 {
				break
			}
			next += from
			overlaps := false
			for _, s := range claimed {
				if next < s.end && next+len(label) > s.start {
					overlaps = true
					break
				}
			}
			if !overlaps {
				index = next
				break
			}
			_, width := utf8.DecodeRuneInString(text[next:])
			from = next + width
		}
		if index < 0 {
			// Explicit labels must already be present: they are a substring to link.
			// Default labels may be appended, matching Mention's convenience contract.
			if m.Text != "" {
				return "", nil, engine.ErrInvalid
			}
			if text != "" {
				text += " "
			}
			index = len(text)
			text += label
		}
		claimed = append(claimed, span{index, index + len(label)})
		resolved = append(resolved, resolvedMention{Peer: p, Input: user, Entity: engine.Entity{Type: engine.EntityMentionName, Offset: writeUTF16Len(text[:index]), Length: writeUTF16Len(label), UserID: m.User.ID}})
	}
	sort.SliceStable(resolved, func(i, j int) bool { return resolved[i].Entity.Offset < resolved[j].Entity.Offset })
	return text, resolved, nil
}

func (e *Engine) SendText(ctx context.Context, r engine.SendTextRequest) (engine.WriteResult, error) {
	var zero engine.WriteResult
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !r.DryRun && e.Mode() == engine.ModeReadOnly {
		return zero, engine.ErrReadOnly
	}
	if !utf8.ValidString(r.Text) || strings.TrimSpace(r.Text) == "" || writeUTF16Len(r.Text) > 4096 || !validWriteMessageID(r.ReplyTo) || !validWriteMessageID(r.TopicID) {
		return zero, engine.ErrInvalid
	}
	target, ip, err := e.writePeer(ctx, r.Peer)
	if err != nil {
		return zero, err
	}
	if r.TopicID > 0 && (r.Peer.Type != engine.PeerChannel || !target.Forum) {
		return zero, engine.ErrInvalid
	}
	text, mentions, err := e.writeMentions(ctx, r.Text, r.Mentions)
	if err != nil {
		return zero, err
	}
	if writeUTF16Len(text) > 4096 {
		return zero, engine.ErrInvalid
	}
	var quoted *engine.Message
	if r.ReplyTo > 0 {
		key := engine.MessageKey{Account: e.cfg.Account, Peer: r.Peer, ID: r.ReplyTo}
		m, ok, err := e.cfg.Store.Message(ctx, key)
		if err != nil {
			return zero, err
		}
		if !ok || m.Key != key || (m.ExpiresAt != nil && !m.ExpiresAt.After(e.now())) {
			return zero, engine.ErrMessageNotFound
		}
		if r.TopicID > 0 && m.TopicID > 0 && m.TopicID != r.TopicID {
			return zero, engine.ErrInvalid
		}
		quoted = &m
	}
	p := &engine.Preview{Action: "send_text", Target: target, Text: text, Quoted: quoted, Reversible: true}
	var entities []tg.MessageEntityClass
	for _, m := range mentions {
		p.Entities = append(p.Entities, m.Entity)
		p.Users = append(p.Users, m.Peer)
		entities = append(entities, &tg.InputMessageEntityMentionName{Offset: m.Entity.Offset, Length: m.Entity.Length, UserID: m.Input})
	}
	// Bind both what was requested and what was resolved: hashes are internal
	// only, while the full quoted snapshot (including edit date) binds its version.
	// DryRun and ExpectFingerprint are authorization metadata, not intent.
	canonical := struct {
		Action            string
		Account           engine.AccountID
		SelfID            int64
		Target            engine.Peer
		Input             tg.InputPeerClass
		Text              string
		OriginalText      string
		ReplyTo           int
		TopicID           int
		Quoted            *engine.Message
		RequestedMentions []engine.Mention
		Mentions          []resolvedMention
		Silent            bool
		NoWebPage         bool
		Key               string
	}{Action: p.Action, Account: e.cfg.Account, Target: target, Input: ip, Text: text, OriginalText: r.Text, ReplyTo: r.ReplyTo, TopicID: r.TopicID, Quoted: quoted, RequestedMentions: r.Mentions, Mentions: mentions, Silent: r.Silent, NoWebPage: r.NoWebPage, Key: r.IdempotencyKey}
	a, err := e.Account(ctx)
	if err != nil {
		return zero, err
	}
	canonical.SelfID = a.SelfID
	p.Fingerprint, err = canonicalWriteFingerprint(canonical)
	if err != nil {
		return zero, err
	}
	if !r.DryRun {
		if err := e.require(); err != nil {
			return zero, err
		}
	}
	dry, err := e.checkWritePreview(r.WriteOptions, p)
	if err != nil {
		return zero, err
	}
	if dry {
		return engine.WriteResult{DryRun: true, Preview: p}, nil
	}
	randomID, err := e.sendRandomID(r.IdempotencyKey)
	if err != nil {
		return zero, err
	}
	req := &tg.MessagesSendMessageRequest{Peer: ip, Message: text, RandomID: randomID, Entities: entities, Silent: r.Silent, NoWebpage: r.NoWebPage}
	if r.ReplyTo > 0 || r.TopicID > 0 {
		reply := &tg.InputReplyToMessage{ReplyToMsgID: r.ReplyTo}
		if reply.ReplyToMsgID == 0 {
			reply.ReplyToMsgID = r.TopicID
		}
		if r.TopicID > 0 && r.TopicID != 1 && reply.ReplyToMsgID != r.TopicID {
			reply.SetTopMsgID(r.TopicID)
		}
		req.ReplyTo = reply
	}
	got, err := e.api.MessagesSendMessage(ctx, req)
	if err != nil {
		return zero, rpcError(err)
	}
	if short, ok := got.(*tg.UpdateShortSentMessage); ok {
		self := engine.PeerRef{Type: engine.PeerUser, ID: a.SelfID}
		m := engine.Message{Key: engine.MessageKey{Account: e.cfg.Account, Peer: r.Peer, ID: short.ID}, Date: time.Unix(int64(short.Date), 0), Text: text, Entities: p.Entities, Out: true, Sender: &self, TopicID: r.TopicID, Silent: r.Silent, TTLPeriod: short.TTLPeriod, Protected: target.Protected}
		if target.Kind == engine.KindChannel {
			m.Post = true
		}
		if reply, ok := req.ReplyTo.(*tg.InputReplyToMessage); ok {
			m.ReplyTo = &engine.ReplyRef{ID: reply.ReplyToMsgID, TopID: r.TopicID}
		}
		if short.TTLPeriod > 0 {
			expires := m.Date.Add(time.Duration(short.TTLPeriod) * time.Second)
			m.ExpiresAt = &expires
		}
		result := engine.WriteResult{Done: true, Affected: 1, Messages: []engine.MessageKey{m.Key}, Note: "accepted by Telegram; not delivery or read confirmation"}
		if _, err := e.apply(ctx, engine.Batch{Account: e.cfg.Account, Source: engine.SourceSend, Messages: []engine.Message{m}}); err != nil {
			return acceptedWriteError(result, err)
		}
		return acceptedWriteError(result, e.manager.HandleAffected(ctx, 0, short.Pts, short.PtsCount))
	}
	result := engine.WriteResult{Done: true, Affected: 1, Messages: e.acceptedMessageKeys(got, r.Peer, map[int64]bool{randomID: true}), Note: "accepted by Telegram; not delivery or read confirmation"}
	// A running manager enqueues updates without waiting for local persistence.
	// Commit the RPC snapshot before returning, then let the unchanged response
	// advance the manager's ordered checkpoint through an idempotent replay.
	if err := e.handle(ctx, got); err != nil {
		return acceptedWriteError(result, err)
	}
	return acceptedWriteError(result, e.manager.Handle(ctx, got))
}

func (e *Engine) sendRandomID(key string) (int64, error) {
	if key != "" {
		// Length-delimited encoding avoids account/key delimiter collisions.
		b, err := json.Marshal(struct {
			Account engine.AccountID
			Key     string
		}{e.cfg.Account, key})
		if err != nil {
			return 0, err
		}
		sum := sha256.Sum256(b)
		return int64(binary.LittleEndian.Uint64(sum[:8])), nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(b[:])), nil
}

// MarkRead is the only entry point allowed to emit readHistory. Merely reading
// local data, resolving peers, or generating a preview never sends a receipt.
func (e *Engine) MarkRead(ctx context.Context, r engine.MarkReadRequest) (engine.WriteResult, error) {
	var zero engine.WriteResult
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if !r.DryRun && e.Mode() == engine.ModeReadOnly {
		return zero, engine.ErrReadOnly
	}
	if !validWriteMessageID(r.MaxID) {
		return zero, engine.ErrInvalid
	}
	target, ip, err := e.writePeer(ctx, r.Peer)
	if err != nil {
		return zero, err
	}
	p := &engine.Preview{Action: "mark_read", Target: target, Reversible: false, Warnings: []string{"sends a read receipt; this cannot be undone"}}
	if r.MaxID == 0 {
		p.Warnings = append(p.Warnings, "marks all messages read, including messages arriving before confirmation")
	} else {
		p.Affects = []engine.MessageKey{{Account: e.cfg.Account, Peer: r.Peer, ID: r.MaxID}}
	}
	a, err := e.Account(ctx)
	if err != nil {
		return zero, err
	}
	p.Fingerprint, err = canonicalWriteFingerprint(struct {
		Action  string
		Account engine.AccountID
		SelfID  int64
		Target  engine.Peer
		Input   tg.InputPeerClass
		MaxID   int
		Key     string
	}{p.Action, e.cfg.Account, a.SelfID, target, ip, r.MaxID, r.IdempotencyKey})
	if err != nil {
		return zero, err
	}
	if !r.DryRun {
		if err := e.require(); err != nil {
			return zero, err
		}
	}
	dry, err := e.checkWritePreview(r.WriteOptions, p)
	if err != nil {
		return zero, err
	}
	if dry {
		return engine.WriteResult{DryRun: true, Preview: p}, nil
	}
	switch v := ip.(type) {
	case *tg.InputPeerChannel:
		accepted, err := e.api.ChannelsReadHistory(ctx, &tg.ChannelsReadHistoryRequest{Channel: &tg.InputChannel{ChannelID: v.ChannelID, AccessHash: v.AccessHash}, MaxID: r.MaxID})
		if err != nil {
			return zero, rpcError(err)
		}
		if !accepted {
			return zero, engine.ErrPermission
		}
	default:
		affected, err := e.api.MessagesReadHistory(ctx, &tg.MessagesReadHistoryRequest{Peer: ip, MaxID: r.MaxID})
		if err != nil {
			return zero, rpcError(err)
		}
		if err := e.manager.HandleAffected(ctx, 0, affected.Pts, affected.PtsCount); err != nil {
			return acceptedWriteError(engine.WriteResult{Done: true, Note: "read receipt accepted by Telegram; local counters follow server updates"}, err)
		}
	}
	// Do not fabricate unread counts or peer-read events from an RPC receipt.
	// The authoritative inbox marker/counts arrive through the updates manager.
	return engine.WriteResult{Done: true, Note: "read receipt accepted by Telegram; local counters follow server updates"}, nil
}
