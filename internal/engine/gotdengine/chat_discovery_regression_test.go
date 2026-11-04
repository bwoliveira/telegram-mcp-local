package gotdengine

import (
	"context"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"path/filepath"
	"testing"
	"time"
)

func discoveryEngine(t *testing.T) (*Engine, *store.Store) {
	t.Helper()
	e, _ := offlineEngine(t)
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "production.db"), "primary")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.BindSelf(context.Background(), "primary", e.account); err != nil {
		t.Fatal(err)
	}
	e.cfg.Store = db
	e.api = tg.NewClient(invokeFunc(func(context.Context, bin.Encoder, bin.Decoder) error {
		t.Fatal("chat discovery attempted RPC")
		return nil
	}))
	return e, db
}

func TestChatDiscoveryNewBasicGroupCreatesDialog(t *testing.T) {
	for _, withPeer := range []bool{false, true} {
		t.Run(map[bool]string{false: "service_only", true: "with_peer"}[withPeer], func(t *testing.T) {
			e, db := discoveryEngine(t)
			ctx := context.Background()
			ref := engine.PeerRef{Type: engine.PeerChat, ID: 71}
			raw := &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.MessageService{ID: 1, PeerID: &tg.PeerChat{ChatID: 71}, Date: 1000, Action: &tg.MessageActionChatCreate{Title: "Synthetic MCP teste"}}}}}
			if withPeer {
				raw.Chats = []tg.ChatClass{&tg.Chat{ID: 71, Title: "Synthetic MCP teste"}}
			}
			for i := 0; i < 2; i++ {
				if err := e.handle(ctx, raw); err != nil {
					t.Fatal(err)
				}
			}
			dialogs, err := db.ReadDialogs(ctx, "primary")
			if err != nil || len(dialogs) != 1 || dialogs[0].Peer != ref || dialogs[0].TopMessageID != 1 {
				t.Fatalf("new group has no durable dialog: %v %v", dialogs, err)
			}
			peers, err := e.Resolve(ctx, engine.ResolveRequest{Query: "MCP teste"})
			if err != nil || len(peers) != 1 || peers[0].Peer.Ref != ref {
				t.Fatalf("new group title is not resolvable: %v %v", peers, err)
			}
			// Replaying creation after a refresh must not reset server counters/state.
			mute := time.Unix(5000, 0)
			want := engine.Dialog{Peer: ref, TopMessageID: 9, ReadInboxMaxID: 5, UnreadCount: 4, Pinned: true, FolderID: 1, MuteUntil: &mute, TTLPeriod: 60}
			if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Dialogs: []engine.Dialog{want}}); err != nil {
				t.Fatal(err)
			}
			if err := e.handle(ctx, raw); err != nil {
				t.Fatal(err)
			}
			dialogs, err = db.ReadDialogs(ctx, "primary")
			if err != nil || len(dialogs) != 1 || dialogs[0].UnreadCount != 4 || !dialogs[0].Pinned || dialogs[0].FolderID != 1 || dialogs[0].TTLPeriod != 60 || dialogs[0].TopMessageID != 9 {
				t.Fatalf("creation replay reset refreshed dialog: %v %v", dialogs, err)
			}
		})
	}
}

func TestChatDiscoveryNewBasicMessageCreatesOnlyMissingDialog(t *testing.T) {
	e, db := discoveryEngine(t)
	raw := &tg.Updates{Chats: []tg.ChatClass{&tg.Chat{ID: 72, Title: "Synthetic fresh group"}}, Updates: []tg.UpdateClass{&tg.UpdateNewMessage{Message: &tg.Message{ID: 7, PeerID: &tg.PeerChat{ChatID: 72}, Date: 1000, Message: "synthetic"}}}}
	if err := e.handle(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	dialogs, err := db.ReadDialogs(context.Background(), "primary")
	if err != nil || len(dialogs) != 1 || dialogs[0].Peer.ID != 72 || dialogs[0].TopMessageID != 7 {
		t.Fatalf("new basic message lacks dialog: %v %v", dialogs, err)
	}
}

func TestChatDiscoveryGotdResolverAcceptsPunctuatedTitles(t *testing.T) {
	e, db := discoveryEngine(t)
	ctx := context.Background()
	ref := engine.PeerRef{Type: engine.PeerChat, ID: 73}
	if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Peers: []engine.PeerUpsert{{Peer: engine.Peer{Ref: ref, Kind: engine.KindGroup, Title: "Synthetic: MCP teste", Access: engine.AccessOK}}}}); err != nil {
		t.Fatal(err)
	}
	got, err := e.Resolve(ctx, engine.ResolveRequest{Query: "Synthetic: MCP teste"})
	if err != nil || len(got) != 1 || got[0].Peer.Ref != ref {
		t.Fatalf("punctuated peer title rejected: %v %v", got, err)
	}
}
