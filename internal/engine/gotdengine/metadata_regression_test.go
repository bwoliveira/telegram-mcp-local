package gotdengine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func metadataPoll() *tg.MessageMediaPoll {
	return &tg.MessageMediaPoll{Poll: tg.Poll{ID: 501, Question: tg.TextWithEntities{Text: "Synthetic question"}, Answers: []tg.PollAnswerClass{&tg.PollAnswer{Text: tg.TextWithEntities{Text: "Option A"}, Option: []byte{0, 255}}}}, Results: tg.PollResults{TotalVoters: 2, Results: []tg.PollAnswerVoters{{Option: []byte{0, 255}, Voters: 2, Chosen: true}}}}
}

func TestMetadataConvertPollAndReactions(t *testing.T) {
	m, ok := convertMessage("primary", &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 10}, Date: 1000, Media: metadataPoll(), Reactions: tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 2}}}})
	if !ok || m.Media == nil || m.Media.Type != engine.MediaPoll || m.Poll == nil || m.Text != "Synthetic question" || len(m.Reactions) != 1 {
		t.Fatal("poll definition, display text or reactions lost in conversion")
		return
	}
	if m.Poll.ID != 501 || m.Poll.TotalVoters != 2 || len(m.Poll.Options) != 1 || m.Poll.Options[0].Key != "AP8=" || !m.Poll.Options[0].Chosen {
		t.Fatal("poll options/results lost")
	}
}

func TestMetadataRichMediaTypes(t *testing.T) {
	for _, tc := range []struct {
		media tg.MessageMediaClass
		typ   engine.MediaType
	}{
		{&tg.MessageMediaGeo{Geo: &tg.GeoPoint{Lat: 1, Long: 2}}, engine.MediaGeo},
		{&tg.MessageMediaGeoLive{Geo: &tg.GeoPoint{Lat: 1, Long: 2}, Period: 60}, engine.MediaLiveGeo},
		{&tg.MessageMediaVenue{Geo: &tg.GeoPoint{Lat: 1, Long: 2}, Title: "Synthetic venue", Address: "Synthetic address"}, engine.MediaVenue},
		{&tg.MessageMediaContact{FirstName: "Synthetic", PhoneNumber: "000", UserID: 10}, engine.MediaContact},
		{&tg.MessageMediaDice{Emoticon: "🎲", Value: 4}, engine.MediaDice},
		{&tg.MessageMediaStory{Peer: &tg.PeerUser{UserID: 10}, ID: 9}, engine.MediaStory},
		{&tg.MessageMediaWebPage{Webpage: &tg.WebPage{URL: "https://example.invalid", Title: "Synthetic preview"}}, engine.MediaWebPage},
	} {
		t.Run(string(tc.typ), func(t *testing.T) {
			m, ok := convertMessage("primary", &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 10}, Media: tc.media})
			if !ok || m.Media == nil || m.Media.Type != tc.typ || m.Media.Downloadable || len(m.Media.Remote) != 0 {
				t.Fatal("rich media type or file safety lost")
			}
			if tc.typ == engine.MediaGeo || tc.typ == engine.MediaVenue || tc.typ == engine.MediaLiveGeo {
				if m.Location == nil || m.Location.Latitude != 1 || m.Location.Longitude != 2 {
					t.Fatal("geo coordinates lost")
				}
			}
		})
	}
}

func TestMetadataSQLiteUpdateIngestAndReplay(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(2000, 0)
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "metadata.db"), "primary", store.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Session: &memorySession{}, Store: db, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
	if err := db.BindSelf(ctx, "primary", e.account); err != nil {
		t.Fatal(err)
	}
	key := engine.MessageKey{Account: "primary", Peer: engine.PeerRef{Type: engine.PeerUser, ID: 10}, ID: 8}
	incoming := &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 10}, Date: 1000, Media: metadataPoll()}
	if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateNewMessage{Message: incoming}}); err != nil {
		t.Fatal(err)
	}
	if err := e.SetMode(ctx, engine.ModeReadWrite); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: key.Peer, Kind: engine.KindUser, FirstName: "Synthetic", Access: engine.AccessOK}, AccessHash: 1010}}}); err != nil {
		t.Fatal(err)
	}
	// Use the real gotd client Invoker path, without a Telegram connection.
	updates := &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: incoming.PeerID, MsgID: 8, Reactions: tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}}}}}
	calls := 0
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		calls++
		if _, ok := in.(*tg.MessagesSendReactionRequest); !ok {
			t.Fatalf("unexpected RPC %T", in)
		}
		out.(*tg.UpdatesBox).Updates = updates
		return nil
	}))
	r := engine.ReactRequest{Key: key, Emoji: "👍", WriteOptions: engine.WriteOptions{DryRun: true}}
	preview, err := e.React(ctx, r)
	if err != nil || preview.Preview == nil || calls != 0 {
		t.Fatal("reaction preview failed or issued RPC")
	}
	r.WriteOptions = engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint}
	result, err := e.React(ctx, r)
	if err != nil || !result.Done || calls != 1 {
		t.Fatal("confirmed reaction failed")
	}
	accepted, found, err := db.Message(ctx, key)
	if err != nil || !found || len(accepted.Reactions) != 1 {
		t.Fatal("accepted reaction did not reconcile into SQLite")
	}
	for i := 0; i < 2; i++ {
		if err := e.handle(ctx, updates); err != nil {
			t.Fatal(err)
		}
	}
	m, ok, err := db.Message(ctx, key)
	if err != nil || !ok || len(m.Reactions) != 1 || m.Poll == nil {
		t.Fatal("SQLite message read lost ingested reaction/poll")
		return
	}
	if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateMessagePoll{PollID: 501, Results: tg.PollResults{TotalVoters: 3, Results: []tg.PollAnswerVoters{{Option: []byte{0, 255}, Voters: 3}}}}}); err != nil {
		t.Fatal(err)
	}
	m, _, err = db.Message(ctx, key)
	if err != nil || m.Poll == nil || m.Poll.TotalVoters != 3 || m.Poll.Options[0].Voters != 3 {
		t.Fatal("poll result-only update not merged")
		return
	}
	if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: incoming.PeerID, MsgID: 8, Reactions: tg.MessageReactions{}}}); err != nil {
		t.Fatal(err)
	}
	m, _, err = db.Message(ctx, key)
	if err != nil || len(m.Reactions) != 0 {
		t.Fatal("reaction clear not persisted")
	}
}
