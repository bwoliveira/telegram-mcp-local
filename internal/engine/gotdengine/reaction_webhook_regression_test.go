package gotdengine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/bwoliveira/telegram-mcp-local/internal/webhook"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestReactionOnlyEditUpdatesReachWebhook(t *testing.T) {
	for _, channel := range []bool{false, true} {
		for _, previousEdit := range []int{0, 1100} {
			for _, hidden := range []bool{true, false} {
				name := fmt.Sprintf("UpdateEditMessage/previous_edit_%d/hidden_%v", previousEdit, hidden)
				if channel {
					name = fmt.Sprintf("UpdateEditChannelMessage/previous_edit_%d/hidden_%v", previousEdit, hidden)
				}
				t.Run(name, func(t *testing.T) {
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					// Use the real persister's reaction diff, but only a fresh temporary
					// database. The PoC offlineEngine store does not implement this diff.
					db, err := store.Open(ctx, filepath.Join(t.TempDir(), "reactions.db"), "primary")
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = db.Close() })
					e, err := New(engine.Config{Account: "primary", APIID: 1, APIHash: "synthetic-test-only", Session: &memorySession{}, Store: db})
					if err != nil {
						t.Fatal(err)
					}
					e.account = engine.Account{ID: "primary", SelfID: 99, Authenticated: true}
					if err := db.BindSelf(ctx, "primary", e.account); err != nil {
						t.Fatal(err)
					}
					// Never start the Telegram client; reject any accidental RPC too.
					e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, _ bin.Decoder) error {
						return fmt.Errorf("unexpected RPC in offline webhook test: %T", in)
					}))
					var peer tg.PeerClass = &tg.PeerUser{UserID: 10}
					if channel {
						peer = &tg.PeerChannel{ChannelID: 20}
					}
					raw := &tg.Message{ID: 8, PeerID: peer, Date: 1000, Message: "Synthetic unchanged content"}
					if previousEdit != 0 {
						raw.SetEditDate(previousEdit)
					}
					var seed tg.UpdateClass = &tg.UpdateNewMessage{Message: raw}
					if channel {
						seed = &tg.UpdateNewChannelMessage{Message: raw}
					}
					if err := e.handle(ctx, &tg.UpdateShort{Update: seed}); err != nil {
						t.Fatal(err)
					}

					st, err := state.Open(t.TempDir())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = st.Close() })
					manager := webhook.New(st, nil, "test", nil)
					if err := manager.Start(ctx); err != nil {
						t.Fatal(err)
					}
					type delivery struct {
						body   []byte
						header http.Header
					}
					deliveries := make(chan delivery, 16)
					receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Errorf("read webhook: %v", err)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
						if r.Method != http.MethodPost {
							t.Errorf("webhook method = %s, want POST", r.Method)
						}
						select {
						case deliveries <- delivery{body: body, header: r.Header.Clone()}:
							w.WriteHeader(http.StatusNoContent)
						case <-r.Context().Done():
						}
					}))
					defer receiver.Close()
					url, events, enabled, includeOwn, allowPrivate := receiver.URL, []string{"reaction", "edit"}, true, false, true
					_, secret, err := manager.Create(ctx, webhook.Settings{URL: &url, Events: &events, Enabled: &enabled, IncludeOwn: &includeOwn, AllowPrivateNetwork: &allowPrivate})
					if err != nil {
						t.Fatal(err)
					}
					manager.BindEngine(ctx, e)

					edit := func(m *tg.Message) {
						t.Helper()
						var update tg.UpdateClass = &tg.UpdateEditMessage{Message: m}
						if channel {
							update = &tg.UpdateEditChannelMessage{Message: m}
						}
						if err := e.handle(ctx, &tg.UpdateShort{Update: update}); err != nil {
							t.Fatal(err)
						}
					}
					incoming := *raw
					incoming.EditHide = hidden
					incoming.SetEditDate(1200)
					reactions := tg.MessageReactions{Results: []tg.ReactionCount{{Reaction: &tg.ReactionEmoji{Emoticon: "👍"}, Count: 1}}}
					reactions.SetRecentReactions([]tg.MessagePeerReaction{{PeerID: &tg.PeerUser{UserID: 10}, Date: 1200, Reaction: &tg.ReactionEmoji{Emoticon: "👍"}}})
					incoming.SetReactions(reactions)
					edit(&incoming) // Add, delivered as a reaction rather than an edit.
					edit(&incoming) // Replaying the same snapshot must not deliver twice.
					incoming.SetEditDate(1250)
					incoming.SetReactions(tg.MessageReactions{}) // Message copies cannot remove known state.
					edit(&incoming)
					edit(&incoming)
					removal := &tg.UpdateShort{Update: &tg.UpdateMessageReactions{Peer: peer, MsgID: incoming.ID, Reactions: tg.MessageReactions{}}}
					if err := e.handle(ctx, removal); err != nil {
						t.Fatal(err)
					}
					if err := e.handle(ctx, removal); err != nil {
						t.Fatal(err)
					}

					// This real edit is an ordering barrier: the engine subscription and
					// webhook worker are FIFO, so its HTTP delivery drains every earlier
					// add/remove/replay. No sleep-based absence assertion is needed.
					incoming.EditHide = false
					incoming.SetEditDate(1300)
					incoming.Message = "Synthetic real edit barrier"
					edit(&incoming)
					var got []webhook.Event
					deadline := time.NewTimer(5 * time.Second)
					defer deadline.Stop()
					for {
						select {
						case d := <-deliveries:
							var event webhook.Event
							if err := json.Unmarshal(d.body, &event); err != nil {
								t.Fatalf("decode HTTP webhook: %v", err)
							}
							if d.header.Get("X-Telegram-MCP-Signature") != webhook.Sign(secret, d.body) || d.header.Get("X-Telegram-MCP-Event") != event.Kind || d.header.Get("X-Telegram-MCP-Delivery") != event.ID || d.header.Get("X-Telegram-MCP-Attempt") != "1" {
								t.Errorf("invalid signed webhook headers for %s", event.Kind)
							}
							if event.Source != engine.SourceUpdate || event.CatchUp || event.Test || event.Identity == nil || event.Identity.ID != "primary" || event.Chat == nil || event.Chat.ID != peerText(peerRef(peer)) {
								t.Errorf("unexpected webhook envelope: %+v", event)
							}
							got = append(got, event)
							if event.Kind == "edit" && event.Message != nil && event.Message.Text == incoming.Message {
								goto drained
							}
						case <-deadline.C:
							t.Fatalf("missing real edit HTTP barrier; received %d webhook deliveries: %+v", len(got), got)
						}
					}
				drained:
					kinds := make([]string, len(got))
					for i, event := range got {
						kinds[i] = event.Kind
					}
					if len(got) != 3 {
						t.Errorf("HTTP deliveries = %v, want exactly [reaction reaction edit] (add/remove/barrier, no replay or spurious edits)", kinds)
					}
					for i, event := range got[:len(got)-1] {
						if event.Kind != "reaction" || event.Reaction == nil || event.Message != nil {
							t.Errorf("reaction-only HTTP delivery %d = %s, want reaction, never edit", i, event.Kind)
							continue
						}
						r := event.Reaction
						if r.MessageID != raw.ID || r.ToID != raw.ID || r.By != "user:10" {
							t.Errorf("reaction target/author lost: %+v", r)
						}
						switch i {
						case 0:
							if len(r.Counts) != 1 || r.Counts[0].Emoji != "👍" || r.Counts[0].Count != 1 || len(r.Added) != 1 || len(r.Removed) != 0 || r.Emoji != "👍" {
								t.Errorf("reaction add payload lost: %+v", r)
							}
						case 1:
							if len(r.Counts) != 0 || len(r.Added) != 0 || len(r.Removed) != 1 || r.Removed[0].Emoji != "👍" || r.Emoji != "" {
								t.Errorf("reaction removal payload lost: %+v", r)
							}
						}
					}
					barrier := got[len(got)-1]
					if barrier.Message.MessageID != raw.ID || barrier.Message.EditDate == nil || barrier.Message.EditDate.Unix() != 1300 {
						t.Errorf("real edit webhook lost: %+v", barrier.Message)
					}
					t.Logf("HTTP deliveries: %v; signed add/remove followed by real edit barrier", kinds)
				})
			}
		}
	}
}
