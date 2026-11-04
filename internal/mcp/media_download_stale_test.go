package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	fakeengine "github.com/bwoliveira/telegram-mcp-local/internal/engine/fake"
)

// Only the local snapshot is stale; the fake downloader retains current media.
type staleDownloadEngine struct {
	*fakeengine.Controller
	key       engine.MessageKey
	edit      func(*engine.Message)
	editPeer  func(*engine.Peer)
	downloads int
}

func (e *staleDownloadEngine) ReadMessages(ctx context.Context, account engine.AccountID) ([]engine.Message, error) {
	messages, err := e.Controller.ReadMessages(ctx, account)
	for i := range messages {
		if messages[i].Key == e.key {
			media := *messages[i].Media
			media.Downloadable = false
			messages[i].Media = &media
			if e.edit != nil {
				e.edit(&messages[i])
			}
		}
	}
	return messages, err
}

func (e *staleDownloadEngine) ReadPeers(ctx context.Context, account engine.AccountID) ([]engine.Peer, error) {
	peers, err := e.Controller.ReadPeers(ctx, account)
	for i := range peers {
		if peers[i].Ref == e.key.Peer && e.editPeer != nil {
			e.editPeer(&peers[i])
		}
	}
	return peers, err
}

func (e *staleDownloadEngine) Download(ctx context.Context, request engine.DownloadRequest) (engine.Download, error) {
	e.downloads++
	return e.Controller.Download(ctx, request)
}

func TestDownloadMediaStaleAvailability(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edit     func(*engine.Message)
		editPeer func(*engine.Peer)
		want     error
	}{
		{name: "allowed"},
		{name: "protected_message", edit: func(m *engine.Message) { m.Protected = true }, want: engine.ErrProtectedContent},
		{name: "temporary_media", edit: func(m *engine.Message) { m.Media.TTLSeconds = 20 }, want: engine.ErrProtectedContent},
		{name: "protected_peer", editPeer: func(p *engine.Peer) { p.Protected = true }, want: engine.ErrProtectedContent},
		{name: "lost_access", editPeer: func(p *engine.Peer) { p.Access = engine.AccessLost }, want: engine.ErrNoAccess},
		{name: "expired", edit: func(m *engine.Message) { at := fakeengine.Epoch().Add(-time.Second); m.ExpiresAt = &at }, want: engine.ErrMessageNotFound},
		{name: "nil_media", edit: func(m *engine.Message) { m.Media = nil }, want: engine.ErrNotSupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, controller := newFakeServer(t, engine.ModeReadOnly)
			key := engine.MessageKey{Account: "primary", Peer: fakeengine.PrivatePeer, ID: 4}
			e := &staleDownloadEngine{Controller: controller, key: key, edit: tc.edit, editPeer: tc.editPeer}
			s.eng = e
			s.mediaDir = filepath.Join(t.TempDir(), "media")

			download, err := s.DownloadMedia(context.Background(), key)
			if !errors.Is(err, tc.want) {
				t.Fatalf("DownloadMedia error = %v, want %v (engine downloads=%d)", err, tc.want, e.downloads)
			}
			if tc.want != nil {
				if e.downloads != 0 {
					t.Fatalf("forbidden download reached engine %d times", e.downloads)
				}
				if _, err := os.Stat(s.mediaDir); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("guard created media directory: %v", err)
				}
				return
			}
			if e.downloads != 1 {
				t.Fatalf("engine downloads = %d, want 1", e.downloads)
			}
			body, err := os.ReadFile(download.Path)
			if err != nil || len(body) == 0 || int64(len(body)) != download.Bytes {
				t.Fatalf("downloaded bytes = %d, declared = %d, error = %v", len(body), download.Bytes, err)
			}
		})
	}
}
