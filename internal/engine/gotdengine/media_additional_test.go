package gotdengine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestMediaChannelPhotoSelectsLargestProgressiveSize(t *testing.T) {
	e, _ := offlineEngine(t)
	ctx := context.Background()
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerChannel, ID: 101}, ID: 7}
	_, err := e.cfg.Store.Apply(ctx, engine.Batch{Account: "primary", Source: engine.SourceUpdate, Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: key.Peer, Kind: engine.KindChannel}, AccessHash: 808}}, Messages: []engine.Message{{Key: key, Date: e.now(), Media: &engine.Media{Type: engine.MediaPhoto}}}})
	if err != nil {
		t.Fatal(err)
	}
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.ChannelsGetMessagesRequest:
			channel, ok := r.Channel.(*tg.InputChannel)
			if !ok || channel.ChannelID != 101 || channel.AccessHash != 808 || len(r.ID) != 1 || r.ID[0].(*tg.InputMessageID).ID != 7 {
				t.Fatalf("bad request %+v", r)
			}
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesChannelMessages{Messages: []tg.MessageClass{&tg.Message{ID: 7, PeerID: &tg.PeerChannel{ChannelID: 101}, Media: &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 41, AccessHash: 42, FileReference: []byte("photo-ref"), Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "s", Size: 2}, &tg.PhotoSizeProgressive{Type: "x", Sizes: []int{2, 5}}}}}}}}
		case *tg.UploadGetFileRequest:
			loc, ok := r.Location.(*tg.InputPhotoFileLocation)
			if !ok || loc.ID != 41 || loc.AccessHash != 42 || loc.ThumbSize != "x" || string(loc.FileReference) != "photo-ref" {
				t.Fatalf("wrong location %#v", r.Location)
			}
			out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: []byte("photo")}
		default:
			t.Fatalf("unexpected %T", in)
		}
		return nil
	}))
	got, err := e.Download(ctx, engine.DownloadRequest{Key: key, Dir: t.TempDir()})
	if err != nil || got.MediaType != engine.MediaPhoto || got.MimeType != "image/jpeg" || got.Bytes != 5 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestMediaExpiredReferenceDiscardsPartialBytesBeforeRestart(t *testing.T) {
	e, _ := offlineEngine(t)
	key := mediaTestMessage(t, e, engine.Message{})
	payload := bytes.Repeat([]byte("a"), (1<<20)+5)
	reads := 0
	oldPartial := false
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch r := in.(type) {
		case *tg.MessagesGetMessagesRequest:
			reads++
			raw := mediaRawDocument(e)
			raw.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).Size = int64(len(payload))
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{raw}}
		case *tg.UploadGetFileRequest:
			if reads == 1 && r.Offset > 0 {
				return tgerr.New(400, "FILE_REFERENCE_EXPIRED")
			}
			var chunk []byte
			if r.Offset < int64(len(payload)) {
				chunk = payload[r.Offset:min(int64(len(payload)), r.Offset+int64(r.Limit))]
			}
			if reads == 1 {
				chunk = bytes.Repeat([]byte("x"), len(chunk))
				oldPartial = true
			}
			out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: chunk}
		default:
			t.Fatalf("unexpected %T", in)
		}
		return nil
	}))
	dir := t.TempDir()
	got, err := e.Download(context.Background(), engine.DownloadRequest{Key: key, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(got.Path)
	files, _ := os.ReadDir(dir)
	if err != nil || !bytes.Equal(body, payload) || reads != 2 || !oldPartial || len(files) != 1 {
		t.Fatalf("restart: err=%v reads=%d files=%v bytes=%d", err, reads, files, len(body))
	}
}

func TestMediaDownloadCannotOutliveTransport(t *testing.T) {
	e, _ := offlineEngine(t)
	key := mediaTestMessage(t, e, engine.Message{})
	active, stop := context.WithCancel(context.Background())
	defer stop()
	e.active = active
	e.api = tg.NewClient(invokeFunc(func(ctx context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesGetMessagesRequest:
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{mediaRawDocument(e)}}
		case *tg.UploadGetFileRequest:
			stop()
			<-ctx.Done()
			return ctx.Err()
		default:
			t.Fatalf("unexpected %T", in)
		}
		return nil
	}))
	dir := t.TempDir()
	got, err := e.Download(context.Background(), engine.DownloadRequest{Key: key, Dir: dir})
	files, _ := os.ReadDir(dir)
	if !errors.Is(err, context.Canceled) || got.Path != "" || len(files) != 0 {
		t.Fatalf("%+v %v files=%v", got, err, files)
	}
}
