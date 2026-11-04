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

func TestReviewLegacyCachedDocumentRemoteFilter(t *testing.T) {
	for _, scoped := range []bool{false, true} {
		name := "global"
		if scoped {
			name = "peer"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, err := store.Open(ctx, filepath.Join(t.TempDir(), "review.db"), "primary")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Session: &memorySession{}, Store: db})
			if err != nil {
				t.Fatal(err)
			}
			e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
			if err := db.BindSelf(ctx, "primary", e.account); err != nil {
				t.Fatal(err)
			}
			ref := engine.PeerRef{Type: engine.PeerUser, ID: 99}
			key := engine.MessageKey{Account: "primary", Peer: ref, ID: 7}
			// Exact output of the pre-fix converter for the raw video below.
			cached := engine.Message{Key: key, Date: time.Unix(1000, 0), Text: "clip", Media: &engine.Media{Type: engine.MediaDocument}}
			if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Source: engine.SourceUpdate, Messages: []engine.Message{cached}}); err != nil {
				t.Fatal(err)
			}
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				switch in.(type) {
				case *tg.MessagesSearchRequest, *tg.MessagesSearchGlobalRequest:
				default:
					t.Fatalf("unexpected RPC %T", in)
				}
				out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "clip", Media: &tg.MessageMediaDocument{Document: &tg.Document{ID: 4, Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeVideo{}}}}}}}
				return nil
			}))
			req := engine.SearchRequest{Query: "clip", Media: engine.MediaVideo, Limit: 3}
			if scoped {
				req.Peer = &ref
			}
			got, err := e.SearchRemote(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			current, _, err := db.Message(ctx, key)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Messages) != 1 || current.Media.Type != engine.MediaVideo {
				t.Fatalf("server returned video but remote filtered result has %d messages, partial=%v, retained type=%q", len(got.Messages), got.Partial, current.Media.Type)
			}
		})
	}
}
