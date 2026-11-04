package mcp

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// StatsExportRegistry must be supplied by the durable ReadStore (or an adapter
// embedding ReadStore and this registry for fake snapshots). Without it export
// fails closed: no file may escape lifecycle retention by accident.
type StatsExportRegistry interface {
	ReserveStatsExport(context.Context, engine.AccountID, string, string) error
	CommitStatsExport(context.Context, engine.AccountID, string, int, int64) error
	AbortStatsExport(context.Context, engine.AccountID, string) error
}
type statsExportDirectory interface {
	StatsExportDir(context.Context, engine.AccountID) (string, error)
}
type statsExporter interface {
	StreamExportMessages(context.Context, engine.AccountID, store.StatsFilter, func(engine.Message) error) error
}

func statsExportFilter(f store.StatsFilter) map[string]any {
	out := map[string]any{"direction": f.Direction, "media_type": f.MediaType, "exclude_groups": f.ExcludeGroups}
	if f.Peer != nil {
		out["chat_id"] = peerID(*f.Peer)
	}
	if !f.Since.IsZero() {
		out["since"] = f.Since
	}
	if !f.Until.IsZero() {
		out["until"] = f.Until
	}
	return out
}

// exportStoreMessages implements T29 without touching the inherited index/state.
// The integrator routes export_messages here. Only safe presentation fields are
// encoded, never Media.Remote, access hashes, tokens or local media paths.
func (s *Server) exportStoreMessages(ctx context.Context, a arguments) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	f, err := s.statsFilter(ctx, a)
	if err != nil {
		return toolError("%v", err)
	}
	account, fail := s.peopleAccount(ctx)
	if fail != nil {
		return fail
	}
	registry, ok := s.readStore.(StatsExportRegistry)
	if !ok {
		return toolError("export_messages requires a local export retention registry on ReadStore")
	}
	if s.exportDir == "" {
		return toolError("export directory is not configured")
	}
	dir, err := filepath.Abs(s.exportDir)
	if err != nil {
		return toolError("%v", err)
	}
	if r, ok := s.readStore.(statsExportDirectory); ok {
		expected, err := r.StatsExportDir(ctx, account)
		if err != nil {
			return engineError(err)
		}
		if dir != expected {
			return toolError("configured export directory does not match the store retention root")
		}
	}
	coverage, err := s.statsCoverage(ctx, account, f.Peer)
	if err != nil {
		return engineError(err)
	}
	peers, err := s.readStore.ReadPeers(ctx, account)
	if err != nil {
		return engineError(err)
	}
	snapshot := readSnapshot{account: account, peers: map[engine.PeerRef]engine.Peer{}}
	for _, p := range peers {
		snapshot.peers[p.Ref] = p
	}
	filter := statsExportFilter(f)
	filterJSON, err := json.Marshal(filter)
	if err != nil {
		return toolError("%v", err)
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return toolError("%v", err)
	}
	name := "telegram-local-" + hex.EncodeToString(random[:]) + ".ndjson"
	partial := name + ".partial"
	path := filepath.Join(dir, name)
	if err = os.MkdirAll(dir, 0700); err != nil {
		return toolError("%v", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return toolError("%v", err)
	}
	if !info.IsDir() || service.CheckPrivateDataPath(dir, true) != nil {
		return toolError("invalid export directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return toolError("%v", err)
	}
	defer root.Close()
	// Persist both possible names BEFORE any message content reaches disk.
	if err = registry.ReserveStatsExport(ctx, account, path, string(filterJSON)); err != nil {
		return toolError("export retention reservation failed: %v", err)
	}
	var file *os.File
	created, published, registered := false, false, false
	defer func() {
		if file != nil {
			file.Close()
		}
		if !registered {
			if created {
				_ = root.Remove(partial)
			}
			if published {
				_ = root.Remove(name)
			}
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = registry.AbortStatsExport(cleanup, account, path)
		}
	}()
	file, err = root.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return toolError("%v", err)
	}
	created = true
	if err = file.Chmod(0600); err != nil {
		return toolError("%v", err)
	}
	writer := bufio.NewWriterSize(file, 256<<10)
	enc := json.NewEncoder(writer)
	enc.SetEscapeHTML(false)
	header := map[string]any{"type": "header", "format": "ndjson", "version": 1, "account": account, "created_at": s.clock().UTC(), "filter": filter, "coverage": coverage, "untrusted_content": UntrustedContent}
	if err = enc.Encode(header); err != nil {
		return toolError("export failed: %v", err)
	}
	count := 0
	var first, last time.Time
	encode := func(m engine.Message) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.Key.Account != account {
			return engine.ErrAccountMismatch
		}
		// Defense in depth: even a custom stream adapter cannot export ephemeral
		// content. Do not try to shorten its retention to a still-future expiry.
		if m.TTLPeriod > 0 || m.ExpiresAt != nil || m.Media != nil && m.Media.TTLSeconds > 0 {
			return nil
		}
		out := snapshot.message(m)
		if out.Protected {
			out.MediaType = ""
			out.MimeType = ""
			out.Filename = ""
			out.Media = nil
			out.Poll = nil
			out.Location = nil
		}
		if count == 0 {
			first = m.Date
		}
		last = m.Date
		if err := enc.Encode(out); err != nil {
			return err
		}
		count++
		return nil
	}
	if r, ok := s.readStore.(statsExporter); ok {
		err = r.StreamExportMessages(ctx, account, f, encode)
	} else {
		snapshot.messages, err = s.readStore.ReadMessages(ctx, account)
		if err == nil {
			sort.SliceStable(snapshot.messages, func(i, j int) bool {
				a, b := snapshot.messages[i], snapshot.messages[j]
				if !a.Date.Equal(b.Date) {
					return a.Date.Before(b.Date)
				}
				if a.Key.Peer.Type != b.Key.Peer.Type {
					return a.Key.Peer.Type < b.Key.Peer.Type
				}
				if a.Key.Peer.ID != b.Key.Peer.ID {
					return a.Key.Peer.ID < b.Key.Peer.ID
				}
				return a.Key.ID < b.Key.ID
			})
			for _, m := range snapshot.messages {
				if !statsMatches(f, m) {
					continue
				}
				if err = encode(m); err != nil {
					break
				}
			}
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = writer.Flush()
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = root.Rename(partial, name)
		published = err == nil
	}
	if err != nil {
		return toolError("export failed: %v", err)
	}
	info, err = root.Stat(name)
	if err != nil {
		return toolError("export failed: %v", err)
	}
	if err = registry.CommitStatsExport(ctx, account, path, count, info.Size()); err != nil {
		return toolError("export retention registration failed: %v", err)
	}
	registered = true
	result := map[string]any{"path": path, "format": "ndjson", "count": count, "bytes": info.Size(), "retention_registered": true, "coverage": coverage, "source": "telegram_local_store", "untrusted_content": UntrustedContent,
		"note": "NDJSON header followed by messages, oldest first; ephemeral and deleted messages omitted; protected text is marked and protected media omitted; file is on the daemon and participates in export retention"}
	if count > 0 {
		result["first_timestamp"], result["last_timestamp"] = first, last
	}
	return textResult(result, false)
}

// Compile-time checks ensure the durable adapter supports the local extensions.
var _ statsAggregator = (*store.Store)(nil)
var _ statsCoverageReader = (*store.Store)(nil)
var _ statsExporter = (*store.Store)(nil)
var _ StatsExportRegistry = (*store.Store)(nil)
var _ statsExportDirectory = (*store.Store)(nil)
