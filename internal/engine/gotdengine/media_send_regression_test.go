package gotdengine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/telegram/updates"
	"github.com/gotd/td/tg"
)

// Hold the real manager after initialization but before its worker starts.
// Handle can enqueue the send result, but cannot persist it for these tests.
func holdMediaSendManager(t *testing.T, e *Engine) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	started, release := make(chan struct{}), make(chan struct{})
	api := tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.UpdatesGetStateRequest:
			*out.(*tg.UpdatesState) = tg.UpdatesState{Pts: 1, Date: 1000}
		case *tg.UpdatesGetDifferenceRequest:
			out.(*tg.UpdatesDifferenceBox).Difference = &tg.UpdatesDifferenceEmpty{Date: 1000}
		case *tg.UpdatesGetChannelDifferenceRequest:
			out.(*tg.UpdatesChannelDifferenceBox).ChannelDifference = &tg.UpdatesChannelDifferenceEmpty{Final: true, Pts: r.Pts}
		default:
			t.Errorf("unexpected manager RPC %T", in)
			return engine.ErrNotSupported
		}
		return nil
	}))
	done := make(chan error, 1)
	go func() {
		done <- e.manager.Run(ctx, api, 99, updates.AuthOptions{OnStart: func(context.Context) { close(started); <-release }})
	}()
	t.Cleanup(func() { cancel(); close(release); <-done; e.manager.Reset() })
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return ctx
}

func TestMediaSendRPCResultImmediatelyDownloadable(t *testing.T) {
	for _, envelope := range []string{"updates", "combined", "short"} {
		for _, channel := range []bool{false, true} {
			name := envelope + "/user"
			if channel {
				name = envelope + "/channel"
			}
			t.Run(name, func(t *testing.T) {
				e, s := writeFixture(t)
				ctx := holdMediaSendManager(t, e)
				ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
				if channel {
					ref = engine.PeerRef{Type: engine.PeerChannel, ID: 30}
				}
				key := engine.MessageKey{Account: "primary", Peer: ref, ID: 504}
				body := []byte("synthetic send/download bytes")
				path := filepath.Join(t.TempDir(), "upload.txt")
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				raw := &tg.Message{ID: key.ID, PeerID: mediaPeerClass(ref), FromID: &tg.PeerUser{UserID: 99}, Out: true, Date: 1000, Message: "caption", Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 31, AccessHash: 32, FileReference: []byte("synthetic-file-reference"), Size: int64(len(body)), MimeType: "text/plain", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "upload.txt"}}}}}
				var messageUpdate tg.UpdateClass = &tg.UpdateNewMessage{Message: raw, Pts: 2, PtsCount: 1}
				if channel {
					messageUpdate = &tg.UpdateNewChannelMessage{Message: raw, Pts: 2, PtsCount: 1}
				}
				var returned tg.UpdatesClass
				uploads, sends, fetches, chunks := 0, 0, 0, 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					switch r := in.(type) {
					case *tg.UploadSaveFilePartRequest:
						uploads++
						out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
					case *tg.MessagesSendMediaRequest:
						sends++
						us := []tg.UpdateClass{&tg.UpdateMessageID{ID: key.ID, RandomID: r.RandomID}, messageUpdate}
						switch envelope {
						case "updates":
							returned = &tg.Updates{Updates: us, Date: 1000, Seq: 1}
						case "combined":
							returned = &tg.UpdatesCombined{Updates: us, Date: 1000, SeqStart: 1, Seq: 1}
						case "short":
							returned = &tg.UpdateShort{Update: messageUpdate, Date: 1000}
						}
						out.(*tg.UpdatesBox).Updates = returned
					case *tg.MessagesGetMessagesRequest:
						if channel || len(r.ID) != 1 || r.ID[0].(*tg.InputMessageID).ID != key.ID {
							t.Fatal("download fetched the wrong message")
						}
						fetches++
						out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{raw}}
					case *tg.ChannelsGetMessagesRequest:
						if !channel || r.Channel.(*tg.InputChannel).ChannelID != ref.ID || len(r.ID) != 1 || r.ID[0].(*tg.InputMessageID).ID != key.ID {
							t.Fatal("download fetched the wrong channel message")
						}
						fetches++
						out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: []tg.MessageClass{raw}}
					case *tg.UploadGetFileRequest:
						chunks++
						loc, ok := r.Location.(*tg.InputDocumentFileLocation)
						if !ok || loc.ID != 31 || loc.AccessHash != 32 || string(loc.FileReference) != "synthetic-file-reference" || r.Offset != 0 {
							t.Fatal("download used the wrong file location")
						}
						out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: body}
					default:
						t.Fatalf("forbidden RPC %T", in)
					}
					return nil
				}))
				events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 10})
				r := engine.SendMediaRequest{Peer: ref, Kind: engine.MediaDocument, Path: path, Caption: "caption", MimeType: "text/plain", WriteOptions: engine.WriteOptions{DryRun: true}}
				preview, err := e.SendMedia(ctx, r)
				if err != nil || uploads != 0 || sends != 0 {
					t.Fatalf("preview: err=%v uploads=%d sends=%d", err, uploads, sends)
				}
				r.DryRun = false
				r.ExpectFingerprint = preview.Preview.Fingerprint
				result, err := e.SendMedia(ctx, r)
				if err != nil || !result.Done || result.Affected != 1 || uploads != 1 || sends != 1 {
					t.Fatalf("send: result=%+v err=%v uploads=%d sends=%d", result, err, uploads, sends)
				}
				// UpdateShort has no random-ID receipt; never invent one.
				if envelope != "short" && (len(result.Messages) != 1 || result.Messages[0] != key) {
					t.Fatalf("wrong accepted message keys: %+v", result.Messages)
				}
				m, found, err := s.Message(ctx, key)
				if err != nil || !found || !m.Out || m.Text != "caption" || m.Media == nil {
					t.Fatalf("send returned before RPC message was ingested: found=%v message=%+v err=%v", found, m, err)
				}
				download, err := e.Download(ctx, engine.DownloadRequest{Key: key, Dir: t.TempDir(), MaxBytes: int64(len(body))})
				if err != nil {
					t.Fatalf("immediate download: %v", err)
				}
				got, err := os.ReadFile(download.Path)
				if err != nil || string(got) != string(body) || fetches != 1 || chunks != 1 {
					t.Fatalf("download: bytes=%q err=%v fetches=%d chunks=%d", got, err, fetches, chunks)
				}
				select {
				case event := <-events:
					if event.Kind != engine.EventMessage || event.Message == nil || event.Message.Key != key {
						t.Fatalf("wrong committed event: %+v", event)
					}
				default:
					t.Fatal("missing committed own-message event")
				}
				// The manager's later delivery must remain an idempotent replay.
				if err := e.handle(ctx, returned); err != nil {
					t.Fatal(err)
				}
				select {
				case event := <-events:
					t.Fatalf("replayed send emitted duplicate event: %+v", event)
				default:
				}
			})
		}
	}
}

type mediaSendFailStore struct {
	engine.Persister
	err error
}

func (s mediaSendFailStore) Apply(context.Context, engine.Batch) (engine.Applied, error) {
	return engine.Applied{}, s.err
}

func TestMediaSendRPCResultImmediateIngestionFailureRetainsReceipt(t *testing.T) {
	e, s := writeFixture(t)
	ctx := holdMediaSendManager(t, e)
	path := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(path, []byte("synthetic upload"), 0600); err != nil {
		t.Fatal(err)
	}
	ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
	key := engine.MessageKey{Account: "primary", Peer: ref, ID: 505}
	localErr := errors.New("synthetic media ingestion failure")
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.UploadSaveFilePartRequest:
			out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
		case *tg.MessagesSendMediaRequest:
			out.(*tg.UpdatesBox).Updates = &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateMessageID{ID: key.ID, RandomID: r.RandomID}, &tg.UpdateNewMessage{Message: &tg.Message{ID: key.ID, PeerID: mediaPeerClass(ref), Out: true, Date: 1000}, Pts: 2, PtsCount: 1}}}
			e.cfg.Store = mediaSendFailStore{Persister: s, err: localErr}
		default:
			t.Fatalf("forbidden RPC %T", in)
		}
		return nil
	}))
	r := engine.SendMediaRequest{Peer: ref, Kind: engine.MediaDocument, Path: path, WriteOptions: engine.WriteOptions{DryRun: true}}
	preview, err := e.SendMedia(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	r.ExpectFingerprint = preview.Preview.Fingerprint
	result, err := e.SendMedia(ctx, r)
	if !errors.Is(err, localErr) || !result.Done || result.Affected != 1 || len(result.Messages) != 1 || result.Messages[0] != key {
		t.Fatalf("lost local failure or accepted receipt: result=%+v err=%v", result, err)
	}
	if !strings.Contains(result.Note, "do not blindly retry") || !e.held.Load() {
		t.Fatalf("missing retry warning/checkpoint hold: result=%+v held=%v", result, e.held.Load())
	}
	if _, found, err := s.Message(ctx, key); err != nil || found {
		t.Fatalf("failed ingestion persisted a message: found=%v err=%v", found, err)
	}
}
