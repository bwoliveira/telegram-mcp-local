package gotdengine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/tgerr"
	"os"
	"path/filepath"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/service"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func mediaTestMessage(t *testing.T, e *Engine, m engine.Message) engine.MessageKey {
	t.Helper()
	m.Key = engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 99}, ID: 7}
	if m.Date.IsZero() {
		m.Date = e.now()
	}
	if m.Media == nil {
		m.Media = &engine.Media{Type: engine.MediaDocument}
	}
	if _, err := e.cfg.Store.Apply(context.Background(), engine.Batch{Account: "primary", Source: engine.SourceUpdate, Messages: []engine.Message{m}}); err != nil {
		t.Fatal(err)
	}
	return m.Key
}

func mediaRawDocument(e *Engine) *tg.Message {
	return &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: int(e.now().Unix()), Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 31, AccessHash: 32, FileReference: []byte("fresh"), Size: 5, MimeType: "text/plain", Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "../../outside.txt"}}}}}
}

func TestMediaDownloadWritesPrivateFileAndDTO(t *testing.T) {
	e, _ := offlineEngine(t)
	key := mediaTestMessage(t, e, engine.Message{})
	var calls int
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		switch r := in.(type) {
		case *tg.MessagesGetMessagesRequest:
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{mediaRawDocument(e)}}
		case *tg.UploadGetFileRequest:
			loc, ok := r.Location.(*tg.InputDocumentFileLocation)
			if !ok || loc.ID != 31 || string(loc.FileReference) != "fresh" {
				t.Fatalf("location: %#v", r.Location)
			}
			out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: []byte("hello")}
		default:
			t.Fatalf("forbidden RPC: %T", in)
		}
		return nil
	}))
	dir := filepath.Join(t.TempDir(), "media")
	got, err := e.Download(context.Background(), engine.DownloadRequest{Key: key, Dir: dir, MaxBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(got.Path)
	if err != nil || string(body) != "hello" {
		t.Fatalf("file=%q err=%v", body, err)
	}
	st, err := os.Stat(got.Path)
	if err != nil || service.CheckPrivateDataPath(got.Path, false) != nil {
		t.Fatalf("permissions: %v %v", st, err)
	}
	sum := sha256.Sum256([]byte("hello"))
	if got.Bytes != 5 || got.SHA256 != hex.EncodeToString(sum[:]) || got.MimeType != "text/plain" || got.Filename != "outside.txt" || got.MediaType != engine.MediaDocument || filepath.Dir(got.Path) != dir || calls != 2 {
		t.Fatalf("DTO=%+v calls=%d", got, calls)
	}
}

func TestMediaDownloadRefreshesExpiredReferenceExactlyOnce(t *testing.T) {
	for _, alwaysExpired := range []bool{false, true} {
		t.Run(fmt.Sprint(alwaysExpired), func(t *testing.T) {
			e, _ := offlineEngine(t)
			key := mediaTestMessage(t, e, engine.Message{})
			reads, downloads := 0, 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch r := in.(type) {
				case *tg.MessagesGetMessagesRequest:
					reads++
					raw := mediaRawDocument(e)
					raw.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).FileReference = []byte(fmt.Sprint(reads))
					out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{raw}}
				case *tg.UploadGetFileRequest:
					downloads++
					if string(r.Location.(*tg.InputDocumentFileLocation).FileReference) != fmt.Sprint(downloads) {
						t.Fatal("old reference reused")
					}
					if downloads == 1 || alwaysExpired {
						return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
					}
					out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: []byte("hello")}
				default:
					t.Fatalf("unexpected %T", in)
				}
				return nil
			}))
			dir := t.TempDir()
			got, err := e.Download(context.Background(), engine.DownloadRequest{Key: key, Dir: dir})
			if reads != 2 || downloads != 2 {
				t.Fatalf("reads=%d downloads=%d", reads, downloads)
			}
			files, _ := os.ReadDir(dir)
			if alwaysExpired {
				var rpc *engine.RPCError
				if !errors.As(err, &rpc) || rpc.Type != "FILE_REFERENCE_EXPIRED" || len(files) != 0 || got.Path != "" {
					t.Fatalf("%+v %v files=%v", got, err, files)
				}
			} else if err != nil || len(files) != 1 {
				t.Fatalf("%v files=%v", err, files)
			}
		})
	}
}

type mediaGuardPersister struct {
	engine.Persister
	message engine.Message
}

func (s mediaGuardPersister) Message(context.Context, engine.MessageKey) (engine.Message, bool, error) {
	return s.message, true, nil
}

func TestMediaGuardsApplyToDownloadAndNativeTranscription(t *testing.T) {
	cases := []string{"local_ttl", "local_protected", "peer_protected", "raw_ttl", "raw_protected", "raw_expired", "raw_auto_delete", "wrong_peer", "account", "paused", "revoked", "canceled"}
	for _, operation := range []string{"download", "transcribe"} {
		for _, name := range cases {
			t.Run(operation+"/"+name, func(t *testing.T) {
				e, _ := offlineEngine(t)
				local := engine.Message{Media: &engine.Media{Type: engine.MediaVoice}}
				want := engine.ErrProtectedContent
				if name == "local_ttl" {
					local.Media.TTLSeconds = 10
				}
				if name == "local_protected" {
					local.Protected = true
				}
				key := mediaTestMessage(t, e, local)
				// pocstore deliberately drops all protected rows; a production
				// persister may instead retain a content-free placeholder.
				if name == "local_ttl" || name == "local_protected" {
					local.Key = key
					e.cfg.Store = mediaGuardPersister{Persister: e.cfg.Store, message: local}
				}
				if name == "peer_protected" {
					_, err := e.cfg.Store.Apply(context.Background(), engine.Batch{Account: "primary", Source: engine.SourceUpdate, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: key.Peer, Kind: engine.KindSelf, Protected: true}}}})
					if err != nil {
						t.Fatal(err)
					}
				}
				if name == "account" {
					key.Account = "other"
					want = engine.ErrAccountMismatch
				}
				if name == "paused" {
					e.paused = true
					want = engine.ErrPaused
				}
				if name == "revoked" {
					e.revoked.Store(true)
					want = engine.ErrRevoked
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if name == "canceled" {
					cancel()
					want = context.Canceled
				}
				calls := 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					calls++
					if _, ok := in.(*tg.MessagesGetMessagesRequest); !ok {
						t.Fatalf("forbidden content RPC %T", in)
					}
					raw := mediaRawDocument(e)
					switch name {
					case "raw_ttl":
						raw.Media.(*tg.MessageMediaDocument).TTLSeconds = 1
					case "raw_protected":
						raw.Noforwards = true
					case "raw_expired":
						raw.Media.(*tg.MessageMediaDocument).Document = &tg.DocumentEmpty{}
					case "raw_auto_delete":
						raw.Date = int(e.now().Add(-time.Minute).Unix())
						raw.TTLPeriod = 1
					case "wrong_peer":
						raw.PeerID = &tg.PeerUser{UserID: 123}
					}
					out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{raw}}
					return nil
				}))
				if name == "wrong_peer" {
					want = engine.ErrMessageNotFound
				}
				dir := t.TempDir()
				var err error
				if operation == "download" {
					_, err = e.Download(ctx, engine.DownloadRequest{Key: key, Dir: dir})
				} else {
					_, err = e.TranscribeNative(ctx, key)
				}
				if !errors.Is(err, want) {
					t.Fatalf("got %v want %v", err, want)
				}
				files, _ := os.ReadDir(dir)
				if len(files) != 0 {
					t.Fatalf("content left: %v", files)
				}
				if (strings.HasPrefix(name, "local_") || name == "peer_protected" || name == "account" || name == "paused" || name == "revoked" || name == "canceled") && calls != 0 {
					t.Fatal("RPC before local guard")
				}
			})
		}
	}
}

func TestMediaDownloadFailuresLeaveNoPartialFile(t *testing.T) {
	for _, name := range []string{"declared_oversize", "actual_oversize", "incomplete", "rpc_failure", "deleted_during_download", "canceled_during_download"} {
		t.Run(name, func(t *testing.T) {
			e, _ := offlineEngine(t)
			key := mediaTestMessage(t, e, engine.Message{})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			downloads := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.MessagesGetMessagesRequest:
					raw := mediaRawDocument(e)
					if name == "actual_oversize" {
						raw.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).Size = 1
					}
					out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{raw}}
				case *tg.UploadGetFileRequest:
					downloads++
					if name == "rpc_failure" {
						return errors.New("do not expose private location")
					}
					body := []byte("hello")
					if name == "incomplete" {
						body = []byte("hi")
					}
					if name == "deleted_during_download" {
						_, err := e.cfg.Store.Apply(ctx, engine.Batch{Account: "primary", Source: engine.SourceUpdate, Deletes: []engine.Deletion{{Peer: &key.Peer, IDs: []int{key.ID}}}})
						if err != nil {
							t.Fatal(err)
						}
					}
					if name == "canceled_during_download" {
						cancel()
					}
					out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: body}
				default:
					t.Fatalf("unexpected %T", in)
				}
				return nil
			}))
			maxBytes := int64(10)
			if name == "declared_oversize" || name == "actual_oversize" {
				maxBytes = 3
			}
			dir := t.TempDir()
			got, err := e.Download(ctx, engine.DownloadRequest{Key: key, Dir: dir, MaxBytes: maxBytes})
			files, _ := os.ReadDir(dir)
			if err == nil || got.Path != "" || len(files) != 0 || strings.Contains(err.Error(), "private location") {
				t.Fatalf("got=%+v err=%v files=%v", got, err, files)
			}
			if name == "declared_oversize" && downloads != 0 {
				t.Fatal("oversize downloaded")
			}
		})
	}
}
