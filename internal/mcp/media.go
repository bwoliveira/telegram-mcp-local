package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// The media folder holds what the tools downloaded on request (download_media,
// transcribe_audio), one folder per chat. Every file in it can be
// downloaded again from Telegram while Telegram still holds it, so clearing it
// frees space without losing a message.

// RetentionSetting is the state key for how many days downloaded media is
// kept; empty or 0 keeps it forever.
const RetentionSetting = "media_retention_days"

type mediaFile struct {
	path    string
	chat    string
	kind    string
	size    int64
	modTime time.Time
}

func mediaKind(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".heic", ".bmp", ".tif", ".tiff":
		return "image"
	case ".mp4", ".mov", ".3gp", ".mkv", ".webm":
		return "video"
	case ".ogg", ".opus", ".mp3", ".m4a", ".aac", ".wav", ".amr":
		return "audio"
	}
	return "document"
}

// mediaFiles lists the downloaded files, optionally of one chat only.
func (s *Server) mediaFiles(chat string) ([]mediaFile, error) {
	if s.mediaDir == "" {
		return nil, errors.New("not implemented yet: media directory is not configured")
	}
	root := s.mediaDir
	if chat != "" {
		if strings.TrimSpace(chat) != chat || chat == "." || chat == ".." || strings.ContainsAny(chat, "/\\\x00") {
			return nil, engine.ErrInvalid
		}
		root = filepath.Join(s.mediaDir, safeName(chat))
	}
	if info, err := os.Lstat(s.mediaDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, engine.ErrInvalid
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if root != s.mediaDir {
		if err := mediaSafePath(s.mediaDir, root, false); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	var files []mediaFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			return err
		}
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(s.mediaDir, path)
		files = append(files, mediaFile{path: path, chat: strings.SplitN(filepath.ToSlash(rel), "/", 2)[0], kind: mediaKind(path),
			size: info.Size(), modTime: info.ModTime()})
		return nil
	})
	if os.IsNotExist(err) {
		err = nil
	}
	return files, err
}

// retentionDays defaults to 30 days. Only an explicit 0 disables retention.
func (s *Server) retentionDays(ctx context.Context) int {
	if s.state == nil {
		return 30
	}
	v, err := s.state.Setting(ctx, RetentionSetting)
	if err != nil || v == "" {
		return 30
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 30
	}
	return n
}

// MediaInventory is what the media folder holds.
type MediaInventory struct {
	Dir            string              `json:"dir"`
	Files          int                 `json:"files"`
	Bytes          int64               `json:"bytes"`
	ByType         map[string]mediaSum `json:"by_type"`
	ByChat         []chatMediaSum      `json:"by_chat"`
	DuplicateBytes int64               `json:"duplicate_bytes"`
	Oldest         *time.Time          `json:"oldest_download,omitempty"`
	Newest         *time.Time          `json:"newest_download,omitempty"`
	RetentionDays  int                 `json:"retention_days"`
	// Exports are the files export_messages wrote, kept apart: the person
	// asked for them, so retention never deletes them.
	Exports    mediaSum `json:"exports"`
	ExportsDir string   `json:"exports_dir"`
}

type mediaSum struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

type chatMediaSum struct {
	ChatJID string `json:"chat_id"`
	Name    string `json:"name"`
	mediaSum
}

// Inventory measures the media folder: by type, by chat (largest first), and
// how much is the same file kept twice.
func (s *Server) Inventory(ctx context.Context) (MediaInventory, error) {
	if err := ctx.Err(); err != nil {
		return MediaInventory{}, err
	}
	files, err := s.mediaFiles("")
	if err != nil {
		return MediaInventory{}, err
	}
	inv := MediaInventory{Dir: s.mediaDir, ByType: map[string]mediaSum{}, RetentionDays: s.retentionDays(ctx)}
	chats := map[string]*chatMediaSum{}
	bySize := map[int64][]string{}
	for _, f := range files {
		inv.Files++
		inv.Bytes += f.size
		t := inv.ByType[f.kind]
		t.Files++
		t.Bytes += f.size
		inv.ByType[f.kind] = t
		c, ok := chats[f.chat]
		if !ok {
			name := f.chat
			if s.index != nil {
				name = s.index.Names.Name(ctx, f.chat)
			}
			c = &chatMediaSum{ChatJID: f.chat, Name: name}
			chats[f.chat] = c
		}
		c.Files++
		c.Bytes += f.size
		bySize[f.size] = append(bySize[f.size], f.path)
		mt := f.modTime.UTC()
		if inv.Oldest == nil || mt.Before(*inv.Oldest) {
			inv.Oldest = &mt
		}
		if inv.Newest == nil || mt.After(*inv.Newest) {
			mt2 := mt
			inv.Newest = &mt2
		}
	}
	// Only files of equal size can be equal, so only those are hashed.
	for size, paths := range bySize {
		if len(paths) < 2 || size == 0 {
			continue
		}
		seen := map[string]bool{}
		for _, p := range paths {
			h, err := hashFile(p)
			if err != nil {
				continue
			}
			if seen[h] {
				inv.DuplicateBytes += size
			}
			seen[h] = true
		}
	}
	for _, c := range chats {
		inv.ByChat = append(inv.ByChat, *c)
	}
	sort.Slice(inv.ByChat, func(i, j int) bool { return inv.ByChat[i].Bytes > inv.ByChat[j].Bytes })
	if len(inv.ByChat) > 20 {
		inv.ByChat = inv.ByChat[:20]
	}
	inv.ExportsDir = s.exportDir
	if entries, err := os.ReadDir(s.exportDir); err == nil {
		for _, e := range entries {
			if info, err := e.Info(); err == nil && !e.IsDir() {
				inv.Exports.Files++
				inv.Exports.Bytes += info.Size()
			}
		}
	}
	return inv, nil
}

// PurgeExports removes every local export using the same durable queue seam.
func (s *Server) PurgeExports() (files int, bytes int64, err error) {
	return s.PurgeExportsContext(context.Background(), 0, false)
}

func hashFile(path string) (string, error) {
	f, err := mediaOpenFile(filepath.Dir(path), path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Server) mediaStats(ctx context.Context, _ arguments) map[string]any {
	inv, err := s.Inventory(ctx)
	if err != nil {
		return mediaToolError(err)
	}
	return textResult(map[string]any{"inventory": inv,
		"note": "local downloads and send-staging files; re-download availability is not guaranteed (protected, expired or deleted media may be unavailable); purging does not delete Telegram messages"}, false)
}

// PurgeMedia deletes downloaded files older than days (0: any age), of one
// chat or all, of at least minBytes, and reports what it removed. With dryRun
// it only reports what it would remove.
func (s *Server) PurgeMedia(chat string, days int, minBytes int64, dryRun bool) (files int, bytes int64, largest []map[string]any, err error) {
	return s.PurgeMediaContext(context.Background(), chat, days, minBytes, dryRun)
}

func (s *Server) purgeMedia(ctx context.Context, a arguments) map[string]any {
	if a.OlderThan < 0 || a.MinMegabyte < 0 || int64(a.MinMegabyte) > (1<<63-1)>>20 {
		return mediaToolError(engine.ErrInvalid)
	}
	if a.ChatJID != "" {
		peer, err := s.resolvePeer(ctx, a.ChatJID)
		if err != nil {
			return mediaToolError(err)
		}
		a.ChatJID = peerID(peer)
	}
	minBytes := int64(a.MinMegabyte) << 20
	if a.DryRun || !a.Confirm {
		files, bytes, largest, fingerprint, err := s.previewMediaPurge(a.ChatJID, a.OlderThan, minBytes)
		if err != nil {
			return mediaToolError(err)
		}
		return textResult(map[string]any{"preview": true, "preview_fingerprint": fingerprint, "would_delete_files": files, "would_free_bytes": bytes, "largest": largest, "expires_in_seconds": 600,
			"next": "obtain explicit human approval, then call purge_media with the same filters, confirm:true and this single-use preview_fingerprint; re-download is not guaranteed for protected, expired or deleted content"}, false)
	}
	files, bytes, err := s.confirmMediaPurge(ctx, a.ChatJID, a.OlderThan, minBytes, a.PreviewFingerprint)
	if err != nil {
		return mediaToolError(err)
	}
	return textResult(map[string]any{"deleted_files": files, "freed_bytes": bytes}, false)
}

// SweepMedia applies the retention setting: files downloaded more days ago
// than it allows are deleted. It does nothing while retention is off.
func (s *Server) SweepMedia(ctx context.Context) (int, int64, error) {
	days := s.retentionDays(ctx)
	var files int
	var bytes int64
	var err error
	if days > 0 {
		files, bytes, _, err = s.PurgeMediaContext(ctx, "", days, 0, false)
	}
	exportDays := s.exportRetentionDays(ctx)
	if exportDays > 0 {
		n, b, e := s.PurgeExportsContext(ctx, exportDays, false)
		files += n
		bytes += b
		err = errors.Join(err, e)
	}
	if err == nil && files > 0 {
		s.logger.Info("media retention", "deleted_files", files, "freed_bytes", bytes, "days", days)
	}
	return files, bytes, err
}

// SetRetention changes how many days downloaded media is kept; 0 keeps it.
func (s *Server) SetRetention(ctx context.Context, days int) error {
	if days < 0 || s.state == nil {
		return engine.ErrInvalid
	}
	return s.state.SetSetting(ctx, RetentionSetting, strconv.Itoa(days))
}
