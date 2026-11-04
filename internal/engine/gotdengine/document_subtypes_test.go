package gotdengine

import (
	"context"
	"reflect"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestDocumentSubtypeVideoRemoteSearchRetainsResult(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		name := "global"
		if scoped {
			name = "peer"
		}
		t.Run(name, func(t *testing.T) {
			e, st := offlineEngine(t)
			ref := engine.PeerRef{Type: engine.PeerUser, ID: 99}
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				calls++
				var filter tg.MessagesFilterClass
				switch req := in.(type) {
				case *tg.MessagesSearchRequest:
					if !scoped || req.Q != "clip" || req.Limit != 3 {
						t.Fatalf("unexpected peer search: %+v", req)
					}
					filter = req.Filter
				case *tg.MessagesSearchGlobalRequest:
					if scoped || req.Q != "clip" || req.Limit != 3 {
						t.Fatalf("unexpected global search: %+v", req)
					}
					filter = req.Filter
				default:
					t.Fatalf("unexpected RPC %T", in)
				}
				if _, ok := filter.(*tg.InputMessagesFilterVideo); !ok {
					t.Fatalf("video request used %T", filter)
				}
				out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{
					&tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "clip", Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 4, MimeType: "video/mp4", Attributes: []tg.DocumentAttributeClass{
						&tg.DocumentAttributeFilename{FileName: "clip.mp4"}, &tg.DocumentAttributeVideo{Duration: 4.5, W: 640, H: 360},
					}}}},
				}}
				return nil
			}))
			req := engine.SearchRequest{Query: "clip", Media: engine.MediaVideo, Limit: 3}
			if scoped {
				req.Peer = &ref
			}
			got, err := e.SearchRemote(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || got.Partial || len(got.Messages) != 1 {
				t.Fatalf("filtered video result discarded: calls=%d result=%+v", calls, got)
			}
			assertDocumentSubtype(t, got.Messages[0], engine.MediaVideo, "clip.mp4")
			stored, ok, err := st.Message(context.Background(), engine.MessageKey{Account: "primary", Peer: ref, ID: 7})
			if err != nil || !ok {
				t.Fatalf("read retained document: ok=%t err=%v", ok, err)
			}
			assertDocumentSubtype(t, stored, engine.MediaVideo, "clip.mp4")
		})
	}
}

func TestDocumentSubtypeRemoteSearchAllKinds(t *testing.T) {
	cases := []struct {
		kind   engine.MediaType
		attrs  []tg.DocumentAttributeClass
		filter tg.MessagesFilterClass
	}{
		{engine.MediaDocument, nil, &tg.InputMessagesFilterDocument{}},
		{engine.MediaVideo, []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{}}, &tg.InputMessagesFilterVideo{}},
		{engine.MediaVideoNote, []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{RoundMessage: true}}, &tg.InputMessagesFilterRoundVideo{}},
		{engine.MediaAudio, []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{}}, &tg.InputMessagesFilterMusic{}},
		{engine.MediaVoice, []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}}, &tg.InputMessagesFilterVoice{}},
		{engine.MediaAnimation, []tg.DocumentAttributeClass{&tg.DocumentAttributeAnimated{}, &tg.DocumentAttributeVideo{}}, &tg.InputMessagesFilterGif{}},
		{engine.MediaSticker, []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{}, &tg.DocumentAttributeVideo{}}, &tg.InputMessagesFilterEmpty{}},
	}
	for _, tc := range cases {
		for _, scoped := range []bool{false, true} {
			name := "global"
			if scoped {
				name = "peer"
			}
			t.Run(string(tc.kind)+"/"+name, func(t *testing.T) {
				e, st := offlineEngine(t)
				ref := engine.PeerRef{Type: engine.PeerUser, ID: 99}
				calls := 0
				e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
					calls++
					var filter tg.MessagesFilterClass
					switch req := in.(type) {
					case *tg.MessagesSearchRequest:
						if !scoped {
							t.Fatal("global search used peer RPC")
						}
						filter = req.Filter
					case *tg.MessagesSearchGlobalRequest:
						if scoped {
							t.Fatal("peer search used global RPC")
						}
						filter = req.Filter
					default:
						t.Fatalf("unexpected RPC %T", in)
					}
					if reflect.TypeOf(filter) != reflect.TypeOf(tc.filter) {
						t.Fatalf("want filter %T, got %T", tc.filter, filter)
					}
					attrs := append([]tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "original.filename"}}, tc.attrs...)
					rows := []tg.MessageClass{&tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "needle", Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 4, Attributes: attrs}}}}
					// Stickers have no native search filter: the local type filter
					// must retain the matching document but discard unrelated rows.
					if tc.kind == engine.MediaSticker {
						rows = append(rows, &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "needle"})
					}
					out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: rows}
					return nil
				}))
				req := engine.SearchRequest{Query: "needle", Media: tc.kind, Limit: 3}
				if scoped {
					req.Peer = &ref
				}
				got, err := e.SearchRemote(context.Background(), req)
				if err != nil || calls != 1 || len(got.Messages) != 1 || got.Partial != (tc.kind == engine.MediaSticker) {
					t.Fatalf("filter/result flow: calls=%d result=%+v err=%v", calls, got, err)
				}
				assertDocumentSubtype(t, got.Messages[0], tc.kind, "original.filename")
				stored, ok, err := st.Message(context.Background(), engine.MessageKey{Account: "primary", Peer: ref, ID: 7})
				if err != nil || !ok {
					t.Fatalf("document not retained: ok=%t err=%v", ok, err)
				}
				assertDocumentSubtype(t, stored, tc.kind, "original.filename")
			})
		}
	}
}

func TestDocumentSubtypeConversion(t *testing.T) {
	cases := []struct {
		name  string
		attrs []tg.DocumentAttributeClass
		want  engine.MediaType
	}{
		{"document", nil, engine.MediaDocument},
		{"unknown_attribute", []tg.DocumentAttributeClass{&tg.DocumentAttributeHasStickers{}}, engine.MediaDocument},
		{"video", []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{}}, engine.MediaVideo},
		{"round_video", []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{RoundMessage: true}}, engine.MediaVideoNote},
		{"audio", []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{}}, engine.MediaAudio},
		{"voice", []tg.DocumentAttributeClass{&tg.DocumentAttributeAudio{Voice: true}}, engine.MediaVoice},
		{"animation", []tg.DocumentAttributeClass{&tg.DocumentAttributeAnimated{}}, engine.MediaAnimation},
		{"animation_before_video", []tg.DocumentAttributeClass{&tg.DocumentAttributeAnimated{}, &tg.DocumentAttributeVideo{}}, engine.MediaAnimation},
		{"animation_after_video", []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{}, &tg.DocumentAttributeAnimated{}}, engine.MediaAnimation},
		{"sticker", []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{}}, engine.MediaSticker},
		{"video_sticker_first", []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{}, &tg.DocumentAttributeVideo{}}, engine.MediaSticker},
		{"video_sticker_last", []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{}, &tg.DocumentAttributeSticker{}}, engine.MediaSticker},
		{"animated_sticker_first", []tg.DocumentAttributeClass{&tg.DocumentAttributeSticker{}, &tg.DocumentAttributeAnimated{}}, engine.MediaSticker},
		{"animated_sticker_last", []tg.DocumentAttributeClass{&tg.DocumentAttributeAnimated{}, &tg.DocumentAttributeSticker{}}, engine.MediaSticker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, filenameFirst := range []bool{false, true} {
				attrs := append([]tg.DocumentAttributeClass(nil), tc.attrs...)
				filename := &tg.DocumentAttributeFilename{FileName: "original.filename"}
				if filenameFirst {
					attrs = append([]tg.DocumentAttributeClass{filename}, attrs...)
				} else {
					attrs = append(attrs, filename)
				}
				got, ok := convertMessage("primary", &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 4, Attributes: attrs}}})
				if !ok {
					t.Fatal("document rejected")
				}
				assertDocumentSubtype(t, got, tc.want, "original.filename")
			}
		})
	}
}

func TestDocumentSubtypeEphemeralPayloadRemainsRedacted(t *testing.T) {
	cases := []struct {
		kind engine.MediaType
		attr tg.DocumentAttributeClass
	}{
		{engine.MediaDocument, &tg.DocumentAttributeHasStickers{}},
		{engine.MediaVideo, &tg.DocumentAttributeVideo{}},
		{engine.MediaVideoNote, &tg.DocumentAttributeVideo{RoundMessage: true}},
		{engine.MediaAudio, &tg.DocumentAttributeAudio{}},
		{engine.MediaVoice, &tg.DocumentAttributeAudio{Voice: true}},
		{engine.MediaAnimation, &tg.DocumentAttributeAnimated{}},
		{engine.MediaSticker, &tg.DocumentAttributeSticker{}},
	}
	for _, tc := range cases {
		t.Run(string(tc.kind), func(t *testing.T) {
			got, ok := convertMessage("primary", &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Message: "private caption", Media: &tg.MessageMediaDocument{TTLSeconds: 10, Document: &tg.Document{ID: 4, Attributes: []tg.DocumentAttributeClass{tc.attr, &tg.DocumentAttributeFilename{FileName: "private.filename"}}}}})
			if !ok {
				t.Fatal("ephemeral placeholder rejected")
			}
			assertDocumentSubtype(t, got, tc.kind, "")
			if got.Text != "[ephemeral media]" || got.Media.TTLSeconds != 10 || got.Media.Downloadable || len(got.Media.Remote) != 0 {
				t.Fatalf("ephemeral payload/policy changed: %+v", got)
			}
		})
	}
}

func assertDocumentSubtype(t *testing.T, got engine.Message, kind engine.MediaType, filename string) {
	t.Helper()
	if got.Media == nil || got.Media.Type != kind || got.Media.Filename != filename {
		t.Fatalf("document subtype/filename lost: want=%s %q media=%+v", kind, filename, got.Media)
	}
}
