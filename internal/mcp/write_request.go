package mcp

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// writeRequest receives the exact, resolved request that will reach the engine.
// Only approval metadata is added later. The visible parameters and the MCP
// approval binding share one JSON representation; caller aliases, file paths,
// confirmation flags and unused arguments are deliberately not action identity.
func (s *Server) writeRequest(ctx context.Context, a arguments, request any) map[string]any {
	if s.eng == nil {
		return toolError("not implemented yet: Telegram engine is not configured")
	}
	account, err := s.eng.Account(ctx)
	if err != nil {
		return engineError(err)
	}
	if account.ID == "" {
		return toolError("engine returned no account identity")
	}
	action, parameters, err := canonicalWriteParameters(account.ID, request)
	if err != nil {
		return toolError("%v", err)
	}
	parameters["idempotency_key"] = a.IdempotencyKey
	return s.write(ctx, a, action, func(ctx context.Context, o engine.WriteOptions) (any, error) {
		// All variants copy their request value before adding write options. The
		// canonical source cannot drift independently of the executed request.
		switch r := request.(type) {
		case engine.SendTextRequest:
			r.WriteOptions = o
			return s.eng.SendText(ctx, r)
		case engine.SendMediaRequest:
			r.WriteOptions = o
			return s.eng.SendMedia(ctx, r)
		case engine.SendLocationRequest:
			r.WriteOptions = o
			return s.eng.SendLocation(ctx, r)
		case engine.SendPollRequest:
			r.WriteOptions = o
			return s.eng.SendPoll(ctx, r)
		case engine.EditRequest:
			r.WriteOptions = o
			return s.eng.Edit(ctx, r)
		case engine.DeleteRequest:
			r.WriteOptions = o
			return s.eng.Delete(ctx, r)
		case engine.ReactRequest:
			r.WriteOptions = o
			return s.eng.React(ctx, r)
		case engine.ForwardRequest:
			r.WriteOptions = o
			return s.eng.Forward(ctx, r)
		case engine.MarkReadRequest:
			r.WriteOptions = o
			return s.eng.MarkRead(ctx, r)
		case engine.TypingRequest:
			r.WriteOptions = o
			return s.eng.SetTyping(ctx, r)
		case engine.OrganiseRequest:
			r.WriteOptions = o
			return s.eng.Organise(ctx, r)
		case engine.ParticipantsRequest:
			r.WriteOptions = o
			return s.eng.ManageParticipants(ctx, r)
		case engine.UpdateGroupRequest:
			r.WriteOptions = o
			return s.eng.UpdateGroup(ctx, r)
		case engine.InviteLinkRequest:
			r.WriteOptions = o
			return s.eng.InviteLink(ctx, r)
		case engine.LeaveRequest:
			r.WriteOptions = o
			return s.eng.Leave(ctx, r)
		default:
			return nil, engine.ErrInvalid
		}
	}, parameters)
}

// Explicit map entries intentionally retain false, zero, empty strings and
// null. Engine request DTOs use omitempty for transport convenience, which is
// inappropriate for a human deciding exactly what an approval authorizes.
func canonicalWriteParameters(account engine.AccountID, request any) (string, map[string]any, error) {
	var action string
	var peer engine.PeerRef
	p := map[string]any{"account": account}
	switch r := request.(type) {
	case engine.SendTextRequest:
		action, peer = "send_text", r.Peer
		p["text"], p["reply_to"], p["topic_id"], p["mentions"] = r.Text, r.ReplyTo, r.TopicID, r.Mentions
		p["no_webpage"], p["silent"] = r.NoWebPage, r.Silent
	case engine.SendMediaRequest:
		action, peer = "send_media", r.Peer
		name := r.Filename
		if name == "" {
			name = filepath.Base(r.Path)
		}
		// A filename is not a private local pathname. gotd applies the same check.
		if name == "." || name == ".." || filepath.Base(name) != name {
			return "", nil, engine.ErrInvalid
		}
		f, err := os.Open(r.Path)
		if err != nil {
			return "", nil, fmt.Errorf("cannot read staged upload")
		}
		defer f.Close()
		hash := sha256.New()
		n, err := io.Copy(hash, io.LimitReader(f, maxMedia+1))
		if err != nil || n > maxMedia {
			return "", nil, fmt.Errorf("cannot hash staged upload")
		}
		contentType := r.MimeType
		if contentType == "" {
			contentType = mime.TypeByExtension(filepath.Ext(name))
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		contentType, _, err = mime.ParseMediaType(contentType)
		if err != nil {
			return "", nil, engine.ErrInvalid
		}
		p["type"], p["caption"], p["filename"], p["mime_type"] = r.Kind, r.Caption, name, contentType
		p["sha256"], p["size"] = fmt.Sprintf("%x", hash.Sum(nil)), n
		p["reply_to"], p["topic_id"], p["mentions"], p["spoiler"] = r.ReplyTo, r.TopicID, r.Mentions, r.Spoiler
	case engine.SendLocationRequest:
		action, peer = "send_location", r.Peer
		p["location"] = r.Location
		p["reply_to"] = r.ReplyTo
	case engine.SendPollRequest:
		action, peer = "send_poll", r.Peer
		p["question"], p["options"], p["multiple_choice"] = r.Question, r.Options, r.Multiple
		// anonymous and quiz are not inputs in the published MCP schema, but
		// the preview and its approval binding disclose the values the engine
		// resolves: nil means an anonymous poll, and quiz mode is never sent.
		p["anonymous"], p["quiz"] = r.Anonymous == nil || *r.Anonymous, false
	case engine.EditRequest:
		action, peer = "edit", r.Key.Peer
		p["key"], p["text"], p["mentions"] = r.Key, r.Text, r.Mentions
	case engine.DeleteRequest:
		action, peer = "delete", r.Peer
		p["ids"], p["revoke"], p["for_me"] = r.IDs, r.Revoke, !r.Revoke
		keys := make([]engine.MessageKey, 0, len(r.IDs))
		for _, id := range r.IDs {
			keys = append(keys, engine.MessageKey{Account: account, Peer: r.Peer, ID: id})
		}
		p["keys"] = keys
		if len(keys) == 1 {
			p["key"] = keys[0]
		}
	case engine.ReactRequest:
		action, peer = "react", r.Key.Peer
		p["key"], p["emoji"], p["big"] = r.Key, r.Emoji, r.Big
	case engine.ForwardRequest:
		action, peer = "forward", r.To
		p["from"], p["to"], p["ids"], p["topic_id"] = r.From, r.To, r.IDs, r.TopicID
		p["drop_author"], p["silent"] = r.DropAuthor, r.Silent
		keys := make([]engine.MessageKey, 0, len(r.IDs))
		for _, id := range r.IDs {
			keys = append(keys, engine.MessageKey{Account: account, Peer: r.From, ID: id})
		}
		p["keys"] = keys
		if len(keys) == 1 {
			p["key"] = keys[0]
		}
	case engine.MarkReadRequest:
		action, peer = "mark_read", r.Peer
		p["max_id"], p["receipts"] = r.MaxID, true
	case engine.TypingRequest:
		action, peer = "typing", r.Peer
		p["action"], p["topic_id"] = r.Action, r.TopicID
	case engine.OrganiseRequest:
		action, peer = "organise", r.Peer
		p["action"], p["mute_until"] = r.Action, r.MuteUntil
	case engine.ParticipantsRequest:
		action, peer = "participants", r.Group
		p["group"], p["action"], p["users"] = r.Group, r.Action, r.Users
	case engine.UpdateGroupRequest:
		action, peer = "update_group", r.Group
		// nil means leave unchanged; a pointer to "" explicitly clears about.
		p["group"], p["title"], p["about"] = r.Group, r.Title, r.About
	case engine.InviteLinkRequest:
		action, peer = "invite_link", r.Group
		p["group"], p["revoke"] = r.Group, r.Revoke
	case engine.LeaveRequest:
		action, peer = "leave", r.Group
		p["group"] = r.Group
	default:
		return "", nil, engine.ErrInvalid
	}
	p["peer"] = peer
	return action, p, nil
}
