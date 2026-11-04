package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
)

const (
	maxMedia       = 100 << 20
	maxInlineMedia = 256 << 10
)

func (s *Server) ensurePrivateMediaRoot() error {
	if s.mediaDir == "" {
		return engine.ErrNotSupported
	}
	if err := os.MkdirAll(s.mediaDir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(s.mediaDir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return engine.ErrInvalid
	}
	return tightenMediaDirectory(s.mediaDir)
}

// isWebP reports whether a file starts like a WebP image, the only format
// this application accepts for a sticker.
func isWebP(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 12)
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return string(head[:4]) == "RIFF" && string(head[8:]) == "WEBP"
}

// fetchFile resolves a send's source to a local file: a path on this machine
// is used as it is, a URL is downloaded to a temporary file.
func (s *Server) fetchFile(ctx context.Context, source, filename string) (string, func(), error) {
	nothing := func() {}
	source = strings.TrimSpace(source)
	if p, ok := strings.CutPrefix(source, "file://"); ok {
		source = p
	}
	if filepath.IsAbs(source) {
		info, err := os.Stat(source)
		if err != nil {
			return "", nothing, fmt.Errorf("cannot read %s: %v", source, err)
		}
		if !info.Mode().IsRegular() || info.Size() > maxMedia {
			return "", nothing, fmt.Errorf("%s is not a file of at most 100 MiB", source)
		}
		return source, nothing, nil
	}
	if !strings.HasPrefix(source, "http://") && !strings.HasPrefix(source, "https://") {
		return "", nothing, errors.New("url must be an http(s) URL or an absolute path on this machine")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return "", nothing, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", nothing, fmt.Errorf("could not download %s: %v", source, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", nothing, fmt.Errorf("downloading %s answered %s", source, resp.Status)
	}
	name := filename
	if name == "" {
		name = filepath.Base(req.URL.Path)
	}
	if name == "" || name == "/" || name == "." {
		name = "file"
	}
	dir, err := os.MkdirTemp("", "telegram-mcp-send-")
	if err != nil {
		return "", nothing, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, filepath.Base(name))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		cleanup()
		return "", nothing, err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxMedia+1))
	f.Close()
	if err != nil {
		cleanup()
		return "", nothing, err
	}
	if n > maxMedia {
		cleanup()
		return "", nothing, errors.New("the file is larger than this application's 100 MiB safety limit")
	}
	return path, cleanup, nil
}

// stageUpload freezes bytes under a content-addressed pathname. The engine
// fingerprints those bytes again; a changed file/URL cannot reuse approval.
// Upload staging belongs to the ordinary local media inventory and purge.
func (s *Server) stageUpload(path string) (string, error) {
	if err := s.ensurePrivateMediaRoot(); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(s.mediaDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	const staging = "send-staging"
	if err := root.MkdirAll(staging, 0o700); err != nil {
		return "", err
	}
	info, err := root.Lstat(staging)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", engine.ErrInvalid
	}
	if err := tightenMediaStaging(root, staging); err != nil {
		return "", err
	}
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer src.Close()
	var dst *os.File
	var temporary string
	for {
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return "", err
		}
		temporary = filepath.Join(staging, "upload-"+hex.EncodeToString(nonce))
		dst, err = root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if !errors.Is(err, os.ErrExist) {
			break
		}
	}
	if err != nil {
		return "", err
	}
	defer root.Remove(temporary)
	defer dst.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, maxMedia+1))
	if err != nil {
		return "", err
	}
	if n > maxMedia {
		return "", errors.New("media exceeds the 100 MiB safety limit")
	}
	if err := dst.Close(); err != nil {
		return "", err
	}
	target := filepath.Join(staging, hex.EncodeToString(h.Sum(nil))+filepath.Ext(path))
	if err := root.Rename(temporary, target); err != nil {
		return "", err
	}
	return filepath.Join(s.mediaDir, target), nil
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == 0 {
			return '_'
		}
		return r
	}, s)
}

func extension(m index.Message) string {
	if ext := filepath.Ext(m.Filename); ext != "" {
		return ext
	}
	switch strings.Split(m.MimeType, ";")[0] {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "audio/ogg":
		return ".ogg"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4":
		return ".m4a"
	case "application/pdf":
		return ".pdf"
	}
	return ""
}
