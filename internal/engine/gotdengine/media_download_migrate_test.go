package gotdengine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgtest"
	"github.com/gotd/td/tgtest/cluster"
)

// Exercise the real gotd client/invoker, not a fake invoker that implements
// FILE_MIGRATE itself. Both MTProto DCs are synthetic loopback tgtest servers;
// the engine uses only offlineEngine's temporary fixture DB and no real session.
func TestMediaDownloadFileMigrate(t *testing.T) {
	e, _ := offlineEngine(t)
	key := mediaTestMessage(t, e, engine.Message{})
	const chunkSize = 512 << 10
	body := bytes.Repeat([]byte("synthetic-media-"), chunkSize/15+3)
	raw := mediaRawDocument(e)
	raw.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).Size = int64(len(body))

	c := cluster.NewCluster(cluster.Options{})
	primary := c.Dispatch(2, "primary")
	files := c.Dispatch(1, "files")
	var messageCalls, redirects, fileCalls, exports, imports atomic.Int32
	primary.HandleFunc(tg.MessagesGetMessagesRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.MessagesGetMessagesRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		if len(req.ID) != 1 {
			t.Errorf("message request IDs = %v", req.ID)
		} else if id, ok := req.ID[0].(*tg.InputMessageID); !ok || id.ID != key.ID {
			t.Errorf("message request ID = %#v", req.ID[0])
		}
		messageCalls.Add(1)
		return s.SendResult(r, &tg.MessagesMessages{Messages: []tg.MessageClass{raw}})
	})
	// If gotd accidentally migrates the primary connection, the post-download
	// read must fail rather than being silently answered by a common handler.
	files.HandleFunc(tg.MessagesGetMessagesRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		t.Error("message read migrated to file DC")
		return s.SendErr(r, tgerr.New(400, "UNEXPECTED_MESSAGE_READ"))
	})
	primary.HandleFunc(tg.AuthExportAuthorizationRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.AuthExportAuthorizationRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		if req.DCID != 1 {
			t.Errorf("auth export target = %d, want 1", req.DCID)
		}
		exports.Add(1)
		return s.SendResult(r, &tg.AuthExportedAuthorization{ID: 99, Bytes: []byte("synthetic-auth")})
	})
	files.HandleFunc(tg.AuthImportAuthorizationRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		var req tg.AuthImportAuthorizationRequest
		if err := req.Decode(r.Buf); err != nil {
			return err
		}
		if req.ID != 99 || string(req.Bytes) != "synthetic-auth" {
			t.Error("incorrect synthetic authorization import")
		}
		imports.Add(1)
		return s.SendResult(r, &tg.AuthAuthorization{User: &tg.User{ID: 99}})
	})
	checkFileRequest := func(r *tgtest.Request) (*tg.UploadGetFileRequest, error) {
		var req tg.UploadGetFileRequest
		if err := req.Decode(r.Buf); err != nil {
			return nil, err
		}
		loc, ok := req.Location.(*tg.InputDocumentFileLocation)
		if !ok || loc.ID != 31 || loc.AccessHash != 32 || string(loc.FileReference) != "fresh" || loc.ThumbSize != "" {
			t.Errorf("incorrect migrated file location: %#v", req.Location)
		}
		if req.Limit != chunkSize || req.CDNSupported {
			t.Errorf("file limit=%d cdn=%v", req.Limit, req.CDNSupported)
		}
		return &req, nil
	}
	primary.HandleFunc(tg.UploadGetFileRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		if _, err := checkFileRequest(r); err != nil {
			return err
		}
		redirects.Add(1)
		return s.SendErr(r, tgerr.New(303, "FILE_MIGRATE_1"))
	})
	files.HandleFunc(tg.UploadGetFileRequestTypeID, func(s *tgtest.Server, r *tgtest.Request) error {
		req, err := checkFileRequest(r)
		if err != nil {
			return err
		}
		call := fileCalls.Add(1)
		if req.Offset != int64(call-1)*chunkSize || req.Offset >= int64(len(body)) {
			t.Errorf("file chunk %d offset=%d", call, req.Offset)
			return s.SendErr(r, tgerr.New(400, "INVALID_OFFSET"))
		}
		end := min(int(req.Offset)+req.Limit, len(body))
		return s.SendResult(r, &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: body[int(req.Offset):end]})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	clusterDone := make(chan error, 1)
	go func() { clusterDone <- c.Up(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-clusterDone; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("stop synthetic DCs: %v", err)
		}
	})
	select {
	case <-c.Ready():
	case <-ctx.Done():
		t.Fatal("synthetic DCs did not start:", ctx.Err())
	}
	client := telegram.NewClient(1, "synthetic-test-only", telegram.Options{
		DC: 2, PublicKeys: c.Keys(), DCList: c.List(), Resolver: c.Resolver(), NoUpdates: true,
	})
	e.api = client.API()
	dir := t.TempDir()
	if err := client.Run(ctx, func(ctx context.Context) error {
		got, err := e.Download(ctx, engine.DownloadRequest{Key: key, Dir: dir, MaxBytes: int64(len(body))})
		if err != nil {
			return err
		}
		downloaded, err := os.ReadFile(got.Path)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(body)
		if !bytes.Equal(downloaded, body) || got.Bytes != int64(len(body)) || got.SHA256 != hex.EncodeToString(digest[:]) {
			t.Errorf("migrated download mismatch: bytes=%d result=%+v", len(downloaded), got)
		}
		// A regular RPC still belongs to DC2 after FILE_MIGRATE_1.
		_, err = client.API().MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: key.ID}})
		return err
	}); err != nil {
		t.Fatal("native gotd FILE_MIGRATE download:", err)
	}
	if messageCalls.Load() != 2 || redirects.Load() != 2 || fileCalls.Load() != 2 {
		t.Errorf("RPC counts: message reads=%d redirects=%d file chunks=%d; want 2 each", messageCalls.Load(), redirects.Load(), fileCalls.Load())
	}
	if exports.Load() != 1 || imports.Load() != 1 {
		t.Errorf("authorization transfers: exports=%d imports=%d; want one cached sub-DC connection", exports.Load(), imports.Load())
	}
}
