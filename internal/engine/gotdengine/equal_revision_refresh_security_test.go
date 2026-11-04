package gotdengine

import (
	"context"
	"testing"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/bin"
	"github.com/gotd/td/tg"
)

func TestReviewEqualRevisionChangedAttachmentProtection(t *testing.T) {
	ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
	e, db, raw := actionSQLiteFixture(t, ref)
	ctx := holdMediaSendManager(t, e)

	initial := *raw
	initial.SetEditDate(1100)
	initial.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{
		ID: 401, AccessHash: 402, DCID: 2, FileReference: []byte("old-reference"),
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "old.txt"}},
	}})
	if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateEditMessage{Message: &initial}}); err != nil {
		t.Fatal(err)
	}
	key := engine.MessageKey{Account: "primary", Peer: ref, ID: initial.ID}

	fetched := initial
	fetched.Noforwards = true
	fetched.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{
		ID: 501, AccessHash: 502, DCID: 2, FileReference: []byte("new-reference"),
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "new.txt"}},
	}})
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesSendReactionRequest:
			out.(*tg.UpdatesBox).Updates = &tg.UpdateShort{Update: &tg.UpdateMessageReactions{
				Peer: initial.PeerID, MsgID: initial.ID, Reactions: tg.MessageReactions{Min: true},
			}}
		case *tg.MessagesGetMessagesRequest:
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&fetched}}
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))

	req := engine.ReactRequest{Key: key, WriteOptions: engine.WriteOptions{DryRun: true}}
	preview, err := e.React(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.WriteOptions = engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint}
	receipt, err := e.React(ctx, req)
	if err != nil || !receipt.Done {
		t.Fatalf("reaction receipt=%+v err=%v", receipt, err)
	}
	got, found, err := db.Message(ctx, key)
	if err != nil || !found {
		t.Fatalf("message found=%v err=%v", found, err)
	}
	if !got.Protected {
		t.Fatalf("authoritative full refresh lost newly reported noforwards: protected=%v media=%+v", got.Protected, got.Media)
	}
}

func TestReviewEqualRevisionChangedAttachmentMessageTTL(t *testing.T) {
	ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
	e, db, raw := actionSQLiteFixture(t, ref)
	ctx := holdMediaSendManager(t, e)

	initial := *raw
	initial.SetEditDate(1100)
	initial.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{
		ID: 401, AccessHash: 402, DCID: 2, FileReference: []byte("old-reference"),
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "old.txt"}},
	}})
	if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateEditMessage{Message: &initial}}); err != nil {
		t.Fatal(err)
	}
	key := engine.MessageKey{Account: "primary", Peer: ref, ID: initial.ID}

	fetched := initial
	fetched.TTLPeriod = 30
	fetched.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{
		ID: 501, AccessHash: 502, DCID: 2, FileReference: []byte("new-reference"),
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "new.txt"}},
	}})
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesSendReactionRequest:
			out.(*tg.UpdatesBox).Updates = &tg.UpdateShort{Update: &tg.UpdateMessageReactions{
				Peer: initial.PeerID, MsgID: initial.ID, Reactions: tg.MessageReactions{Min: true},
			}}
		case *tg.MessagesGetMessagesRequest:
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&fetched}}
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))

	req := engine.ReactRequest{Key: key, WriteOptions: engine.WriteOptions{DryRun: true}}
	preview, err := e.React(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.WriteOptions = engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint}
	receipt, err := e.React(ctx, req)
	if err != nil || !receipt.Done {
		t.Fatalf("reaction receipt=%+v err=%v", receipt, err)
	}
	_, found, err := db.Message(ctx, key)
	if err != nil || found {
		t.Fatalf("expired authoritative full refresh remains readable: found=%v err=%v", found, err)
	}
}

func TestReviewEqualRevisionChangedAttachmentMediaTTL(t *testing.T) {
	ref := engine.PeerRef{Type: engine.PeerUser, ID: 10}
	e, db, raw := actionSQLiteFixture(t, ref)
	ctx := holdMediaSendManager(t, e)

	initial := *raw
	initial.SetEditDate(1100)
	initial.SetMedia(&tg.MessageMediaDocument{Document: &tg.Document{
		ID: 401, AccessHash: 402, DCID: 2, FileReference: []byte("old-reference"),
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "old.txt"}},
	}})
	if err := e.handle(ctx, &tg.UpdateShort{Update: &tg.UpdateEditMessage{Message: &initial}}); err != nil {
		t.Fatal(err)
	}
	key := engine.MessageKey{Account: "primary", Peer: ref, ID: initial.ID}

	fetched := initial
	fetched.SetMedia(&tg.MessageMediaDocument{TTLSeconds: 30, Document: &tg.Document{
		ID: 501, AccessHash: 502, DCID: 2, FileReference: []byte("new-reference"),
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "new.txt"}},
	}})
	e.api = tg.NewClient(invokeFunc(func(_ context.Context, in bin.Encoder, out bin.Decoder) error {
		switch in.(type) {
		case *tg.MessagesSendReactionRequest:
			out.(*tg.UpdatesBox).Updates = &tg.UpdateShort{Update: &tg.UpdateMessageReactions{
				Peer: initial.PeerID, MsgID: initial.ID, Reactions: tg.MessageReactions{Min: true},
			}}
		case *tg.MessagesGetMessagesRequest:
			out.(*tg.MessagesMessagesBox).Messages = &tg.MessagesMessages{Messages: []tg.MessageClass{&fetched}}
		default:
			t.Fatalf("unexpected RPC %T", in)
		}
		return nil
	}))

	req := engine.ReactRequest{Key: key, WriteOptions: engine.WriteOptions{DryRun: true}}
	preview, err := e.React(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.WriteOptions = engine.WriteOptions{ExpectFingerprint: preview.Preview.Fingerprint}
	receipt, err := e.React(ctx, req)
	if err != nil || !receipt.Done {
		t.Fatalf("reaction receipt=%+v err=%v", receipt, err)
	}
	got, found, err := db.Message(ctx, key)
	if err != nil || !found {
		t.Fatalf("message found=%v err=%v", found, err)
	}
	if got.Media == nil || got.Media.TTLSeconds != 30 || got.Media.Downloadable {
		t.Fatalf("authoritative full refresh lost newly reported media TTL: media=%+v", got.Media)
	}
}
