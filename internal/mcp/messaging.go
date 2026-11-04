package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// No transport has Controller: neither a tool nor message content can turn
// writes on, log out, or drive authentication.
type previewApproval struct {
	binding [32]byte
	expires time.Time
}

func requestBinding(action string, parameters map[string]any) ([32]byte, error) {
	b, err := json.Marshal(struct {
		Action     string         `json:"action"`
		Parameters map[string]any `json:"parameters"`
	}{action, parameters})
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(b), nil
}
func (s *Server) write(ctx context.Context, a arguments, action string, invoke func(context.Context, engine.WriteOptions) (any, error), details ...map[string]any) map[string]any {
	if s.eng == nil {
		return toolError("not implemented yet: Telegram engine is not configured")
	}
	opts := engine.WriteOptions{DryRun: a.DryRun || !a.Confirm, IdempotencyKey: a.IdempotencyKey}
	var parameters map[string]any
	if len(details) > 0 {
		// Freeze a JSON value tree, including nested DTOs. This sorts every
		// object's keys consistently for the visible representation and binding,
		// while UseNumber preserves Telegram's full int64 identities exactly.
		encoded, err := json.Marshal(details[0])
		if err != nil {
			return toolError("cannot encode canonical preview parameters")
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.UseNumber()
		if err := decoder.Decode(&parameters); err != nil {
			return toolError("cannot encode canonical preview parameters")
		}
	}
	binding, err := requestBinding(action, parameters)
	if err != nil {
		return toolError("cannot encode canonical preview parameters")
	}
	if !opts.DryRun {
		if a.PreviewFingerprint == "" {
			return toolError("preview_fingerprint is required; preview the action and obtain explicit human approval first")
		}
		s.previewMu.Lock()
		approval, ok := s.previews[a.PreviewFingerprint]
		if ok && approval.binding == binding {
			delete(s.previews, a.PreviewFingerprint)
		}
		s.previewMu.Unlock()
		if !ok || !s.clock().Before(approval.expires) {
			return toolError("preview expired or already used; preview it again")
		}
		if approval.binding != binding {
			return toolError("preview does not match this action: %v", engine.ErrPreviewMismatch)
		}
		opts.ExpectFingerprint = a.PreviewFingerprint
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	out, err := invoke(ctx, opts)
	if err != nil {
		// A failed multi-RPC operation may already have changed Telegram. Keep
		// the accepted messages/counts and per-user outcomes next to the error.
		if writeHasEvidence(out) {
			return partialWriteError(out, err)
		}
		return engineError(err)
	}
	var result engine.WriteResult
	switch v := out.(type) {
	case engine.WriteResult:
		result = v
	case engine.ParticipantsResult:
		result = v.WriteResult
	case engine.InviteLinkResult:
		result = v.WriteResult
	default:
		return toolError("engine returned an invalid write result")
	}
	// Do not mutate engine-owned preview objects: their internal fingerprint was
	// computed before MCP attached this independently bound representation.
	if result.Preview != nil {
		preview := *result.Preview
		preview.Parameters = parameters
		result.Preview = &preview
		switch v := out.(type) {
		case engine.WriteResult:
			v.Preview = &preview
			out = v
		case engine.ParticipantsResult:
			v.Preview = &preview
			out = v
		case engine.InviteLinkResult:
			v.Preview = &preview
			out = v
		}
	}
	if opts.DryRun {
		if !result.DryRun || result.Done || result.Preview == nil || result.Preview.Fingerprint == "" {
			return toolError("engine did not return a valid preview; nothing is approved")
		}
		s.previewMu.Lock()
		for k, p := range s.previews {
			if !s.clock().Before(p.expires) {
				delete(s.previews, k)
			}
		}
		if len(s.previews) >= 1024 {
			s.previewMu.Unlock()
			return toolError("too many pending previews; wait for them to expire")
		}
		s.previews[result.Preview.Fingerprint] = previewApproval{binding: binding, expires: s.clock().Add(10 * time.Minute)}
		s.previewMu.Unlock()
	} else if result.DryRun || !result.Done {
		if writeHasEvidence(out) {
			return partialWriteError(out, errors.New("engine did not confirm complete execution"))
		}
		return toolError("engine did not confirm execution")
	}
	return textResult(out, false)
}
func engineError(err error) map[string]any {
	return textResult(engineErrorFields(err), true)
}
func engineErrorFields(err error) map[string]any {
	if errors.Is(err, engine.ErrNotSupported) {
		return map[string]any{"error": fmt.Sprintf("not implemented yet: %v", err)}
	}
	var ambiguous *engine.AmbiguousError
	if errors.As(err, &ambiguous) {
		return map[string]any{"error": "ambiguous recipient; choose a typed peer ID", "candidates": ambiguous.Candidates}
	}
	var flood *engine.FloodWaitError
	if errors.As(err, &flood) {
		return map[string]any{"error": "Telegram rate limit; retry after flood_wait_until", "flood_wait_until": flood.Until}
	}
	return map[string]any{"error": err.Error()}
}

func parsePeer(value string) (engine.PeerRef, error) {
	kind, id, ok := strings.Cut(value, ":")
	if !ok || (kind != "user" && kind != "chat" && kind != "channel") {
		return engine.PeerRef{}, fmt.Errorf("peer must be user:ID, chat:ID or channel:ID (positive Telegram ID)")
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 || strconv.FormatInt(n, 10) != id {
		return engine.PeerRef{}, fmt.Errorf("peer must have a canonical positive Telegram ID")
	}
	return engine.PeerRef{Type: engine.PeerType(kind), ID: n}, nil
}
func peerID(p engine.PeerRef) string { return string(p.Type) + ":" + strconv.FormatInt(p.ID, 10) }
func parseMessageKey(a arguments, account engine.AccountID) (engine.MessageKey, error) {
	chat, id := a.ChatJID, a.MessageID
	if composed, number, ok := strings.Cut(id, "/"); ok {
		if chat != "" && chat != composed {
			return engine.MessageKey{}, fmt.Errorf("message_id belongs to another chat_id")
		}
		chat, id = composed, number
	}
	if chat == "" || id == "" {
		return engine.MessageKey{}, fmt.Errorf("message_id and chat_id are required; alternatively use channel:123/456")
	}
	peer, err := parsePeer(chat)
	if err != nil {
		return engine.MessageKey{}, err
	}
	n, err := strconv.Atoi(id)
	if err != nil || n <= 0 || strconv.Itoa(n) != id || int64(n) > 2147483647 {
		return engine.MessageKey{}, fmt.Errorf("message_id must be a canonical positive Telegram message ID")
	}
	return engine.MessageKey{Account: account, Peer: peer, ID: n}, nil
}
func (s *Server) key(ctx context.Context, a arguments) (engine.MessageKey, map[string]any) {
	k, err := s.resolveMessageKey(ctx, a)
	if err != nil {
		return k, toolError("%v", err)
	}
	if s.eng == nil {
		return k, toolError("not implemented yet: Telegram engine is not configured")
	}
	acc, err := s.eng.Account(ctx)
	if err != nil {
		return k, engineError(err)
	}
	if acc.ID == "" {
		return k, toolError("engine returned no account identity")
	}
	k.Account = acc.ID
	return k, nil
}
func (s *Server) recipient(ctx context.Context, to string) (engine.PeerRef, map[string]any) {
	if to == "" {
		return engine.PeerRef{}, toolError("to is required")
	}
	p, err := s.resolvePeer(ctx, to)
	if err != nil {
		return engine.PeerRef{}, engineError(err)
	}
	return p, nil
}
func (s *Server) mentions(ctx context.Context, values []string) ([]engine.Mention, map[string]any) {
	var mentions []engine.Mention
	for _, v := range values {
		p, fail := s.recipient(ctx, v)
		if fail != nil {
			return nil, fail
		}
		if p.Type != engine.PeerUser {
			return nil, toolError("mentions must identify users")
		}
		mentions = append(mentions, engine.Mention{User: p})
	}
	return mentions, nil
}
func replyID(value string, p engine.PeerRef) (int, map[string]any) {
	if value == "" {
		return 0, nil
	}
	k, err := parseMessageKey(arguments{ChatJID: peerID(p), MessageID: value}, "")
	if err != nil {
		return 0, toolError("reply_to: %v", err)
	}
	return k.ID, nil
}
func clip(v string, n int) string {
	r := []rune(v)
	if n <= 0 || len(r) <= n {
		return v
	}
	return string(r[:n]) + "…"
}

func (s *Server) markChatRead(ctx context.Context, a arguments) map[string]any {
	if a.MaxID < 0 || int64(a.MaxID) > 2147483647 {
		return toolError("max_id must be zero (all messages) or a positive Telegram message ID")
	}
	p, err := s.resolvePeer(ctx, a.ChatJID)
	if err != nil {
		return toolError("chat_id: %v", err)
	}
	if a.Receipts != nil && !*a.Receipts {
		return toolError("Telegram mark_chat_read sends a read receipt; receipts:false is not supported")
	}
	return s.writeRequest(ctx, a, engine.MarkReadRequest{Peer: p, MaxID: a.MaxID})
}
