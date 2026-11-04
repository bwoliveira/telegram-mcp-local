package gotdengine

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

func (e *Engine) SetTyping(ctx context.Context, r engine.TypingRequest) (engine.WriteResult, error) {
	var zero engine.WriteResult
	if err := e.messageActionGate(ctx, r.WriteOptions); err != nil {
		return zero, err
	}
	var action tg.SendMessageActionClass
	switch r.Action {
	case engine.TypingText:
		action = &tg.SendMessageTypingAction{}
	case engine.TypingAudio:
		action = &tg.SendMessageRecordAudioAction{}
	case engine.TypingCancel:
		action = &tg.SendMessageCancelAction{}
	default:
		return zero, engine.ErrInvalid
	}
	target, ip, err := e.writePeer(ctx, r.Peer)
	if err != nil {
		return zero, err
	}
	if !validWriteMessageID(r.TopicID) || (r.TopicID > 0 && (r.Peer.Type != engine.PeerChannel || !target.Forum)) {
		return zero, engine.ErrInvalid
	}
	p := &engine.Preview{Action: "typing", Target: target, Text: string(r.Action), Reversible: true}
	dry, err := e.messageActionPreview(ctx, r.WriteOptions, p, ip, struct {
		Action engine.TypingAction
		Topic  int
	}{r.Action, r.TopicID})
	if err != nil {
		return zero, err
	}
	if dry {
		return engine.WriteResult{DryRun: true, Preview: p}, nil
	}
	req := &tg.MessagesSetTypingRequest{Peer: ip, Action: action}
	if r.TopicID > 0 {
		req.SetTopMsgID(r.TopicID)
	}
	accepted, err := e.api.MessagesSetTyping(ctx, req)
	if err != nil {
		return zero, rpcError(err)
	}
	if !accepted {
		return zero, engine.ErrPermission
	}
	return engine.WriteResult{Done: true, Note: "typing action accepted by Telegram"}, nil
}

func (e *Engine) Organise(ctx context.Context, r engine.OrganiseRequest) (engine.WriteResult, error) {
	var zero engine.WriteResult
	if err := e.messageActionGate(ctx, r.WriteOptions); err != nil {
		return zero, err
	}
	muteUntil := 0
	switch r.Action {
	case engine.OrganiseArchive, engine.OrganiseUnarchive, engine.OrganisePin, engine.OrganiseUnpin, engine.OrganiseUnmute:
		if r.MuteUntil != nil {
			return zero, engine.ErrInvalid
		}
	case engine.OrganiseMute:
		muteUntil = 2147483647
		if r.MuteUntil != nil {
			seconds := r.MuteUntil.Unix()
			if !r.MuteUntil.After(e.now()) || seconds > 2147483647 || seconds <= 0 {
				return zero, engine.ErrInvalid
			}
			muteUntil = int(seconds)
		}
	default:
		return zero, engine.ErrInvalid
	}
	target, ip, err := e.writePeer(ctx, r.Peer)
	if err != nil {
		return zero, err
	}
	p := &engine.Preview{Action: "organise", Target: target, Text: string(r.Action), Reversible: true}
	dry, err := e.messageActionPreview(ctx, r.WriteOptions, p, ip, struct {
		Action    engine.OrganiseAction
		MuteUntil int
	}{r.Action, muteUntil})
	if err != nil {
		return zero, err
	}
	if dry {
		return engine.WriteResult{DryRun: true, Preview: p}, nil
	}
	switch r.Action {
	case engine.OrganiseArchive, engine.OrganiseUnarchive:
		folder := 0
		if r.Action == engine.OrganiseArchive {
			folder = 1
		}
		u, err := e.api.FoldersEditPeerFolders(ctx, []tg.InputFolderPeer{{Peer: ip, FolderID: folder}})
		if err != nil {
			return zero, rpcError(err)
		}
		return e.finishMessageAction(ctx, u, 1)
	case engine.OrganisePin, engine.OrganiseUnpin:
		accepted, err := e.api.MessagesToggleDialogPin(ctx, &tg.MessagesToggleDialogPinRequest{Peer: &tg.InputDialogPeer{Peer: ip}, Pinned: r.Action == engine.OrganisePin})
		if err != nil {
			return zero, rpcError(err)
		}
		if !accepted {
			return zero, engine.ErrPermission
		}
	case engine.OrganiseMute, engine.OrganiseUnmute:
		settings := tg.InputPeerNotifySettings{}
		settings.SetMuteUntil(muteUntil)
		accepted, err := e.api.AccountUpdateNotifySettings(ctx, &tg.AccountUpdateNotifySettingsRequest{Peer: &tg.InputNotifyPeer{Peer: ip}, Settings: settings})
		if err != nil {
			return zero, rpcError(err)
		}
		if !accepted {
			return zero, engine.ErrPermission
		}
	}
	// Never replace a complete dialog with guessed zero counters. Updates/refresh
	// provide the authoritative folder, pin and notification state.
	return engine.WriteResult{Done: true, Affected: 1, Note: "chat organisation accepted by Telegram; local state follows updates or refresh"}, nil
}
