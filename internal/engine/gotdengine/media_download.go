package gotdengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

const defaultMediaMaxBytes int64 = 100 << 20

type mediaFile struct {
	location       tg.InputFileLocationClass
	size           int64
	kind           engine.MediaType
	mime, filename string
}

func mediaFilename(name string) string {
	// Telegram names are labels, never paths; normalize both path separators.
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, name)
	if name == "." || name == ".." || name == "/" {
		return ""
	}
	return name
}

// mediaExtension uses a fixed allowlist, never a Telegram-supplied path or
// arbitrary suffix. Local inventory classifies by this extension.
func mediaExtension(f mediaFile) string {
	switch strings.ToLower(f.mime) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "audio/ogg", "application/ogg":
		return ".ogg"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	}
	switch f.kind {
	case engine.MediaPhoto:
		return ".jpg"
	case engine.MediaAudio, engine.MediaVoice:
		return ".ogg"
	case engine.MediaVideo, engine.MediaVideoNote, engine.MediaAnimation:
		return ".mp4"
	case engine.MediaSticker:
		return ".webp"
	}
	return ".bin"
}

// convertDownloadableMedia advertises only attachments that the existing
// downloader can resolve. File references stay ephemeral: every download
// re-reads the exact message instead of exporting or persisting credentials.
func convertDownloadableMedia(out *engine.Message, raw *tg.Message) {
	if out.Media == nil || out.Protected || out.Media.TTLSeconds > 0 {
		return
	}
	file, err := mediaFileFor(raw)
	if err != nil {
		return
	}
	out.Media.Downloadable = true
	out.Media.Size = file.size
	out.Media.MimeType = file.mime
	out.Media.Filename = file.filename
}

func mediaFileFor(m *tg.Message) (mediaFile, error) {
	switch v := m.Media.(type) {
	case *tg.MessageMediaDocument:
		d, ok := v.Document.(*tg.Document)
		if !ok {
			return mediaFile{}, engine.ErrProtectedContent
		}
		f := mediaFile{location: &tg.InputDocumentFileLocation{ID: d.ID, AccessHash: d.AccessHash, FileReference: d.FileReference}, size: d.Size, kind: engine.MediaDocument, mime: d.MimeType}
		for _, a := range d.Attributes {
			switch a := a.(type) {
			case *tg.DocumentAttributeFilename:
				f.filename = mediaFilename(a.FileName)
			case *tg.DocumentAttributeAudio:
				f.kind = engine.MediaAudio
				if a.Voice {
					f.kind = engine.MediaVoice
				}
			case *tg.DocumentAttributeVideo:
				f.kind = engine.MediaVideo
				if a.RoundMessage {
					f.kind = engine.MediaVideoNote
				}
			case *tg.DocumentAttributeSticker:
				f.kind = engine.MediaSticker
			case *tg.DocumentAttributeAnimated:
				f.kind = engine.MediaAnimation
			}
		}
		if d.ID <= 0 || f.size < 0 {
			return mediaFile{}, engine.ErrInvalid
		}
		return f, nil
	case *tg.MessageMediaPhoto:
		p, ok := v.Photo.(*tg.Photo)
		if !ok {
			return mediaFile{}, engine.ErrProtectedContent
		}
		var bestSize int64
		var thumb string
		for _, raw := range p.Sizes {
			var size int64
			var typ string
			switch s := raw.(type) {
			case *tg.PhotoSize:
				size, typ = int64(s.Size), s.Type
			case *tg.PhotoSizeProgressive:
				typ = s.Type
				for _, n := range s.Sizes {
					if int64(n) > size {
						size = int64(n)
					}
				}
			}
			if size > bestSize {
				bestSize, thumb = size, typ
			}
		}
		if p.ID <= 0 || thumb == "" {
			return mediaFile{}, engine.ErrNotSupported
		}
		return mediaFile{location: &tg.InputPhotoFileLocation{ID: p.ID, AccessHash: p.AccessHash, FileReference: p.FileReference, ThumbSize: thumb}, size: bestSize, kind: engine.MediaPhoto, mime: "image/jpeg", filename: "photo.jpg"}, nil
	}
	return mediaFile{}, engine.ErrNotSupported
}

// mediaSink enforces the byte ceiling on actual bytes, not just Telegram's
// declared size. It hashes precisely the bytes accepted by the private file.
type mediaSink struct {
	file         *os.File
	digest       hash.Hash
	written, max int64
}

func (s *mediaSink) Write(p []byte) (int, error) {
	if int64(len(p)) > s.max-s.written {
		return 0, fmt.Errorf("media exceeds max_bytes: %w", engine.ErrInvalid)
	}
	n, err := s.file.Write(p)
	if n > 0 {
		_, _ = s.digest.Write(p[:n])
		s.written += int64(n)
	}
	return n, err
}

// Download uses gotd's streaming downloader. gotd's API invoker handles
// FILE_MIGRATE without migrating the account's primary connection. CDN is not
// implicitly enabled. A stale reference is refreshed at most once by message ID.
func (e *Engine) Download(parent context.Context, req engine.DownloadRequest) (engine.Download, error) {
	ctx, cancel := e.mediaContext(parent)
	defer cancel()
	if err := e.checkMedia(ctx, req.Key); err != nil {
		return engine.Download{}, err
	}
	if strings.TrimSpace(req.Dir) == "" || req.MaxBytes < 0 {
		return engine.Download{}, engine.ErrInvalid
	}
	if req.Thumb {
		return engine.Download{}, engine.ErrNotSupported
	}
	maxBytes := req.MaxBytes
	if maxBytes == 0 {
		maxBytes = defaultMediaMaxBytes
	}
	for attempt := 0; attempt < 2; attempt++ {
		m, err := e.fetchMediaMessage(ctx, req.Key)
		if err != nil {
			return engine.Download{}, err
		}
		media, err := mediaFileFor(m)
		if err != nil {
			return engine.Download{}, err
		}
		if media.size > maxBytes {
			return engine.Download{}, fmt.Errorf("media exceeds max_bytes: %w", engine.ErrInvalid)
		}
		result, err := e.downloadMediaFile(ctx, req, media, m, maxBytes)
		if err == nil {
			return result, nil
		}
		if attempt == 0 && (tgerr.Is(err, "FILE_REFERENCE_EXPIRED") || tgerr.Is(err, "FILE_REFERENCE_INVALID")) {
			continue
		}
		// Preserve sentinels from our own guards and writer rather than suppressing
		// them as transport diagnostics. Never return a partial path on any error.
		if errors.Is(err, engine.ErrInvalid) || errors.Is(err, engine.ErrProtectedContent) || errors.Is(err, engine.ErrMessageNotFound) {
			return engine.Download{}, err
		}
		return engine.Download{}, rpcError(err)
	}
	return engine.Download{}, engine.ErrInvalid
}

func (e *Engine) downloadMediaFile(ctx context.Context, req engine.DownloadRequest, media mediaFile, m *tg.Message, maxBytes int64) (result engine.Download, err error) {
	if err = os.MkdirAll(req.Dir, 0700); err != nil {
		return result, err
	}
	f, err := os.CreateTemp(req.Dir, "telegram-*"+mediaExtension(media))
	if err != nil {
		return result, err
	}
	keep := false
	defer func() {
		_ = f.Close()
		if !keep {
			_ = os.Remove(f.Name())
		}
	}()
	sink := &mediaSink{file: f, digest: sha256.New(), max: maxBytes}
	if _, err = downloader.NewDownloader().Download(e.api, media.location).Stream(ctx, sink); err != nil {
		return result, err
	}
	if sink.written != media.size {
		return result, fmt.Errorf("incomplete or changed media: %w", engine.ErrInvalid)
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	if err = e.checkRawMedia(m); err != nil {
		return result, err
	}
	if err = e.checkMedia(ctx, req.Key); err != nil {
		return result, err
	}
	if err = f.Sync(); err != nil {
		return result, err
	}
	if err = f.Close(); err != nil {
		return result, err
	}
	if err = ctx.Err(); err != nil {
		return result, err
	}
	result = engine.Download{Path: f.Name(), Bytes: sink.written, MediaType: media.kind, MimeType: media.mime, Filename: media.filename, SHA256: hex.EncodeToString(sink.digest.Sum(nil))}
	keep = true
	return result, nil
}

var _ io.Writer = (*mediaSink)(nil)
