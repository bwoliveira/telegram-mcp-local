package gotdengine

import (
	"context"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

func TestDiscoveryChannelSignalsAreCoalescedWithoutPhantomMembership(t *testing.T) {
	for _, left := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "left"}[left], func(t *testing.T) {
			e, db := discoveryEngine(t)
			ctx := context.Background()
			raw := &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateChannel{ChannelID: 81}}}
			if left {
				raw.Chats = []tg.ChatClass{&tg.Channel{ID: 81, Title: "Left broadcast", Left: true, Broadcast: true}}
			}
			for n := 0; n < 2; n++ {
				if err := e.handle(ctx, raw); err != nil {
					t.Fatal(err)
				}
			}
			dialogs, err := db.ReadDialogs(ctx, "primary")
			if err != nil || len(dialogs) != 0 {
				t.Fatalf("metadata update invented membership: %+v %v", dialogs, err)
			}
			select {
			case <-e.dialogRefresh:
			default:
				t.Fatal("unknown channel did not queue authoritative discovery")
			}
			select {
			case <-e.dialogRefresh:
				t.Fatal("channel signals were not coalesced")
			default:
			}
		})
	}
}

func TestDiscoveryBroadcastUpdateCreatesMissingDialog(t *testing.T) {
	for _, source := range []string{"channel_update", "service_only", "service_with_peer", "message"} {
		t.Run(source, func(t *testing.T) {
			e, db := discoveryEngine(t)
			ctx := context.Background()
			raw := &tg.Updates{}
			switch source {
			case "channel_update":
				raw.Updates = []tg.UpdateClass{&tg.UpdateChannel{ChannelID: 81}}
			case "service_only", "service_with_peer":
				raw.Updates = []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: &tg.MessageService{ID: 1, PeerID: &tg.PeerChannel{ChannelID: 81}, Action: &tg.MessageActionChannelCreate{Title: "Synthetic broadcast"}}}}
			case "message":
				raw.Updates = []tg.UpdateClass{&tg.UpdateNewChannelMessage{Message: &tg.Message{ID: 1, PeerID: &tg.PeerChannel{ChannelID: 81}, Message: "synthetic", Date: 1000}}}
			}
			if source != "service_only" {
				raw.Chats = []tg.ChatClass{&tg.Channel{ID: 81, Title: "Synthetic broadcast", AccessHash: 99, Broadcast: true}}
			}
			ref := engine.PeerRef{Type: engine.PeerChannel, ID: 81}
			for n := 0; n < 2; n++ {
				if err := e.handle(ctx, raw); err != nil {
					t.Fatal(err)
				}
			}
			dialogs, err := db.ReadDialogs(ctx, "primary")
			if err != nil || len(dialogs) != 1 || dialogs[0].Peer != ref {
				t.Fatalf("broadcast missing durable dialog: %+v %v", dialogs, err)
			}
			p, ok, err := db.LookupPeer(ctx, "primary", ref)
			if err != nil || !ok || p.Kind != engine.KindChannel || p.Title != "Synthetic broadcast" {
				t.Fatalf("broadcast peer missing: %+v %v", p, err)
			}
			want := engine.Dialog{Peer: ref, TopMessageID: 9, UnreadCount: 4, Pinned: true, FolderID: 1, TTLPeriod: 60}
			if _, err := db.Apply(ctx, engine.Batch{Account: "primary", Dialogs: []engine.Dialog{want}}); err != nil {
				t.Fatal(err)
			}
			if err := e.handle(ctx, raw); err != nil {
				t.Fatal(err)
			}
			dialogs, err = db.ReadDialogs(ctx, "primary")
			if err != nil || len(dialogs) != 1 || dialogs[0] != want {
				t.Fatalf("replay reset broadcast state: %+v %v", dialogs, err)
			}
		})
	}
}
