package gotdengine

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestMessageActionsNativeRPC(t *testing.T) {
	for _, action := range []string{"edit", "react", "remove reaction", "delete", "delete channel", "forward", "typing", "record", "cancel", "archive", "unarchive", "pin", "unpin", "mute", "unmute"} {
		t.Run(action, func(t *testing.T) {
			e, s := writeFixture(t)
			ctx := context.Background()
			ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
			if action == "delete channel" {
				ref = engine.PeerRef{Type: engine.PeerChannel, ID: 30}
			}
			key := engine.MessageKey{Account: "primary", Peer: ref, ID: 71}
			_, err := s.Apply(ctx, engine.Batch{Account: "primary", Messages: []engine.Message{{Key: key, Text: "old", Date: time.Unix(1000, 0), Out: true}}})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
				calls++
				switch r := in.(type) {
				case *tg.MessagesEditMessageRequest:
					if action != "edit" || r.ID != 71 || r.Message != "new" {
						t.Fatalf("wrong edit %+v", r)
					}
				case *tg.MessagesSendReactionRequest:
					if r.MsgID != 71 || (action == "react" && (len(r.Reaction) != 1 || r.Reaction[0].(*tg.ReactionEmoji).Emoticon != "👍")) || (action == "remove reaction" && len(r.Reaction) != 0) {
						t.Fatalf("wrong reaction %+v", r)
					}
				case *tg.MessagesGetMessagesRequest:
					if (action != "react" && action != "remove reaction") || len(r.ID) != 1 || r.ID[0].(*tg.InputMessageID).ID != key.ID {
						t.Fatal("wrong reaction reconciliation request")
					}
					var reaction tg.ReactionClass
					if action == "react" {
						reaction = &tg.ReactionEmoji{Emoticon: "👍"}
					}
					out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{reactionReadFixture(key, "old", reaction)}}
					return nil
				case *tg.MessagesDeleteMessagesRequest:
					if action != "delete" || len(r.ID) != 1 || r.ID[0] != 71 || r.Revoke {
						t.Fatalf("wrong delete %+v", r)
					}
				case *tg.ChannelsDeleteMessagesRequest:
					if action != "delete channel" || r.Channel.(*tg.InputChannel).AccessHash != 3030 {
						t.Fatalf("wrong channel delete %+v", r)
					}
				case *tg.MessagesForwardMessagesRequest:
					if action != "forward" || len(r.RandomID) != 1 || !r.Silent || !r.DropAuthor || r.ToPeer.(*tg.InputPeerChannel).ChannelID != 30 || r.TopMsgID != 60 {
						t.Fatalf("wrong forward %+v", r)
					}
				case *tg.MessagesSetTypingRequest:
					want := map[string]string{"typing": "*tg.SendMessageTypingAction", "record": "*tg.SendMessageRecordAudioAction", "cancel": "*tg.SendMessageCancelAction"}[action]
					if fmt.Sprintf("%T", r.Action) != want {
						t.Fatalf("wrong typing %+v", r)
					}
				case *tg.FoldersEditPeerFoldersRequest:
					want := 0
					if action == "archive" {
						want = 1
					}
					if len(r.FolderPeers) != 1 || r.FolderPeers[0].FolderID != want {
						t.Fatalf("wrong folder %+v", r)
					}
				case *tg.MessagesToggleDialogPinRequest:
					if r.Pinned != (action == "pin") {
						t.Fatalf("wrong pin %+v", r)
					}
				case *tg.AccountUpdateNotifySettingsRequest:
					if (action == "mute" && r.Settings.MuteUntil != 2147483647) || (action == "unmute" && r.Settings.MuteUntil != 0) {
						t.Fatalf("wrong mute %+v", r)
					}
				default:
					t.Fatalf("unexpected RPC %T", in)
				}
				switch v := out.(type) {
				case *tg.UpdatesBox:
					v.Updates = &tg.Updates{}
				case *tg.BoolBox:
					v.Bool = &tg.BoolTrue{}
				}
				return nil
			}))
			run := func(opts engine.WriteOptions) (engine.WriteResult, error) {
				switch action {
				case "edit":
					return e.Edit(ctx, engine.EditRequest{Key: key, Text: "new", WriteOptions: opts})
				case "react", "remove reaction":
					emoji := "👍"
					if action == "remove reaction" {
						emoji = ""
					}
					return e.React(ctx, engine.ReactRequest{Key: key, Emoji: emoji, WriteOptions: opts})
				case "delete", "delete channel":
					return e.DeleteConfirmed(ctx, engine.DeleteRequest{Peer: ref, IDs: []int{71}, Revoke: action == "delete channel", WriteOptions: opts}, action == "delete channel")
				case "forward":
					return e.Forward(ctx, engine.ForwardRequest{From: ref, IDs: []int{71}, To: engine.PeerRef{Type: engine.PeerChannel, ID: 30}, TopicID: 60, DropAuthor: true, Silent: true, WriteOptions: opts})
				case "typing", "record", "cancel":
					a := engine.TypingText
					if action == "record" {
						a = engine.TypingAudio
					}
					if action == "cancel" {
						a = engine.TypingCancel
					}
					return e.SetTyping(ctx, engine.TypingRequest{Peer: ref, Action: a, WriteOptions: opts})
				default:
					return e.Organise(ctx, engine.OrganiseRequest{Peer: ref, Action: engine.OrganiseAction(action), WriteOptions: opts})
				}
			}
			p, err := run(engine.WriteOptions{DryRun: true})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 0 {
				t.Fatal("preview called RPC")
			}
			got, err := run(engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint})
			wantCalls := 1
			if action == "react" || action == "remove reaction" {
				wantCalls++ // Explicitly exercise reconciliation of the empty RPC response.
			}
			if err != nil || !got.Done || calls != wantCalls {
				t.Fatalf("got=%+v err=%v calls=%d", got, err, calls)
			}
			if _, err := run(engine.WriteOptions{ExpectFingerprint: p.Preview.Fingerprint}); !errors.Is(err, engine.ErrPreviewMismatch) && !errors.Is(err, engine.ErrMessageNotFound) {
				t.Fatalf("reused preview: %v", err)
			}
		})
	}
}
