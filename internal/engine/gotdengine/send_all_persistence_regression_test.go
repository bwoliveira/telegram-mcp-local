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
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func sendAllFixture(t *testing.T) (*Engine, *store.Store, context.Context) {
	t.Helper()
	e, db := groupRefreshStoreEngine(t)
	ctx := holdMediaSendManager(t, e)
	if err := e.SetMode(ctx, engine.ModeReadWrite); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerUser, ID: 10}, Kind: engine.KindUser}, AccessHash: 1010},
		{Peer: engine.Peer{Ref: engine.PeerRef{Type: engine.PeerChannel, ID: 40}, Kind: engine.KindChannel}, AccessHash: 4040},
	}, Messages: []engine.Message{{Key: engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 71}, Text: "synthetic source", Date: time.Unix(1000, 0)}}}); err != nil {
		t.Fatal(err)
	}
	return e, db, ctx
}

func sendAllCall(e *Engine, ctx context.Context, kind string, ref engine.PeerRef, path string, opts engine.WriteOptions) (engine.WriteResult, error) {
	switch kind {
	case "text":
		return e.SendText(ctx, engine.SendTextRequest{Peer: ref, Text: "synthetic sent text", WriteOptions: opts})
	case "poll":
		return e.SendPoll(ctx, engine.SendPollRequest{Peer: ref, Question: "Pick?", Options: []string{"one", "two"}, WriteOptions: opts})
	case "location":
		return e.SendLocation(ctx, engine.SendLocationRequest{Peer: ref, Location: engine.Location{Latitude: 1, Longitude: 2}, WriteOptions: opts})
	case "forward":
		return e.Forward(ctx, engine.ForwardRequest{From: engine.PeerRef{Type: engine.PeerUser, ID: 10}, To: ref, IDs: []int{71}, WriteOptions: opts})
	default:
		return e.SendMedia(ctx, engine.SendMediaRequest{Peer: ref, Kind: engine.MediaType(kind), Path: path, Caption: "synthetic caption", WriteOptions: opts})
	}
}

func sendAllRaw(kind string, ref engine.PeerRef, id int) *tg.Message {
	raw := &tg.Message{ID: id, PeerID: mediaPeerClass(ref), FromID: &tg.PeerUser{UserID: 99}, Out: true, Date: 1000, Message: "synthetic sent text"}
	if ref.Type == engine.PeerChannel {
		// Broadcast posts need no from_id, and carry presentation-only metadata.
		raw.FromID = nil
		raw.Post = true
		raw.SetViews(7)
		raw.SetPostAuthor("Synthetic author")
	}
	switch kind {
	case "poll":
		raw.Message = ""
		raw.Media = &tg.MessageMediaPoll{Poll: tg.Poll{ID: 81, Question: tg.TextWithEntities{Text: "Pick?"}, Answers: []tg.PollAnswerClass{&tg.PollAnswer{Text: tg.TextWithEntities{Text: "one"}, Option: []byte{0}}, &tg.PollAnswer{Text: tg.TextWithEntities{Text: "two"}, Option: []byte{1}}}}}
	case "location":
		raw.Message = ""
		raw.Media = &tg.MessageMediaGeo{Geo: &tg.GeoPoint{Lat: 1, Long: 2}}
	case "forward":
		raw.Message = "synthetic source"
		raw.SetFwdFrom(tg.MessageFwdHeader{Date: 1000, FromID: &tg.PeerUser{UserID: 10}})
	case "photo":
		raw.Message = "synthetic caption"
		raw.Media = &tg.MessageMediaPhoto{Photo: &tg.Photo{ID: 82}}
	case "text":
	default:
		attrs := []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "synthetic.bin"}}
		switch kind {
		case "video":
			attrs = append(attrs, &tg.DocumentAttributeVideo{})
		case "audio":
			attrs = append(attrs, &tg.DocumentAttributeAudio{})
		case "voice":
			attrs = append(attrs, &tg.DocumentAttributeAudio{Voice: true})
		case "animation":
			attrs = append(attrs, &tg.DocumentAttributeAnimated{})
		case "sticker":
			attrs = append(attrs, &tg.DocumentAttributeSticker{Stickerset: &tg.InputStickerSetEmpty{}})
		}
		raw.Message = "synthetic caption"
		raw.Media = &tg.MessageMediaDocument{Document: &tg.Document{ID: 83, Size: 16, MimeType: "application/octet-stream", Attributes: attrs}}
	}
	return raw
}

func sendAllRPC(t *testing.T, e *Engine, db *store.Store, kind, envelope string, ref engine.PeerRef, fail error) (*int, *tg.UpdatesClass) {
	t.Helper()
	calls := new(int)
	var returned tg.UpdatesClass
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		var ids []int64
		switch r := in.(type) {
		case *tg.UploadSaveFilePartRequest:
			out.(*tg.BoolBox).Bool = &tg.BoolTrue{}
			return nil
		case *tg.MessagesSendMessageRequest:
			ids = []int64{r.RandomID}
		case *tg.MessagesSendMediaRequest:
			ids = []int64{r.RandomID}
		case *tg.MessagesForwardMessagesRequest:
			ids = r.RandomID
		default:
			t.Fatalf("forbidden RPC %T", in)
		}
		*calls++
		raw := sendAllRaw(kind, ref, 504)
		var update tg.UpdateClass = &tg.UpdateNewMessage{Message: raw, Pts: 2, PtsCount: 1}
		if ref.Type == engine.PeerChannel {
			update = &tg.UpdateNewChannelMessage{Message: raw, Pts: 2, PtsCount: 1}
		}
		updates := []tg.UpdateClass{&tg.UpdateMessageID{ID: raw.ID, RandomID: ids[0]}, update}
		switch envelope {
		case "updates":
			returned = &tg.Updates{Updates: updates, Date: 1000, Seq: 1}
		case "combined":
			returned = &tg.UpdatesCombined{Updates: updates, Date: 1000, SeqStart: 1, Seq: 1}
		case "short":
			returned = &tg.UpdateShort{Update: update, Date: 1000}
		case "short_sent":
			returned = &tg.UpdateShortSentMessage{ID: raw.ID, Date: 1000, Out: true, Pts: 2, PtsCount: 1, Media: raw.Media}
		default:
			t.Fatal(envelope)
		}
		out.(*tg.UpdatesBox).Updates = returned
		if fail != nil {
			e.cfg.Store = mediaSendFailStore{Persister: db, err: fail}
		}
		return nil
	}))
	return calls, &returned
}

func sendAllUpload(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "synthetic.bin")
	if err := os.WriteFile(path, []byte("synthetic upload"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSendAllRPCResultImmediatelyActionable(t *testing.T) {
	for _, kind := range []string{"text", "forward", "poll", "location", "photo", "video", "audio", "voice", "document", "sticker", "animation"} {
		for _, envelope := range []string{"updates", "combined", "short", "short_sent"} {
			if kind == "forward" && envelope == "short_sent" {
				continue
			}
			for _, peer := range []string{"user", "broadcast"} {
				t.Run(kind+"/"+envelope+"/"+peer, func(t *testing.T) {
					e, db, ctx := sendAllFixture(t)
					ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
					if peer == "broadcast" {
						ref = engine.PeerRef{Type: engine.PeerChannel, ID: 40}
					}
					calls, returned := sendAllRPC(t, e, db, kind, envelope, ref, nil)
					path := sendAllUpload(t)
					preview, err := sendAllCall(e, ctx, kind, ref, path, engine.WriteOptions{DryRun: true})
					if err != nil || *calls != 0 {
						t.Fatalf("preview: %+v err=%v calls=%d", preview, err, *calls)
					}
					events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 10})
					result, err := sendAllCall(e, ctx, kind, ref, path, engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint})
					if err != nil || !result.Done || result.Affected != 1 || *calls != 1 {
						t.Fatalf("send: %+v err=%v calls=%d", result, err, *calls)
					}
					key := engine.MessageKey{Account: "primary", Peer: ref, ID: 504}
					if envelope != "short" && (len(result.Messages) != 1 || result.Messages[0] != key) {
						t.Fatalf("wrong receipt: %+v", result)
					}
					m, found, err := db.Message(ctx, key)
					if err != nil || !found || !m.Out {
						t.Errorf("send returned before persistence: found=%v message=%+v err=%v", found, m, err)
					}
					for _, action := range []string{"edit", "react", "delete"} {
						var got engine.WriteResult
						var err error
						opts := engine.WriteOptions{DryRun: true}
						switch action {
						case "edit":
							got, err = e.Edit(ctx, engine.EditRequest{Key: key, Text: "replacement", WriteOptions: opts})
						case "react":
							got, err = e.React(ctx, engine.ReactRequest{Key: key, Emoji: "👍", WriteOptions: opts})
						case "delete":
							got, err = e.Delete(ctx, engine.DeleteRequest{Peer: ref, IDs: []int{key.ID}, Revoke: peer == "broadcast", WriteOptions: opts})
						}
						if err != nil || !got.DryRun || got.Preview == nil || got.Preview.Quoted == nil || got.Preview.Quoted.Key != key {
							t.Errorf("immediate %s preview: %+v err=%v", action, got, err)
						}
					}
					if *calls != 1 {
						t.Fatalf("previews issued RPC: %d", *calls)
					}
					if !found {
						return
					}
					if peer == "broadcast" && envelope != "short_sent" && (!m.Post || m.Sender != nil || m.Views != 7 || m.PostAuthor != "Synthetic author") {
						t.Fatalf("broadcast metadata lost: %+v", m)
					}
					switch kind {
					case "poll":
						if m.Poll == nil || m.Poll.Question != "Pick?" {
							t.Fatalf("poll lost: %+v", m)
						}
					case "location":
						if m.Location == nil || m.Location.Latitude != 1 || m.Location.Longitude != 2 {
							t.Fatalf("location lost: %+v", m)
						}
					case "text", "forward":
						if m.Text == "" {
							t.Fatalf("text lost: %+v", m)
						}
					default:
						if m.Media == nil || string(m.Media.Type) != kind {
							t.Fatalf("media lost: %+v", m)
						}
					}
					select {
					case event := <-events:
						if event.Kind != engine.EventMessage || event.Message == nil || event.Message.Key != key {
							t.Fatalf("wrong event: %+v", event)
						}
					default:
						t.Fatal("missing committed event")
					}
					// The held manager has processed nothing; an eventual replay is idempotent.
					if envelope != "short_sent" {
						if err := e.handle(ctx, *returned); err != nil {
							t.Fatal(err)
						}
						select {
						case event := <-events:
							t.Fatalf("replay emitted duplicate: %+v", event)
						default:
						}
					}
				})
			}
		}
	}
}

func TestSendAllRPCIngestionFailureRetainsReceipt(t *testing.T) {
	for _, kind := range []string{"text", "forward", "poll", "location", "photo", "video", "audio", "voice", "document", "sticker", "animation"} {
		t.Run(kind, func(t *testing.T) {
			e, db, ctx := sendAllFixture(t)
			ref := engine.PeerRef{Type: engine.PeerChannel, ID: 40}
			localErr := errors.New("synthetic synchronous send ingestion failure")
			calls, _ := sendAllRPC(t, e, db, kind, "updates", ref, localErr)
			path := sendAllUpload(t)
			preview, err := sendAllCall(e, ctx, kind, ref, path, engine.WriteOptions{DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := sendAllCall(e, ctx, kind, ref, path, engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint})
			key := engine.MessageKey{Account: "primary", Peer: ref, ID: 504}
			if !errors.Is(err, localErr) || !result.Done || result.Affected != 1 || len(result.Messages) != 1 || result.Messages[0] != key || *calls != 1 {
				t.Fatalf("lost acceptance or failure: result=%+v err=%v calls=%d", result, err, *calls)
			}
			if !strings.Contains(result.Note, "do not blindly retry") || !e.held.Load() {
				t.Fatalf("missing warning/checkpoint hold: %+v held=%v", result, e.held.Load())
			}
			if _, found, err := db.Message(ctx, key); err != nil || found {
				t.Fatalf("failed ingestion persisted: found=%v err=%v", found, err)
			}
		})
	}
}
