package gotdengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

func (e *Engine) messageActionGate(ctx context.Context, opts engine.WriteOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !opts.DryRun && e.Mode() == engine.ModeReadOnly {
		return engine.ErrReadOnly
	}
	return nil
}

// messageActionPreview binds the persisted identity, native access hash, full
// message versions and action-specific intent. Authorization metadata is not intent.
func (e *Engine) messageActionPreview(ctx context.Context, opts engine.WriteOptions, p *engine.Preview, ip tg.InputPeerClass, intent any) (bool, error) {
	a, err := e.Account(ctx)
	if err != nil {
		return false, err
	}
	p.Fingerprint, err = canonicalWriteFingerprint(struct {
		Action  string
		Account engine.AccountID
		SelfID  int64
		Target  engine.Peer
		Input   tg.InputPeerClass
		Intent  any
		Key     string
	}{p.Action, e.cfg.Account, a.SelfID, p.Target, ip, intent, opts.IdempotencyKey})
	if err != nil {
		return false, err
	}
	if !opts.DryRun {
		if err := e.require(); err != nil {
			return false, err
		}
	}
	return e.checkWritePreview(opts, p)
}

func (e *Engine) actionMessage(ctx context.Context, key engine.MessageKey) (engine.Message, error) {
	if key.Account != e.cfg.Account {
		return engine.Message{}, engine.ErrAccountMismatch
	}
	if key.ID <= 0 || !validWriteMessageID(key.ID) {
		return engine.Message{}, engine.ErrInvalid
	}
	m, ok, err := e.cfg.Store.Message(ctx, key)
	if err != nil {
		return engine.Message{}, err
	}
	if !ok || m.Key != key || (m.ExpiresAt != nil && !m.ExpiresAt.After(e.now())) {
		return engine.Message{}, engine.ErrMessageNotFound
	}
	return m, nil
}

func (e *Engine) actionMessages(ctx context.Context, ref engine.PeerRef, ids []int) ([]engine.Message, error) {
	if len(ids) == 0 || len(ids) > 100 {
		return nil, engine.ErrInvalid
	}
	seen := map[int]bool{}
	var messages []engine.Message
	for _, id := range ids {
		if seen[id] {
			return nil, engine.ErrInvalid
		}
		seen[id] = true
		m, err := e.actionMessage(ctx, engine.MessageKey{Account: e.cfg.Account, Peer: ref, ID: id})
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, nil
}

func actionAffects(p *engine.Preview, messages []engine.Message) {
	for _, m := range messages {
		p.Affects = append(p.Affects, m.Key)
	}
	if len(messages) > 0 {
		m := messages[0]
		p.Quoted = &m
	}
}

// acceptedWriteError keeps the RPC receipt separate from local reconciliation.
// Done describes Telegram acceptance, not persistence or delivery. Preserve the
// wrapped cause so callers can still inspect storage/context errors with Is/As.
func acceptedWriteError(result engine.WriteResult, err error) (engine.WriteResult, error) {
	if err == nil {
		return result, nil
	}
	const warning = "Telegram accepted the write but local reconciliation failed; do not blindly retry"
	if result.Note != "" {
		result.Note += "; "
	}
	result.Note += warning
	return result, fmt.Errorf("%s: %w", warning, err)
}

func (e *Engine) finishMessageAction(ctx context.Context, u tg.UpdatesClass, affected int) (engine.WriteResult, error) {
	result := engine.WriteResult{Done: true, Affected: affected, Note: "accepted by Telegram; local state follows server updates"}
	// A running manager can acknowledge enqueueing before its worker commits.
	// Persist the authoritative RPC response first, without advancing PTS/SEQ.
	if err := e.handle(ctx, u); err != nil {
		return acceptedWriteError(result, err)
	}
	// Preserve all ordering fields, but do not apply stateless reactions twice:
	// the worker may drain this response after a later removal has committed.
	return acceptedWriteError(result, e.manager.Handle(ctx, withoutActionReactionReplay(u)))
}

// Extract server-assigned IDs before ingestion: that receipt remains useful even
// if the local store rejects the updates. Never guess IDs from unrelated updates.
func (e *Engine) acceptedMessageKeys(u tg.UpdatesClass, peer engine.PeerRef, ids map[int64]bool) []engine.MessageKey {
	var updates []tg.UpdateClass
	switch v := u.(type) {
	case *tg.Updates:
		updates = v.Updates
	case *tg.UpdatesCombined:
		updates = v.Updates
	case *tg.UpdateShort:
		updates = []tg.UpdateClass{v.Update}
	}
	var keys []engine.MessageKey
	for _, update := range updates {
		if id, ok := update.(*tg.UpdateMessageID); ok && ids[id.RandomID] {
			keys = append(keys, engine.MessageKey{Account: e.cfg.Account, Peer: peer, ID: id.ID})
		}
	}
	return keys
}

func (e *Engine) Edit(ctx context.Context, r engine.EditRequest) (engine.WriteResult, error) {
	var zero engine.WriteResult
	if err := e.messageActionGate(ctx, r.WriteOptions); err != nil {
		return zero, err
	}
	if !utf8.ValidString(r.Text) || strings.TrimSpace(r.Text) == "" || writeUTF16Len(r.Text) > 4096 {
		return zero, engine.ErrInvalid
	}
	m, err := e.actionMessage(ctx, r.Key)
	if err != nil {
		return zero, err
	}
	if !m.Out {
		return zero, engine.ErrPermission
	}
	target, ip, err := e.writePeer(ctx, r.Key.Peer)
	if err != nil {
		return zero, err
	}
	text, mentions, err := e.writeMentions(ctx, r.Text, r.Mentions)
	if err != nil {
		return zero, err
	}
	limit := 4096
	if m.Media != nil {
		limit = 1024
	}
	if writeUTF16Len(text) > limit {
		return zero, engine.ErrInvalid
	}
	p := &engine.Preview{Action: "edit", Target: target, Text: text, Quoted: &m, Affects: []engine.MessageKey{m.Key}, Reversible: true}
	req := &tg.MessagesEditMessageRequest{Peer: ip, ID: r.Key.ID}
	req.SetMessage(text)
	var entities []tg.MessageEntityClass
	for _, mention := range mentions {
		p.Entities = append(p.Entities, mention.Entity)
		p.Users = append(p.Users, mention.Peer)
		entities = append(entities, &tg.InputMessageEntityMentionName{Offset: mention.Entity.Offset, Length: mention.Entity.Length, UserID: mention.Input})
	}
	req.SetEntities(entities)
	dry, err := e.messageActionPreview(ctx, r.WriteOptions, p, ip, struct {
		Message  engine.Message
		Original string
		Mentions []engine.Mention
		Resolved []resolvedMention
		Text     string
	}{m, r.Text, r.Mentions, mentions, text})
	if err != nil {
		return zero, err
	}
	if dry {
		return engine.WriteResult{DryRun: true, Preview: p}, nil
	}
	u, err := e.api.MessagesEditMessage(ctx, req)
	if err != nil {
		return zero, rpcError(err)
	}
	return e.finishMessageAction(ctx, u, 1)
}

func (e *Engine) Delete(ctx context.Context, r engine.DeleteRequest) (engine.WriteResult, error) {
	var zero engine.WriteResult
	if err := e.messageActionGate(ctx, r.WriteOptions); err != nil {
		return zero, err
	}
	target, ip, err := e.writePeer(ctx, r.Peer)
	if err != nil {
		return zero, err
	}
	// channels.deleteMessages has no account-only variant; never escalate scope.
	if r.Peer.Type == engine.PeerChannel && !r.Revoke {
		return zero, engine.ErrNotSupported
	}
	if r.Revoke && !r.DryRun && !r.ConfirmRevoke {
		return zero, fmt.Errorf("%w: confirm_revoke:true is required to delete for everyone", engine.ErrInvalid)
	}
	messages, err := e.actionMessages(ctx, r.Peer, r.IDs)
	if err != nil {
		return zero, err
	}
	p := &engine.Preview{Action: "delete", Target: target, Warnings: []string{"deleting messages is irreversible"}}
	if r.Revoke {
		p.Warnings = append(p.Warnings, engine.DeleteRevokeWarning)
	}
	actionAffects(p, messages)
	dry, err := e.messageActionPreview(ctx, r.WriteOptions, p, ip, struct {
		Messages []engine.Message
		Revoke   bool
		Everyone bool
	}{messages, r.Revoke, r.Revoke})
	if err != nil {
		return zero, err
	}
	if dry {
		return engine.WriteResult{DryRun: true, Preview: p}, nil
	}
	var affected *tg.MessagesAffectedMessages
	channelID := int64(0)
	if ch, ok := ip.(*tg.InputPeerChannel); ok {
		channelID = ch.ChannelID
		affected, err = e.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash}, ID: r.IDs})
	} else {
		affected, err = e.api.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{ID: r.IDs, Revoke: r.Revoke})
	}
	if err != nil {
		return zero, rpcError(err)
	}
	result := engine.WriteResult{Done: true, Affected: len(r.IDs), Note: "deletion accepted by Telegram"}
	// Apply an exact peer-scoped tombstone, never account-wide guessed deletions.
	if _, err := e.apply(ctx, engine.Batch{Account: e.cfg.Account, Source: engine.SourceSend, Deletes: []engine.Deletion{{Peer: &r.Peer, IDs: r.IDs}}}); err != nil {
		return acceptedWriteError(result, err)
	}
	return acceptedWriteError(result, e.manager.HandleAffected(ctx, channelID, affected.Pts, affected.PtsCount))
}

// DeleteConfirmed adapts the legacy daemon capability to the canonical request.
func (e *Engine) DeleteConfirmed(ctx context.Context, r engine.DeleteRequest, confirmed bool) (engine.WriteResult, error) {
	r.ConfirmRevoke = confirmed
	return e.Delete(ctx, r)
}

func (e *Engine) React(ctx context.Context, r engine.ReactRequest) (engine.WriteResult, error) {
	var zero engine.WriteResult
	if err := e.messageActionGate(ctx, r.WriteOptions); err != nil {
		return zero, err
	}
	if !utf8.ValidString(r.Emoji) || len(r.Emoji) > 128 || strings.TrimSpace(r.Emoji) != r.Emoji {
		return zero, engine.ErrInvalid
	}
	var reactions []tg.ReactionClass
	if strings.HasPrefix(r.Emoji, "custom:") {
		id, err := strconv.ParseInt(strings.TrimPrefix(r.Emoji, "custom:"), 10, 64)
		if err != nil || id <= 0 {
			return zero, engine.ErrInvalid
		}
		reactions = append(reactions, &tg.ReactionCustomEmoji{DocumentID: id})
	} else if r.Emoji != "" {
		if r.Emoji == "paid" {
			return zero, engine.ErrNotSupported
		}
		reactions = append(reactions, &tg.ReactionEmoji{Emoticon: r.Emoji})
	}
	m, err := e.actionMessage(ctx, r.Key)
	if err != nil {
		return zero, err
	}
	target, ip, err := e.writePeer(ctx, r.Key.Peer)
	if err != nil {
		return zero, err
	}
	p := &engine.Preview{Action: "react", Target: target, Text: r.Emoji, Quoted: &m, Affects: []engine.MessageKey{m.Key}, Reversible: true}
	dry, err := e.messageActionPreview(ctx, r.WriteOptions, p, ip, struct {
		Message engine.Message
		Emoji   string
		Big     bool
	}{m, r.Emoji, r.Big})
	if err != nil {
		return zero, err
	}
	if dry {
		return engine.WriteResult{DryRun: true, Preview: p}, nil
	}
	req := &tg.MessagesSendReactionRequest{Peer: ip, MsgID: r.Key.ID, Big: r.Big}
	req.SetReaction(reactions)
	u, err := e.api.MessagesSendReaction(ctx, req)
	if err != nil {
		return zero, rpcError(err)
	}
	result, err := e.finishMessageAction(ctx, u, 1)
	// The installed bridge otherwise exposes only acceptance, not the TL shape
	// needed to distinguish omitted reaction updates from minimal snapshots.
	result.Note += "; reaction response: " + reactionRPCShape(u, r.Key)
	if err != nil {
		return result, err
	}
	return acceptedWriteError(result, e.reconcileMinimalReactionResult(ctx, u, r.Key, ip))
}

func (e *Engine) Forward(ctx context.Context, r engine.ForwardRequest) (engine.WriteResult, error) {
	var zero engine.WriteResult
	if err := e.messageActionGate(ctx, r.WriteOptions); err != nil {
		return zero, err
	}
	source, from, err := e.writePeer(ctx, r.From)
	if err != nil {
		return zero, err
	}
	target, to, err := e.writePeer(ctx, r.To)
	if err != nil {
		return zero, err
	}
	if !validWriteMessageID(r.TopicID) || (r.TopicID > 0 && (r.To.Type != engine.PeerChannel || !target.Forum)) {
		return zero, engine.ErrInvalid
	}
	if source.Protected {
		return zero, engine.ErrProtectedContent
	}
	messages, err := e.actionMessages(ctx, r.From, r.IDs)
	if err != nil {
		return zero, err
	}
	for _, m := range messages {
		if m.Protected || m.TTLPeriod > 0 || m.ExpiresAt != nil || (m.Media != nil && m.Media.TTLSeconds > 0) {
			return zero, engine.ErrProtectedContent
		}
	}
	p := &engine.Preview{Action: "forward", Target: target, Reversible: true}
	actionAffects(p, messages)
	payload, err := json.Marshal(messages)
	if err != nil {
		return zero, engine.ErrInvalid
	}
	p.Text = string(payload)
	dry, err := e.messageActionPreview(ctx, r.WriteOptions, p, to, struct {
		Source             engine.Peer
		Input              tg.InputPeerClass
		Messages           []engine.Message
		TopicID            int
		DropAuthor, Silent bool
	}{source, from, messages, r.TopicID, r.DropAuthor, r.Silent})
	if err != nil {
		return zero, err
	}
	if dry {
		return engine.WriteResult{DryRun: true, Preview: p}, nil
	}
	randomIDs := make([]int64, len(r.IDs))
	idIndex := map[int64]bool{}
	for i, id := range r.IDs {
		key := ""
		if r.IdempotencyKey != "" {
			// Dedupe binds stable operation identity, not a mutable preview snapshot
			// (reactions, names, access hashes, etc.). Approval above still binds
			// those snapshots and is single-use even for an idempotent retry.
			key, err = canonicalWriteFingerprint(struct {
				Action             string
				From, To           engine.PeerRef
				IDs                []int
				TopicID            int
				DropAuthor, Silent bool
				Key                string
				Index, ID          int
			}{"forward", r.From, r.To, r.IDs, r.TopicID, r.DropAuthor, r.Silent, r.IdempotencyKey, i, id})
			if err != nil {
				return zero, err
			}
		}
		randomIDs[i], err = e.sendRandomID(key)
		if err != nil {
			return zero, err
		}
		idIndex[randomIDs[i]] = true
	}
	req := &tg.MessagesForwardMessagesRequest{FromPeer: from, ToPeer: to, ID: r.IDs, RandomID: randomIDs, Silent: r.Silent, DropAuthor: r.DropAuthor}
	if r.TopicID > 0 {
		req.SetTopMsgID(r.TopicID)
	}
	u, err := e.api.MessagesForwardMessages(ctx, req)
	if err != nil {
		return zero, rpcError(err)
	}
	result := engine.WriteResult{Done: true, Affected: len(r.IDs), Messages: e.acceptedMessageKeys(u, r.To, idIndex), Note: "forward accepted by Telegram; not delivery or read confirmation"}
	// Persist accepted forwarded messages before returning, even when the
	// running manager has not processed the queued RPC response yet.
	if err := e.handle(ctx, u); err != nil {
		return acceptedWriteError(result, err)
	}
	return acceptedWriteError(result, e.manager.Handle(ctx, u))
}
