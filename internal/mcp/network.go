package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
)

func (s *Server) sendText(ctx context.Context, a arguments) map[string]any {
	if a.Text == "" {
		return toolError("to and text are required")
	}
	p, fail := s.recipient(ctx, a.To)
	if fail != nil {
		return fail
	}
	reply, fail := replyID(a.ReplyTo, p)
	if fail != nil {
		return fail
	}
	if s.eng == nil {
		return toolError("not implemented yet: Telegram engine is not configured")
	}
	text, mentions, fail := s.textMentions(ctx, p, a.Text, a.Mentions)
	if fail != nil {
		return fail
	}
	return s.writeRequest(ctx, a, engine.SendTextRequest{Peer: p, Text: text, ReplyTo: reply, TopicID: a.TopicID, Mentions: mentions})
}

func (s *Server) getGroup(ctx context.Context, a arguments) map[string]any {
	if a.Live == nil || !*a.Live {
		if s.index == nil {
			return toolError("not implemented yet: group snapshot needs the inherited local index")
		}
		g, err := s.index.Group(ctx, a.GroupJID, a.Search)
		if err != nil {
			return toolError("%v", err)
		}
		return inheritedResult(textResult(g, false))
	}
	p, err := s.resolvePeer(ctx, a.GroupJID)
	if err != nil {
		return toolError("group_id: %v", err)
	}
	if p.Type == engine.PeerUser {
		return toolError("group_id must identify a chat or channel")
	}
	if s.eng == nil {
		return toolError("not implemented yet: Telegram engine is not configured")
	}
	g, err := s.eng.RefreshGroup(ctx, engine.GroupRequest{Group: p, Search: a.Search, Limit: limit(a.Limit, 100)})
	if err != nil {
		return engineError(err)
	}
	return textResult(g, false)
}

type downloaded struct {
	Path      string `json:"path"`
	Bytes     int64  `json:"bytes"`
	MediaType string `json:"media_type"`
	MimeType  string `json:"mime_type"`
}

func (s *Server) fetchMedia(ctx context.Context, m index.Message) (downloaded, error) {
	k, err := parseMessageKey(arguments{ChatJID: m.ChatJID, MessageID: m.ID}, "")
	if err != nil {
		return downloaded{}, errors.New("not implemented yet: downloading inherited legacy media requires the Telegram store migration")
	}
	if s.eng == nil {
		return downloaded{}, errors.New("not implemented yet: Telegram engine is not configured")
	}
	if err := s.ensurePrivateMediaRoot(); err != nil {
		return downloaded{}, err
	}
	acc, err := s.eng.Account(ctx)
	if err != nil {
		return downloaded{}, err
	}
	k.Account = acc.ID
	d, err := s.eng.Download(ctx, engine.DownloadRequest{Key: k, Dir: filepath.Join(s.mediaDir, safeName(peerID(k.Peer))), MaxBytes: maxMedia})
	return downloaded{Path: d.Path, Bytes: d.Bytes, MediaType: string(d.MediaType), MimeType: d.MimeType}, err
}
func (s *Server) downloadMedia(ctx context.Context, a arguments) map[string]any {
	if err := s.ensurePrivateMediaRoot(); err != nil {
		return engineError(err)
	}
	k, fail := s.key(ctx, a)
	if fail != nil {
		return fail
	}
	d, err := s.eng.Download(ctx, engine.DownloadRequest{Key: k, Dir: filepath.Join(s.mediaDir, safeName(peerID(k.Peer))), MaxBytes: maxMedia})
	if err != nil {
		return engineError(err)
	}
	result := map[string]any{"message_id": strconv.Itoa(k.ID), "chat_id": peerID(k.Peer), "path": d.Path, "bytes": d.Bytes, "media_type": d.MediaType, "mime_type": d.MimeType, "filename": d.Filename}
	f, err := mediaOpenFile(s.mediaDir, d.Path)
	if err != nil {
		return toolError("%v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() != d.Bytes || info.Size() > maxMedia {
		return toolError("downloaded media size did not match the engine result")
	}
	if a.Link || info.Size() > maxInlineMedia {
		token := s.links.add(d.Path, d.MimeType, 10*time.Minute)
		result["url"] = s.base() + "/media/" + token
		result["expires_in_seconds"] = 600
		result["authentication"] = "bearer or panel session required"
		return textResult(result, false)
	}
	body, err := io.ReadAll(io.LimitReader(f, maxInlineMedia+1))
	if err != nil {
		return toolError("%v", err)
	}
	if len(body) > maxInlineMedia {
		return toolError("downloaded media exceeds the inline limit")
	}
	meta, _ := json.Marshal(result)
	content := []any{map[string]any{"type": "text", "text": string(meta)}}
	encoded := base64.StdEncoding.EncodeToString(body)
	switch d.MediaType {
	case engine.MediaPhoto, engine.MediaSticker:
		content = append(content, map[string]any{"type": "image", "data": encoded, "mimeType": orDefault(d.MimeType, "image/jpeg")})
	case engine.MediaAudio, engine.MediaVoice:
		content = append(content, map[string]any{"type": "audio", "data": encoded, "mimeType": orDefault(d.MimeType, "audio/ogg")})
	default:
		content = append(content, map[string]any{"type": "resource", "resource": map[string]any{"uri": "file://" + d.Path, "mimeType": orDefault(d.MimeType, "application/octet-stream"), "blob": encoded}})
	}
	return map[string]any{"content": content, "isError": false}
}
