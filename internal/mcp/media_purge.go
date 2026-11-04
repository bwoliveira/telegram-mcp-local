package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// ExportRetentionSetting defaults to 7 days; an explicit 0 disables retention.
const ExportRetentionSetting = "export_retention_days"

type approvedMediaFile struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	ModTime int64  `json:"mod_time"`
	Digest  string `json:"sha256"`
}

type purgeApproval struct {
	binding [32]byte
	files   []approvedMediaFile
	expires time.Time
}

func purgeBinding(chat string, days int, minBytes int64) [32]byte {
	b, _ := json.Marshal(struct {
		Chat     string `json:"chat"`
		Days     int    `json:"days"`
		MinBytes int64  `json:"min_bytes"`
	}{chat, days, minBytes})
	return sha256.Sum256(b)
}

func mediaDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return openMediaDigest(f)
}

func openMediaDigest(f *os.File) (string, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Serialize recovery with live quarantines, including separate Server instances.
var mediaQuarantineMu sync.Mutex

// The original basename is stored atomically by the rename into this directory.
// Restoring uses Link, not Rename: it must never overwrite a fresh download.
func restoreMediaQuarantine(parent *os.Root, quarantine string) error {
	info, err := parent.Lstat(quarantine)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		// Legacy flat quarantines have no original-name mapping. Fail visibly
		// rather than guess a destination or silently lose their bytes.
		return errors.New("invalid or legacy purge quarantine requires manual recovery")
	}
	q, err := parent.OpenRoot(quarantine)
	if err != nil {
		return err
	}
	defer q.Close()
	opened, err := q.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return engine.ErrInvalid
	}
	f, err := q.Open(".")
	if err != nil {
		return err
	}
	entries, readErr := f.ReadDir(-1)
	if err := errors.Join(readErr, f.Close()); err != nil {
		return err
	}
	if len(entries) > 1 {
		return errors.New("invalid purge quarantine requires manual recovery")
	}
	if len(entries) == 1 {
		name := entries[0].Name()
		info, err := q.Lstat(name)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || strings.HasPrefix(name, ".") {
			return engine.ErrInvalid
		}
		if err := parent.Link(filepath.Join(quarantine, name), name); err != nil {
			existing, statErr := parent.Lstat(name)
			// A crash after Link but before Remove leaves two names for the
			// same inode. Only that exact case is safe to finish automatically.
			if !errors.Is(err, os.ErrExist) || statErr != nil || !existing.Mode().IsRegular() || !os.SameFile(info, existing) {
				return errors.New("purge recovery destination exists or cannot be restored; quarantined bytes preserved")
			}
		}
		// Link resolves a path beneath parent rather than the pinned q handle.
		// If that path was swapped, do not discard the validated source bytes.
		restored, err := parent.Lstat(name)
		if err != nil || !restored.Mode().IsRegular() || !os.SameFile(info, restored) {
			return errors.New("purge recovery source changed; quarantined bytes preserved")
		}
		if err := syncMediaPurgeDir(parent); err != nil {
			return err
		}
		if err := q.Remove(name); err != nil {
			return err
		}
	}
	if err := syncMediaPurgeDir(q); err != nil {
		return err
	}
	if err := parent.Remove(quarantine); err != nil {
		return err
	}
	return syncMediaPurgeDir(parent)
}

// Recovery is deliberately rollback-only. Fresh filters/approval, not a stale
// quarantine, decide whether restored bytes may be deleted. Dry runs do not
// recover files; the next destructive purge scans the whole owned media root.
func recoverMediaPurges(path string) error {
	if path == "" {
		return nil
	}
	mediaQuarantineMu.Lock()
	defer mediaQuarantineMu.Unlock()
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return engine.ErrInvalid
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return engine.ErrInvalid
	}
	var scan func(*os.Root) error
	scan = func(parent *os.Root) error {
		f, err := parent.Open(".")
		if err != nil {
			return err
		}
		entries, readErr := f.ReadDir(-1)
		if err := errors.Join(readErr, f.Close()); err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, ".purge-") && len(name) == len(".purge-")+32 {
				if _, err := hex.DecodeString(strings.TrimPrefix(name, ".purge-")); err == nil {
					if err := restoreMediaQuarantine(parent, name); err != nil {
						return err
					}
					continue
				}
			}
			if strings.HasPrefix(name, ".") || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			before, err := parent.Lstat(name)
			if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
				return engine.ErrInvalid
			}
			child, err := parent.OpenRoot(name)
			if err != nil {
				return err
			}
			opened, statErr := child.Stat(".")
			if statErr != nil || !os.SameFile(before, opened) {
				_ = child.Close()
				return engine.ErrInvalid
			}
			err = scan(child)
			closeErr := child.Close()
			if err := errors.Join(err, closeErr); err != nil {
				return err
			}
		}
		return nil
	}
	return scan(root)
}

func removeApprovedMedia(root string, expected approvedMediaFile) (err error) {
	mediaQuarantineMu.Lock()
	defer mediaQuarantineMu.Unlock()
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(expected.Path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absolute)
	if err != nil || !filepath.IsLocal(rel) || rel == "." {
		return engine.ErrInvalid
	}
	current, err := os.OpenRoot(absRoot)
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		info, err := current.Lstat(part)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return engine.ErrInvalid
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			return err
		}
		_ = current.Close()
		current = next
	}
	name := parts[len(parts)-1]
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	quarantine := ".purge-" + hex.EncodeToString(nonce)
	if err := current.Mkdir(quarantine, 0o700); err != nil {
		return err
	}
	// Persist the original-name container before moving any bytes into it.
	if err := syncMediaPurgeDir(current); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, restoreMediaQuarantine(current, quarantine)) }()
	quarantinedName := filepath.Join(quarantine, name)
	if err := current.Rename(name, quarantinedName); err != nil {
		return err
	}
	q, err := current.OpenRoot(quarantine)
	if err != nil {
		return err
	}
	defer q.Close()
	if err := errors.Join(syncMediaPurgeDir(q), syncMediaPurgeDir(current)); err != nil {
		return err
	}
	info, err := current.Lstat(quarantinedName)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return engine.ErrInvalid
	}
	f, err := current.Open(quarantinedName)
	if err != nil {
		return err
	}
	info, statErr := f.Stat()
	digest, digestErr := openMediaDigest(f)
	closeErr := f.Close()
	if statErr != nil || digestErr != nil || closeErr != nil || !info.Mode().IsRegular() || info.Size() != expected.Size || info.ModTime().UnixNano() != expected.ModTime || digest != expected.Digest {
		return errors.New("approved media set changed; preview it again")
	}
	if err := current.Remove(quarantinedName); err != nil {
		return err
	}
	// The deferred recovery removes the now-empty container and syncs both
	// directories; a crash before then leaves an idempotent empty recovery.
	return nil
}

func approveMediaFiles(files []mediaFile, binding [32]byte) ([]approvedMediaFile, string, error) {
	approved := make([]approvedMediaFile, 0, len(files))
	for _, file := range files {
		digest, err := mediaDigest(file.path)
		if err != nil {
			return nil, "", err
		}
		approved = append(approved, approvedMediaFile{Path: file.path, Size: file.size, ModTime: file.modTime.UnixNano(), Digest: digest})
	}
	b, err := json.Marshal(approved)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	payload := append([]byte("purge_media\x00"), binding[:]...)
	payload = append(payload, nonce...)
	payload = append(payload, b...)
	fingerprint := sha256.Sum256(payload)
	return approved, hex.EncodeToString(fingerprint[:]), nil
}

func (s *Server) verifyApprovedMediaFiles(approved []approvedMediaFile) ([]mediaFile, error) {
	files := make([]mediaFile, 0, len(approved))
	for _, expected := range approved {
		if err := mediaSafePath(s.mediaDir, expected.Path, true); err != nil {
			return nil, err
		}
		info, err := os.Lstat(expected.Path)
		if err != nil || !info.Mode().IsRegular() || info.Size() != expected.Size || info.ModTime().UnixNano() != expected.ModTime {
			return nil, errors.New("approved media set changed; preview it again")
		}
		digest, err := mediaDigest(expected.Path)
		if err != nil || digest != expected.Digest {
			return nil, errors.New("approved media set changed; preview it again")
		}
		files = append(files, mediaFile{path: expected.Path, size: expected.Size, modTime: info.ModTime()})
	}
	return files, nil
}

func (s *Server) exportRetentionDays(ctx context.Context) int {
	if s.state == nil {
		return 7
	}
	v, err := s.state.Setting(ctx, ExportRetentionSetting)
	if err != nil || v == "" {
		return 7
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 7
	}
	return n
}

// SetExportRetention controls exports independently of downloaded media.
func (s *Server) SetExportRetention(ctx context.Context, days int) error {
	if s.state == nil || days < 0 {
		return engine.ErrInvalid
	}
	return s.state.SetSetting(ctx, ExportRetentionSetting, strconv.Itoa(days))
}

// PurgeExportsContext removes local exports of at least the requested age;
// zero selects all ages. It never deletes Telegram messages or transcripts.
func (s *Server) PurgeExportsContext(ctx context.Context, days int, dryRun bool) (files int, bytes int64, err error) {
	if days < 0 || days > 365000 {
		return 0, 0, engine.ErrInvalid
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if s.exportDir == "" {
		return 0, 0, nil
	}
	info, err := os.Lstat(s.exportDir)
	if os.IsNotExist(err) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return 0, 0, engine.ErrInvalid
	}
	entries, err := os.ReadDir(s.exportDir)
	if err != nil {
		return 0, 0, err
	}
	cutoff := s.clock().AddDate(0, 0, -days)
	picked := []mediaFile{}
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil {
			return 0, 0, e
		}
		if !info.Mode().IsRegular() || strings.HasPrefix(entry.Name(), ".") || (days > 0 && info.ModTime().After(cutoff)) {
			continue
		}
		picked = append(picked, mediaFile{path: filepath.Join(s.exportDir, entry.Name()), size: info.Size(), modTime: info.ModTime()})
	}
	if !dryRun {
		if err = s.queueMediaPurge(ctx, picked); err != nil {
			return 0, 0, err
		}
	}
	for _, f := range picked {
		if e := ctx.Err(); e != nil {
			return files, bytes, errors.Join(err, e)
		}
		if !dryRun {
			if s.readStore == nil {
				if e := mediaRemove(s.exportDir, f.path); e != nil && !os.IsNotExist(e) {
					err = errors.Join(err, e)
					continue
				}
			}
			s.links.RevokePath(f.path)
		}
		files++
		bytes += f.size
	}
	return
}

// MediaPurger is the store transaction seam required by a persistent Telegram
// deployment. It removes matching media_files/exports metadata and enqueues the
// paths in files_to_unlink atomically. It must scope every row to account, refuse
// paths outside its owned media/export roots and leave the queue crash-safe.
// Retention delegates unlinking to the Store under its writer lock. A confirmed
// fingerprinted MCP purge does not touch that shared queue: it quarantines,
// re-verifies and removes only the approved bytes. Metadata pointing at a
// missing media file is harmless and is refreshed by a later download or
// removed by retention.
type MediaPurger interface {
	PurgeMediaPaths(context.Context, engine.AccountID, []string) error
	UnlinkQueuedMedia(context.Context, engine.AccountID) error
}

func (s *Server) queueMediaPurgePaths(ctx context.Context, files []mediaFile) (MediaPurger, engine.AccountID, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if len(files) == 0 || s.readStore == nil {
		return nil, "", nil
	}
	purger, ok := s.readStore.(MediaPurger)
	if !ok {
		return nil, "", engine.ErrNotSupported
	}
	if s.eng == nil {
		return nil, "", engine.ErrNotSupported
	}
	acc, err := s.eng.Account(ctx)
	if err != nil {
		return nil, "", err
	}
	if acc.ID == "" {
		return nil, "", engine.ErrAccountMismatch
	}
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.path
	}
	if err := purger.PurgeMediaPaths(ctx, acc.ID, paths); err != nil {
		return nil, "", err
	}
	return purger, acc.ID, nil
}

func (s *Server) queueMediaPurge(ctx context.Context, files []mediaFile) error {
	purger, account, err := s.queueMediaPurgePaths(ctx, files)
	if err != nil || purger == nil {
		return err
	}
	return purger.UnlinkQueuedMedia(ctx, account)
}

func mediaRemove(root, path string) error {
	if err := mediaSafePath(root, path, true); err != nil {
		return err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absolute)
	if err != nil {
		return err
	}
	// Pin each ancestor. os.Root alone allows symlink aliases within the root;
	// opening one directory at a time prevents a parent rename redirecting Remove.
	current, err := os.OpenRoot(absRoot)
	if err != nil {
		return err
	}
	defer func() { _ = current.Close() }()
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		info, e := current.Lstat(part)
		if e != nil {
			return e
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return engine.ErrInvalid
		}
		next, e := current.OpenRoot(part)
		if e != nil {
			return e
		}
		_ = current.Close()
		current = next
	}
	name := parts[len(parts)-1]
	info, err := current.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return engine.ErrInvalid
	}
	return current.Remove(name)
}

func (s *Server) mediaPurgeSelection(chat string, days int, minBytes int64) ([]mediaFile, []map[string]any, error) {
	if days < 0 || days > 365000 || minBytes < 0 {
		return nil, nil, engine.ErrInvalid
	}
	all, err := s.mediaFiles(chat)
	if err != nil {
		return nil, nil, err
	}
	cutoff := s.clock().AddDate(0, 0, -days)
	picked := make([]mediaFile, 0)
	for _, f := range all {
		if (days == 0 || !f.modTime.After(cutoff)) && f.size >= minBytes {
			picked = append(picked, f)
		}
	}
	sort.Slice(picked, func(i, j int) bool {
		if picked[i].size == picked[j].size {
			return picked[i].path < picked[j].path
		}
		return picked[i].size > picked[j].size
	})
	largest := make([]map[string]any, 0, min(10, len(picked)))
	for i, f := range picked {
		if i < 10 {
			largest = append(largest, map[string]any{"path": f.path, "bytes": f.size, "downloaded_at": f.modTime.UTC()})
		}
	}
	return picked, largest, nil
}

func (s *Server) previewMediaPurge(chat string, days int, minBytes int64) (int, int64, []map[string]any, string, error) {
	picked, largest, err := s.mediaPurgeSelection(chat, days, minBytes)
	if err != nil {
		return 0, 0, nil, "", err
	}
	binding := purgeBinding(chat, days, minBytes)
	approved, fingerprint, err := approveMediaFiles(picked, binding)
	if err != nil {
		return 0, 0, nil, "", err
	}
	var bytes int64
	for _, file := range picked {
		bytes += file.size
	}
	s.previewMu.Lock()
	defer s.previewMu.Unlock()
	for token, approval := range s.purges {
		if !s.clock().Before(approval.expires) {
			delete(s.purges, token)
		}
	}
	if len(s.purges) >= 1024 {
		return 0, 0, nil, "", errors.New("too many pending purge previews; wait for them to expire")
	}
	s.purges[fingerprint] = purgeApproval{binding: binding, files: approved, expires: s.clock().Add(10 * time.Minute)}
	return len(picked), bytes, largest, fingerprint, nil
}

func (s *Server) confirmMediaPurge(ctx context.Context, chat string, days int, minBytes int64, fingerprint string) (int, int64, error) {
	if fingerprint == "" {
		return 0, 0, errors.New("preview_fingerprint is required; preview the purge and obtain explicit human approval first")
	}
	binding := purgeBinding(chat, days, minBytes)
	s.previewMu.Lock()
	approval, ok := s.purges[fingerprint]
	if ok {
		delete(s.purges, fingerprint)
	}
	s.previewMu.Unlock()
	if !ok || approval.binding != binding || !s.clock().Before(approval.expires) {
		return 0, 0, errors.New("purge preview expired, already used, or does not match these filters; preview it again")
	}
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	if err := recoverMediaPurges(s.mediaDir); err != nil {
		return 0, 0, err
	}
	files, err := s.verifyApprovedMediaFiles(approval.files)
	if err != nil {
		return 0, 0, err
	}
	var removed int
	var bytes int64
	for i, file := range files {
		if err := ctx.Err(); err != nil {
			return removed, bytes, err
		}
		if err := removeApprovedMedia(s.mediaDir, approval.files[i]); err != nil {
			return removed, bytes, err
		}
		s.links.RevokePath(file.path)
		removed++
		bytes += file.size
	}
	return removed, bytes, nil
}

// PurgeMediaContext shares filters and accounting between HTTP, MCP and the
// retention job. Zero days selects every age; a dry run never touches metadata,
// files or links. Failures are returned, not silently counted as successful.
func (s *Server) PurgeMediaContext(ctx context.Context, chat string, days int, minBytes int64, dryRun bool) (files int, bytes int64, largest []map[string]any, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	picked, largest, err := s.mediaPurgeSelection(chat, days, minBytes)
	if err != nil {
		return 0, 0, nil, err
	}
	if !dryRun {
		if err := recoverMediaPurges(s.mediaDir); err != nil {
			return 0, 0, nil, err
		}
		picked, largest, err = s.mediaPurgeSelection(chat, days, minBytes)
		if err != nil {
			return 0, 0, nil, err
		}
	}
	if !dryRun {
		if err = s.queueMediaPurge(ctx, picked); err != nil {
			return 0, 0, largest, err
		}
	}
	for _, f := range picked {
		if e := ctx.Err(); e != nil {
			return files, bytes, largest, errors.Join(err, e)
		}
		if !dryRun {
			if s.readStore == nil {
				e := mediaRemove(s.mediaDir, f.path)
				if e != nil && !os.IsNotExist(e) {
					err = errors.Join(err, e)
					continue
				}
			}
			s.links.RevokePath(f.path)
		}
		files++
		bytes += f.size
	}
	return
}
