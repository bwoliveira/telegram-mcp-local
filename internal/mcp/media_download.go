package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// MediaRecorder uses the existing store/engine batch contract; engine.go does
// not need to grow a media-specific persistence API. *store.Store implements it.
type MediaRecorder interface {
	Apply(context.Context, engine.Batch) (engine.Applied, error)
}

// DownloadMedia is the typed HTTP/MCP API. Lifecycle checks precede Download;
// the existing engine owns Telegram refresh; the caller registers the completed
// file atomically before exposing it (both fake and gotd engines are supported).
func (s *Server) DownloadMedia(ctx context.Context, key engine.MessageKey) (engine.Download, error) {
	m, _, err := s.mediaMessage(ctx, key, true)
	if err != nil {
		return engine.Download{}, err
	}
	// Downloadable is a snapshot hint, not a policy decision: the engine
	// refreshes current media availability after the lifecycle checks above.
	if m.Media == nil {
		return engine.Download{}, engine.ErrNotSupported
	}
	if s.mediaDir == "" {
		return engine.Download{}, engine.ErrNotSupported
	}
	var recorder MediaRecorder
	if s.readStore != nil {
		var ok bool
		recorder, ok = s.readStore.(MediaRecorder)
		if !ok {
			return engine.Download{}, engine.ErrNotSupported
		}
	}
	if err = s.ensurePrivateMediaRoot(); err != nil {
		return engine.Download{}, err
	}
	info, err := os.Lstat(s.mediaDir)
	if err != nil {
		return engine.Download{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return engine.Download{}, engine.ErrInvalid
	}
	dir := filepath.Join(s.mediaDir, safeName(peerID(key.Peer)))
	if err = os.MkdirAll(dir, 0700); err != nil {
		return engine.Download{}, err
	}
	if err = mediaSafePath(s.mediaDir, dir, false); err != nil {
		return engine.Download{}, err
	}
	if err = tightenMediaDirectory(dir); err != nil {
		return engine.Download{}, err
	}
	d, err := s.eng.Download(ctx, engine.DownloadRequest{Key: key, Dir: dir, MaxBytes: maxMedia})
	if err != nil {
		return engine.Download{}, err
	}
	f, err := mediaOpenFile(s.mediaDir, d.Path)
	if err != nil {
		return engine.Download{}, err
	}
	defer f.Close()
	accepted := false
	defer func() {
		if !accepted {
			_ = mediaRemove(s.mediaDir, d.Path)
			s.links.RevokePath(d.Path)
		}
	}()
	info, err = f.Stat()
	if err != nil {
		return engine.Download{}, err
	}
	if info.Size() > maxMedia || info.Size() != d.Bytes {
		return engine.Download{}, engine.ErrInvalid
	}
	if err = tightenMediaFile(f); err != nil {
		return engine.Download{}, err
	}
	// A deletion/expiry during the download must not issue a usable link or bytes.
	if _, _, err = s.mediaMessage(ctx, key, true); err != nil {
		return engine.Download{}, err
	}
	if recorder != nil {
		at := s.clock()
		_, err = recorder.Apply(ctx, engine.Batch{Account: key.Account, Source: engine.SourceRefresh, Downloads: []engine.DownloadRecord{{Key: key, Path: d.Path, Bytes: d.Bytes, SHA256: d.SHA256, At: at}}})
		if err != nil {
			return engine.Download{}, err
		}
	}
	accepted = true
	return d, nil
}

// mediaSafePath refuses symlinked roots, ancestors, files and nonregular files.
// The subsequent I/O additionally uses os.Root to contain concurrent renames.
func mediaSafePath(root, path string, regular bool) error {
	if root == "" || path == "" {
		return engine.ErrInvalid
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return engine.ErrInvalid
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return engine.ErrInvalid
	}
	p := root
	parts := strings.Split(rel, string(filepath.Separator))
	for i, part := range parts {
		p = filepath.Join(p, part)
		info, err = os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return engine.ErrInvalid
		}
		if i < len(parts)-1 && !info.IsDir() {
			return engine.ErrInvalid
		}
	}
	if regular && !info.Mode().IsRegular() {
		return engine.ErrInvalid
	}
	if !regular && !info.IsDir() {
		return engine.ErrInvalid
	}
	return nil
}

func mediaOpenFile(root, path string) (*os.File, error) {
	if err := mediaSafePath(root, path, true); err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(absRoot)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(rel)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, engine.ErrInvalid
	}
	return f, nil
}

// MediaTool is the HTTP media seam. HTTP retains its explicit native
// transcription opt-in; the MCP dispatcher uses mcpMediaTool, which never does.
func (s *Server) MediaTool(ctx context.Context, name string, raw json.RawMessage) (map[string]any, bool) {
	return s.mediaTool(ctx, name, raw, true)
}

func (s *Server) mcpMediaTool(ctx context.Context, name string, raw json.RawMessage) (map[string]any, bool) {
	return s.mediaTool(ctx, name, raw, false)
}

func (s *Server) mediaTool(ctx context.Context, name string, raw json.RawMessage, allowNative bool) (map[string]any, bool) {
	switch name {
	case "download_media", "media_stats", "purge_media":
	case "transcribe_audio", "save_transcript":
		if s.readStore == nil {
			return nil, false
		}
	default:
		return nil, false
	}
	var a arguments
	native := false
	if len(raw) > 0 && string(raw) != "null" {
		if allowNative && name == "transcribe_audio" {
			var supplied map[string]json.RawMessage
			if err := json.Unmarshal(raw, &supplied); err != nil {
				return toolError("invalid media tool arguments for %s: %v", name, err), true
			}
			if value, ok := supplied["native"]; ok {
				if err := json.Unmarshal(value, &native); err != nil {
					return toolError("invalid media tool arguments for %s: native must be a boolean", name), true
				}
				delete(supplied, "native")
				raw, _ = json.Marshal(supplied)
			}
		}
		if err := decodeArguments(name, raw, &a); err != nil {
			return toolError("invalid media tool arguments for %s: %v", name, err), true
		}
	}
	switch name {
	case "media_stats":
		return s.mediaStats(ctx, a), true
	case "purge_media":
		return s.purgeMedia(ctx, a), true
	}
	key, err := s.resolveMessageKey(ctx, a)
	if err != nil {
		return mediaToolError(engine.ErrInvalid), true
	}
	if s.eng == nil {
		return mediaToolError(engine.ErrNotSupported), true
	}
	acc, err := s.eng.Account(ctx)
	if err != nil {
		return mediaToolError(err), true
	}
	key.Account = acc.ID
	switch name {
	case "download_media":
		return s.mediaDownloadTool(ctx, key, a.Link), true
	case "save_transcript":
		tr, err := s.SaveMediaTranscript(ctx, key, a.Text, a.Language, a.Model)
		if err != nil {
			return mediaToolError(err), true
		}
		return textResult(map[string]any{"saved": true, "transcript": tr, "untrusted_content": UntrustedContent}, false), true
	default:
		tr, err := s.TranscribeMedia(ctx, key, a.Language, a.Refresh, native)
		if errors.Is(err, ErrNoEngine) {
			return textResult(map[string]any{"error": "local transcription is not installed", "setup": []string{"Install whisper.cpp, ffmpeg and the model from the app's transcription settings or `telegram-mcp transcription install`. Audio stays on this computer; installation is explicit."}}, true), true
		}
		if err != nil {
			return mediaToolError(err), true
		}
		return textResult(tr, false), true
	}
}

func (s *Server) mediaDownloadTool(ctx context.Context, key engine.MessageKey, link bool) map[string]any {
	d, err := s.DownloadMedia(ctx, key)
	if err != nil {
		return mediaToolError(err)
	}
	result := map[string]any{"message_id": strconv.Itoa(key.ID), "chat_id": peerID(key.Peer), "path": d.Path, "bytes": d.Bytes, "media_type": d.MediaType, "mime_type": d.MimeType, "filename": d.Filename, "untrusted_content": UntrustedContent}
	f, err := mediaOpenFile(s.mediaDir, d.Path)
	if err != nil {
		return mediaToolError(err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() != d.Bytes || info.Size() > maxMedia {
		return mediaToolError(engine.ErrInvalid)
	}
	if link || info.Size() > maxInlineMedia {
		token := s.links.addChecked(d.Path, d.MimeType, 10*time.Minute, func(ctx context.Context) bool {
			if _, _, err := s.mediaMessage(ctx, key, true); err != nil {
				return false
			}
			return mediaSafePath(s.mediaDir, d.Path, true) == nil
		})
		if token == "" {
			return toolError("could not create media link")
		}
		result["url"] = s.base() + "/media/" + token
		result["link"] = result["url"]
		result["expires_in_seconds"] = 600
		result["authentication"] = "bearer or panel session required"
		return textResult(result, false)
	}
	body, err := io.ReadAll(io.LimitReader(f, maxInlineMedia+1))
	if err != nil {
		return mediaToolError(err)
	}
	if int64(len(body)) != d.Bytes || len(body) > maxInlineMedia {
		return mediaToolError(engine.ErrInvalid)
	}
	metadata, _ := json.Marshal(result)
	content := []any{map[string]any{"type": "text", "text": string(metadata)}}
	encoded := base64.StdEncoding.EncodeToString(body)
	switch d.MediaType {
	case engine.MediaPhoto, engine.MediaSticker:
		content = append(content, map[string]any{"type": "image", "data": encoded, "mimeType": orDefault(d.MimeType, "image/jpeg")})
	case engine.MediaAudio, engine.MediaVoice:
		content = append(content, map[string]any{"type": "audio", "data": encoded, "mimeType": orDefault(d.MimeType, "audio/ogg")})
	default:
		content = append(content, map[string]any{"type": "resource", "resource": map[string]any{"uri": (&url.URL{Scheme: "file", Path: d.Path}).String(), "mimeType": orDefault(d.MimeType, "application/octet-stream"), "blob": encoded}})
	}
	return map[string]any{"content": content, "isError": false}
}
