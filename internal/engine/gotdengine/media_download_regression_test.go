package gotdengine

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestMediaDownloadableConversionJPEGAndVoice(t *testing.T) {
	for _, voice := range []bool{false, true} {
		name := "jpeg"
		if voice {
			name = "voice"
		}
		t.Run(name, func(t *testing.T) {
			e, _ := offlineEngine(t)
			raw := mediaRawDocument(e)
			wantType, wantMIME := engine.MediaPhoto, "image/jpeg"
			if voice {
				d := raw.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
				d.MimeType = "audio/ogg"
				d.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}}
				wantType, wantMIME = engine.MediaVoice, "audio/ogg"
			} else {
				raw.Media = &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 41, AccessHash: 42, FileReference: []byte("synthetic-file-reference"), Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", Size: 5}}}}
			}
			for _, protected := range []bool{false, true} {
				raw.Noforwards = protected
				got, ok := convertMessage("primary", raw)
				if !ok || got.Media == nil || got.Media.Downloadable == protected {
					t.Fatalf("downloadable=%v for protected=%v", got.Media, protected)
				}
				if !protected && (got.Media.Size != 5 || got.Media.MimeType != wantMIME || got.Media.Type != wantType) {
					t.Fatalf("attachment metadata=%+v", got.Media)
				}
				if len(got.Media.Remote) != 0 {
					t.Fatal("file credentials persisted unnecessarily")
				}
				payload, err := json.Marshal(got)
				if err != nil || bytes.Contains(payload, []byte("synthetic-file-reference")) {
					t.Fatal("file reference escaped the transport")
				}
			}
		})
	}
}

func TestMediaDownloadChunkLocationsJPEGAndVoice(t *testing.T) {
	for _, voice := range []bool{false, true} {
		name := "jpeg"
		if voice {
			name = "voice"
		}
		t.Run(name, func(t *testing.T) {
			e, _ := offlineEngine(t)
			key := mediaTestMessage(t, e, engine.Message{})
			body := bytes.Repeat([]byte{0x5a}, (512<<10)+31)
			raw := mediaRawDocument(e)
			if voice {
				d := raw.Media.(*tg.MessageMediaDocument).Document.(*tg.Document)
				d.Size, d.MimeType = int64(len(body)), "audio/ogg"
				d.Attributes = []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}}
			} else {
				raw.Media = &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 41, AccessHash: 42, FileReference: []byte("fresh"), Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "s", Size: 12}, &tg.PhotoSizeProgressive{Type: "y", Sizes: []int{20, len(body)}}}}}
			}
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch r := in.(type) {
				case *tg.MessagesGetMessagesRequest:
					out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{raw}}
				case *tg.UploadGetFileRequest:
					if r.Offset != int64(calls)*(512<<10) || r.Limit != 512<<10 || r.CDNSupported {
						t.Fatalf("invalid chunk request offset=%d limit=%d cdn=%v", r.Offset, r.Limit, r.CDNSupported)
					}
					if voice {
						loc, ok := r.Location.(*tg.InputDocumentFileLocation)
						if !ok || loc.ID != 31 || loc.AccessHash != 32 || string(loc.FileReference) != "fresh" || loc.ThumbSize != "" {
							t.Fatal("wrong document location")
						}
					} else {
						loc, ok := r.Location.(*tg.InputPhotoFileLocation)
						if !ok || loc.ID != 41 || loc.AccessHash != 42 || string(loc.FileReference) != "fresh" || loc.ThumbSize != "y" {
							t.Fatal("wrong photo location")
						}
					}
					end := min(int(r.Offset)+r.Limit, len(body))
					out.(*tg.UploadFileBox).File = &tg.UploadFile{Type: &tg.StorageFileUnknown{}, Bytes: body[int(r.Offset):end]}
					calls++
				default:
					t.Fatalf("forbidden RPC %T", in)
				}
				return nil
			}))
			got, err := e.Download(context.Background(), engine.DownloadRequest{Key: key, Dir: t.TempDir(), MaxBytes: int64(len(body))})
			if err != nil {
				t.Fatal(err)
			}
			downloaded, err := os.ReadFile(got.Path)
			if err != nil || !bytes.Equal(downloaded, body) || calls != 2 {
				t.Fatalf("chunks=%d bytes=%d err=%v", calls, len(downloaded), err)
			}
		})
	}
}
