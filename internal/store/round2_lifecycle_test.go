package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Uses the canonical store.md §1 and lifecycle.md §8 placement.
func TestRound2CanonicalExportRetention(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	now := time.Unix(1800000000, 0)
	s, err := Open(ctx, filepath.Join(data, "accounts", "primary", "store.db"), "primary", WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	export := filepath.Join(data, "exports", "old.ndjson")
	if err := os.MkdirAll(filepath.Dir(export), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(export, []byte("private history"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writer.ExecContext(ctx, `INSERT INTO exports(id,account,path,filter_json,count,bytes,created_at) VALUES(1,'primary',?,'{}',1,15,?)`, export, now.Add(-8*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	cleanErr := s.Cleanup(ctx)
	var metadata, queued int
	if err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM exports`).Scan(&metadata); err != nil {
		t.Fatal(err)
	}
	if err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM files_to_unlink`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	_, statErr := os.Stat(export)
	t.Logf("cleanup=%v; exports rows=%d; queued=%d; file stat=%v", cleanErr, metadata, queued, statErr)
	if cleanErr != nil || !os.IsNotExist(statErr) || queued != 0 {
		t.Fatalf("canonical expired export was not removed: cleanup=%v; metadata=%d queued=%d file=%v", cleanErr, metadata, queued, statErr)
	}
}

func TestRound2ReadsRespectNewPeerProtection(t *testing.T) {
	ctx := context.Background()
	s := confOpen(t)
	p := engine.PeerRef{Type: engine.PeerChannel, ID: 8765}
	m := confMessage(p, 1, "old media message")
	m.Media = &engine.Media{Type: engine.MediaDocument, Downloadable: true, Filename: "private.txt", Remote: []byte("remote")}
	confApply(t, s, engine.Batch{Account: confAccount, Messages: []engine.Message{m}})
	confApply(t, s, engine.Batch{Account: confAccount, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: p, Protected: true}}}})
	peer, found, err := s.LookupPeer(ctx, confAccount, p)
	if err != nil || !found || !peer.Protected {
		t.Fatalf("peer update failed: %+v %v %v", peer, found, err)
	}
	page, err := s.ListMessages(ctx, confAccount, p, MessageOptions{})
	if err != nil || len(page.Messages) != 1 {
		t.Fatalf("read: %+v %v", page, err)
	}
	got := page.Messages[0]
	t.Logf("peer protected=%v; message protected=%v; downloadable=%v", peer.Protected, got.Protected, got.Media.Downloadable)
	if got.Media.Downloadable || !got.Protected {
		t.Fatalf("protected chat exposes unprotected/downloadable message: protected=%v downloadable=%v", got.Protected, got.Media.Downloadable)
	}
}
