package gotdengine

import (
	"context"
	"errors"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestMigrationAccessLossCommitsBeforeNextCandidate(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "rollback"}[fail], func(t *testing.T) {
			path := metadataAccessPaths()[0]
			armed := false
			diskErr := errors.New("synthetic migration lifecycle failure")
			e, s := seedMetadataAccess(t, path, func(stage string) error {
				if armed && fail && stage == "apply_before_commit" {
					return diskErr
				}
				return nil
			})
			ctx := context.Background()
			successor := engine.PeerRef{Type: engine.PeerChannel, ID: 81}
			channel := &tg.Channel{ID: successor.ID, Megagroup: true, AccessHash: 99}
			if _, err := s.Apply(ctx, engine.Batch{Account: "primary", Peers: peerUpserts(nil, []tg.ChatClass{channel}), Dialogs: []engine.Dialog{{Peer: successor}}}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				req, ok := in.(*tg.ChannelsGetFullChannelRequest)
				if !ok {
					t.Fatalf("unexpected migration RPC: %T", in)
				}
				calls++
				switch id := req.Channel.(*tg.InputChannel).ChannelID; id {
				case 80:
					return tgerr.New(400, "CHANNEL_PRIVATE")
				case 81:
					if fail {
						t.Fatal("discovery advanced after failed access-loss commit")
					}
					// The later RPC cannot race ahead of durable lifecycle reconciliation.
					assertMetadataAccessState(t, s, path.peer, true)
					*out.(*tg.MessagesChatFull) = tg.MessagesChatFull{FullChat: &tg.ChannelFull{ID: 81, MigratedFromChatID: 71}, Chats: []tg.ChatClass{channel}}
					return nil
				default:
					t.Fatalf("unexpected migration candidate: %d", id)
					return engine.ErrInvalid
				}
			}))
			armed = true
			err := e.discoverLegacyMigrations(ctx, nil)
			if fail {
				if !errors.Is(err, diskErr) || calls != 1 {
					t.Fatalf("failed lifecycle did not stop discovery: err=%v calls=%d", err, calls)
				}
				assertMetadataAccessState(t, s, path.peer, false)
			} else {
				if err != nil || calls != 2 {
					t.Fatalf("definitive loss starved later candidate: err=%v calls=%d", err, calls)
				}
				old, ok, err := s.LookupPeer(ctx, "primary", engine.PeerRef{Type: engine.PeerChat, ID: 71})
				if err != nil || !ok || old.MigratedTo == nil || *old.MigratedTo != successor {
					t.Fatalf("later candidate migration missing: %+v exists=%v err=%v", old, ok, err)
				}
			}
		})
	}
}
