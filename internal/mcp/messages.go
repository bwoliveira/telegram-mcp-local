package mcp

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"time"
)

func (s *Server) sendMedia(ctx context.Context, a arguments) map[string]any {
	p, fail := s.recipient(ctx, a.To)
	if fail != nil {
		return fail
	}
	if a.URL == "" {
		return toolError("url is required")
	}
	kind := map[string]engine.MediaType{"image": engine.MediaPhoto, "photo": engine.MediaPhoto, "video": engine.MediaVideo, "audio": engine.MediaAudio, "voice": engine.MediaVoice, "document": engine.MediaDocument, "sticker": engine.MediaSticker, "animation": engine.MediaAnimation}[a.Type]
	if kind == "" {
		return toolError("type must be image, video, audio, voice, document, sticker or animation")
	}
	if kind == engine.MediaSticker && (a.Caption != "" || a.Filename != "") {
		return toolError("a sticker carries no caption or filename")
	}
	reply, fail := replyID(a.ReplyTo, p)
	if fail != nil {
		return fail
	}
	if s.eng == nil {
		return toolError("not implemented yet: Telegram engine is not configured")
	}
	// The same file validation is performed for a preview and execution. Fetching
	// a URL is local staging, never a Telegram write or upload.
	path, cleanup, err := s.fetchFile(ctx, a.URL, a.Filename)
	if err != nil {
		return toolError("%v", err)
	}
	defer cleanup()
	// Stable content-addressed staging makes preview and confirmation use the
	// same engine request even when an HTTP download used a random temp path.
	path, err = s.stageUpload(path)
	if err != nil {
		return toolError("%v", err)
	}
	if kind == engine.MediaSticker && !isWebP(path) {
		return toolError("a sticker must be a WebP image")
	}
	return s.writeRequest(ctx, a, engine.SendMediaRequest{Peer: p, Kind: kind, Path: path, Caption: a.Caption, Filename: a.Filename, ReplyTo: reply, TopicID: a.TopicID})
}

func (s *Server) sendLocation(ctx context.Context, a arguments) map[string]any {
	if a.Latitude == nil || a.Longitude == nil {
		return toolError("to, latitude and longitude are required")
	}
	if *a.Latitude < -90 || *a.Latitude > 90 || *a.Longitude < -180 || *a.Longitude > 180 {
		return toolError("latitude or longitude is out of range")
	}
	p, fail := s.recipient(ctx, a.To)
	if fail != nil {
		return fail
	}
	return s.writeRequest(ctx, a, engine.SendLocationRequest{Peer: p, Location: engine.Location{Latitude: *a.Latitude, Longitude: *a.Longitude, Title: a.Name, Address: a.Address}})
}

func (s *Server) sendPoll(ctx context.Context, a arguments) map[string]any {
	if a.Question == "" {
		return toolError("question is required")
	}
	if len(a.Options) < 2 || len(a.Options) > 12 {
		return toolError("a poll needs two to twelve options")
	}
	p, fail := s.recipient(ctx, a.To)
	if fail != nil {
		return fail
	}
	return s.writeRequest(ctx, a, engine.SendPollRequest{Peer: p, Question: a.Question, Options: a.Options, Multiple: a.Multiple})
}

func (s *Server) editMessage(ctx context.Context, a arguments) map[string]any {
	if a.Text == "" {
		return toolError("text is required")
	}
	k, fail := s.key(ctx, a)
	if fail != nil {
		return fail
	}
	mentions, fail := s.mentions(ctx, a.Mentions)
	if fail != nil {
		return fail
	}
	return s.writeRequest(ctx, a, engine.EditRequest{Key: k, Text: a.Text, Mentions: mentions})
}

func (s *Server) react(ctx context.Context, a arguments) map[string]any {
	k, fail := s.key(ctx, a)
	if fail != nil {
		return fail
	}
	emoji := "👍"
	if a.Emoji != nil {
		emoji = *a.Emoji
	}
	return s.writeRequest(ctx, a, engine.ReactRequest{Key: k, Emoji: emoji})
}

func (s *Server) deleteMessage(ctx context.Context, a arguments) map[string]any {
	k, fail := s.key(ctx, a)
	if fail != nil {
		return fail
	}
	revoke := !a.ForMe
	return s.writeRequest(ctx, a, engine.DeleteRequest{Peer: k.Peer, IDs: []int{k.ID}, Revoke: revoke, ConfirmRevoke: a.ConfirmRevoke})
}

func (s *Server) resolveContacts(ctx context.Context, a arguments) map[string]any {
	if len(a.Queries) == 0 || len(a.Queries) > 50 {
		return toolError("queries must contain one to fifty usernames or phone numbers")
	}
	if s.eng == nil {
		return toolError("not implemented yet: Telegram engine is not configured")
	}
	out, err := s.eng.ResolveContacts(ctx, a.Queries)
	if err != nil {
		return engineError(err)
	}
	return textResult(map[string]any{"results": out, "note": "phone resolution may be restricted by Telegram privacy settings; not found is not proof of absence"}, false)
}

func (s *Server) profilePicture(ctx context.Context, a arguments) map[string]any {
	p, fail := s.recipient(ctx, a.To)
	if fail != nil {
		return fail
	}
	if s.eng == nil {
		return toolError("not implemented yet: Telegram engine is not configured")
	}
	if err := s.ensurePrivateMediaRoot(); err != nil {
		return engineError(err)
	}
	out, err := s.eng.ProfilePhoto(ctx, engine.PhotoRequest{Peer: p, Full: a.Full, Dir: s.mediaDir})
	if err != nil {
		return engineError(err)
	}
	return textResult(out, false)
}

func (s *Server) organiseChat(ctx context.Context, a arguments) map[string]any {
	p, fail := s.recipient(ctx, a.ChatJID)
	if fail != nil {
		return fail
	}
	if !map[string]bool{"archive": true, "unarchive": true, "pin": true, "unpin": true, "mute": true, "unmute": true}[a.Action] {
		return toolError("invalid organise_chat action")
	}
	var until *time.Time
	if a.Until != "" {
		v, err := parseWhen(a.Until, "until")
		if err != nil {
			return toolError("%v", err)
		}
		until = &v
	}
	return s.writeRequest(ctx, a, engine.OrganiseRequest{Peer: p, Action: engine.OrganiseAction(a.Action), MuteUntil: until})
}

func (s *Server) pollResults(ctx context.Context, a arguments) map[string]any {
	k, fail := s.key(ctx, a)
	if fail != nil {
		return fail
	}
	out, err := s.eng.PollResults(ctx, engine.PollRequest{Key: k, WithVoters: true, Limit: limit(a.Limit, 100)})
	if err != nil {
		return engineError(err)
	}
	return textResult(out, false)
}

func (s *Server) forwardMessage(ctx context.Context, a arguments) map[string]any {
	k, fail := s.key(ctx, a)
	if fail != nil {
		return fail
	}
	p, fail := s.recipient(ctx, a.To)
	if fail != nil {
		return fail
	}
	return s.writeRequest(ctx, a, engine.ForwardRequest{From: k.Peer, IDs: []int{k.ID}, To: p, TopicID: a.TopicID})
}

func (s *Server) sendTyping(ctx context.Context, a arguments) map[string]any {
	p, fail := s.recipient(ctx, a.To)
	if fail != nil {
		return fail
	}
	action := engine.TypingText
	if a.Typing != nil && !*a.Typing {
		action = engine.TypingCancel
	} else if a.Audio {
		action = engine.TypingAudio
	}
	return s.writeRequest(ctx, a, engine.TypingRequest{Peer: p, Action: action, TopicID: a.TopicID})
}
