package gotdengine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
	"github.com/bwoliveira/telegram-mcp-local/internal/webhook"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestReviewRemoteHistoryCatchUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, _ := offlineEngine(t)
	events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 4, Kinds: []engine.EventKind{engine.EventMessage}})
	_, err := e.retainReadPage(ctx, &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "historical"}}}, engine.PeerRef{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if !ev.CatchUp {
			t.Fatalf("historical read emitted kind=%s source=%s catch_up=%v", ev.Kind, ev.Source, ev.CatchUp)
		}
	default:
		t.Fatal("missing event")
	}
}

func TestRemoteReadPathsCarryCatchUp(t *testing.T) {
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	for _, name := range []string{"peer_search", "global_search", "context", "backfill"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e, st := offlineEngine(t)
			events := e.Subscribe(ctx, engine.SubscribeOptions{Buffer: 4, Kinds: []engine.EventKind{engine.EventMessage, engine.EventEdit}})
			raw := &tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "historical"}
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				calls++
				switch name {
				case "peer_search":
					if _, ok := in.(*tg.MessagesSearchRequest); !ok {
						t.Fatalf("unexpected RPC %T", in)
					}
				case "global_search":
					if _, ok := in.(*tg.MessagesSearchGlobalRequest); !ok {
						t.Fatalf("unexpected RPC %T", in)
					}
				default:
					if _, ok := in.(*tg.MessagesGetHistoryRequest); !ok {
						t.Fatalf("unexpected RPC, including forbidden read receipt/write: %T", in)
					}
				}
				out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{raw}}
				return nil
			}))
			read := func() error {
				switch name {
				case "peer_search", "global_search":
					req := engine.SearchRequest{Query: "historical", Limit: 1}
					if name == "peer_search" {
						req.Peer = &peer
					}
					_, err := e.SearchRemote(ctx, req)
					return err
				case "context":
					_, err := e.FetchWindow(ctx, engine.WindowRequest{Key: engine.MessageKey{Account: "primary", Peer: peer, ID: 7}, Before: 1, After: 1})
					return err
				default:
					_, err := e.Backfill(ctx, engine.BackfillRequest{Peer: peer, Count: 1, Rounds: 1})
					return err
				}
			}
			wantSource := engine.SourceRefresh
			if name == "backfill" {
				wantSource = engine.SourceBackfill
			}
			if err := read(); err != nil {
				t.Fatal(err)
			}
			select {
			case ev := <-events:
				if ev.Kind != engine.EventMessage || ev.Source != wantSource || !ev.CatchUp || ev.Message == nil || ev.Message.Text != raw.Message {
					t.Fatalf("historical read metadata: %+v", ev)
				}
				if _, ok, err := st.Message(ctx, ev.Message.Key); err != nil || !ok {
					t.Fatalf("event preceded persistence: found=%v err=%v", ok, err)
				}
			default:
				t.Fatal("missing historical message event")
			}
			if calls != 1 {
				t.Fatalf("RPC calls=%d, want 1", calls)
			}
			// Re-reading retained search/context rows must not replay old facts.
			// Backfill has cursor-advance semantics, covered by its own tests.
			if name != "backfill" {
				if err := read(); err != nil {
					t.Fatal(err)
				}
				select {
				case ev := <-events:
					t.Fatalf("unchanged page re-emitted: %+v", ev)
				default:
				}
				raw.Message = "historical edit"
				raw.EditDate = 2000
				if err := read(); err != nil {
					t.Fatal(err)
				}
				select {
				case ev := <-events:
					if ev.Kind != engine.EventEdit || ev.Source != wantSource || !ev.CatchUp {
						t.Fatalf("historical edit metadata: %+v", ev)
					}
				default:
					t.Fatal("missing historical edit event")
				}
			}
		})
	}
}

func TestRemoteReadCatchUpReachesWebhook(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e, _ := offlineEngine(t)
	st, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	m := webhook.New(st, nil, "test", nil)
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	deliveries := make(chan webhook.Event, 4)
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev webhook.Event
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			t.Errorf("decode webhook: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		deliveries <- ev
		w.WriteHeader(http.StatusNoContent)
	}))
	defer receiver.Close()
	url := receiver.URL
	allowPrivate := true
	if _, _, err := m.Create(ctx, webhook.Settings{URL: &url, AllowPrivateNetwork: &allowPrivate}); err != nil {
		t.Fatal(err)
	}
	m.BindEngine(ctx, e)
	peer := engine.PeerRef{Type: engine.PeerUser, ID: 99}
	page := &tg.MessagesMessages{Messages: []tg.MessageClass{&tg.Message{ID: 7, PeerID: &tg.PeerUser{UserID: 99}, Date: 1000, Message: "historical"}}}
	if _, err := e.retainReadPage(ctx, page, peer); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-deliveries:
		if ev.Kind != "message" || ev.Source != engine.SourceRefresh || !ev.CatchUp || ev.Message == nil || ev.Message.MessageID != 7 || ev.Message.Text != "historical" {
			t.Fatalf("historical webhook projection: %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing historical webhook")
	}
	// Live updates must remain distinguishable from recovered history.
	if err := e.handle(ctx, &tg.Updates{Seq: 1, Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Pts: 1, PtsCount: 1, Message: &tg.Message{ID: 8, PeerID: &tg.PeerUser{UserID: 99}, Date: 3000, Message: "live"}}}}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-deliveries:
		if ev.Kind != "message" || ev.Source != engine.SourceUpdate || ev.CatchUp || ev.Message == nil || ev.Message.MessageID != 8 {
			t.Fatalf("live webhook projection: %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing live webhook")
	}
}
