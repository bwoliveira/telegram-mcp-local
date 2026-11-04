package gotdengine

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestMediaAllUploadKindsAndImmutableSnapshot(t *testing.T) {
	for _, kind := range []engine.MediaType{engine.MediaPhoto, engine.MediaVideo, engine.MediaAudio, engine.MediaVoice, engine.MediaDocument, engine.MediaSticker, engine.MediaAnimation} {
		t.Run(string(kind), func(t *testing.T) {
			e, _ := writeFixture(t)
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "upload.bin")
			data := make([]byte, 600000)
			for i := range data {
				data[i] = 'a'
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			uploads := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch r := in.(type) {
				case *tg.UploadSaveFilePartRequest:
					uploads++
					for _, b := range r.Bytes {
						if b != 'a' {
							t.Fatal("upload read mutable source")
						}
					}
					if uploads == 1 {
						if err := os.WriteFile(path, []byte("changed after authorization"), 0600); err != nil {
							t.Fatal(err)
						}
					}
					out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
				case *tg.MessagesSendMediaRequest:
					if photo, ok := r.Media.(*tg.InputMediaUploadedPhoto); kind == engine.MediaPhoto {
						if !ok || !photo.Spoiler {
							t.Fatalf("photo %T", r.Media)
						}
					} else {
						d, ok := r.Media.(*tg.InputMediaUploadedDocument)
						if !ok {
							t.Fatalf("document %T", r.Media)
						}
						found := kind == engine.MediaDocument
						for _, a := range d.Attributes {
							switch a := a.(type) {
							case *tg.DocumentAttributeVideo:
								found = kind == engine.MediaVideo
							case *tg.DocumentAttributeAnimated:
								found = kind == engine.MediaAnimation
							case *tg.DocumentAttributeAudio:
								found = (kind == engine.MediaVoice && a.Voice) || (kind == engine.MediaAudio && !a.Voice)
							case *tg.DocumentAttributeSticker:
								found = kind == engine.MediaSticker
							}
						}
						if !found {
							t.Fatalf("attributes %+v", d.Attributes)
						}
					}
					out.(*tg.UpdatesBox).Updates = &tg.UpdateShortSentMessage{ID: 700, Date: 1000}
				default:
					t.Fatalf("RPC %T", in)
				}
				return nil
			}))
			r := engine.SendMediaRequest{Peer: engine.PeerRef{Type: engine.PeerChat, ID: 20}, Kind: kind, Path: path, Spoiler: true, WriteOptions: engine.WriteOptions{DryRun: true}}
			p, err := e.SendMedia(ctx, r)
			if err != nil {
				t.Fatal(err)
			}
			r.DryRun = false
			r.ExpectFingerprint = p.Preview.Fingerprint
			if _, err = e.SendMedia(ctx, r); err != nil {
				t.Fatal(err)
			}
			if uploads < 2 {
				t.Fatalf("parts %d", uploads)
			}
		})
	}
}

func TestMediaChangedResolvedMentionAndReply(t *testing.T) {
	e, s := writeFixture(t)
	ctx := context.Background()
	ref := engine.PeerRef{Type: engine.PeerChannel, ID: 30}
	key := engine.MessageKey{Account: "primary", Peer: ref, ID: 70}
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Text: "original", TopicID: 60}}}); err != nil {
		t.Fatal(err)
	}
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("must not RPC"); return nil }))
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("payload"), 0600); err != nil {
		t.Fatal(err)
	}
	r := engine.SendMediaRequest{Peer: ref, Kind: engine.MediaDocument, Path: path, Caption: "😀 Ana", Mentions: []engine.Mention{{User: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Text: "Ana"}}, ReplyTo: 70, TopicID: 60, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.SendMedia(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Preview.Entities) != 1 || p.Preview.Entities[0].Offset != 3 || p.Preview.Quoted == nil {
		t.Fatalf("preview %+v", p.Preview)
	}
	editedAt := time.Now()
	if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Edits: []engine.Message{{Key: key, Text: "edited", TopicID: 60, EditDate: &editedAt}}}); err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	if _, err = e.SendMedia(ctx, r); !errors.Is(err, engine.ErrPreviewMismatch) {
		t.Fatalf("changed quote %v", err)
	}
}

func TestMediaModeSwitchStopsUpload(t *testing.T) {
	e, _ := writeFixture(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(path, make([]byte, 600000), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		if _, ok := in.(*tg.UploadSaveFilePartRequest); !ok {
			t.Fatalf("RPC %T", in)
		}
		out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
		return e.SetMode(ctx, engine.ModeReadOnly)
	}))
	r := engine.SendMediaRequest{Peer: engine.PeerRef{Type: engine.PeerChat, ID: 20}, Kind: engine.MediaDocument, Path: path, WriteOptions: engine.WriteOptions{DryRun: true}}
	p, err := e.SendMedia(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	r.DryRun = false
	r.ExpectFingerprint = p.Preview.Fingerprint
	if _, err = e.SendMedia(ctx, r); !errors.Is(err, engine.ErrReadOnly) {
		t.Fatalf("mode switch %v", err)
	}
	if calls != 1 {
		t.Fatalf("RPCs after read-only: %d", calls)
	}
	if err = e.SetMode(ctx, engine.ModeReadWrite); err != nil {
		t.Fatal(err)
	}
	if _, err = e.SendMedia(ctx, r); !errors.Is(err, engine.ErrPreviewMismatch) {
		t.Fatalf("consumed approval %v", err)
	}
}

func TestMediaInvalidPayloadsNoRPC(t *testing.T) {
	e, _ := writeFixture(t)
	ctx := context.Background()
	ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error { t.Fatal("must not RPC"); return nil }))
	for _, loc := range []engine.Location{{Latitude: math.NaN()}, {Longitude: math.Inf(1)}, {Latitude: 91}, {Longitude: -181}, {AccuracyMeters: -1}, {LivePeriod: 123}} {
		if _, err := e.SendLocation(ctx, engine.SendLocationRequest{Peer: ref, Location: loc, WriteOptions: engine.WriteOptions{DryRun: true}}); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("location %+v: %v", loc, err)
		}
	}
	for _, options := range [][]string{{"one"}, {"same", "same"}, {"", "b"}, {"a", " "}} {
		if _, err := e.SendPoll(ctx, engine.SendPollRequest{Peer: ref, Question: "question", Options: options, WriteOptions: engine.WriteOptions{DryRun: true}}); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("options %+v: %v", options, err)
		}
	}
}
