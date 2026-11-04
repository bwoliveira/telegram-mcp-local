package gotdengine

import (
	"context"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

// TranscribeNative is the sole opt-in path to Telegram's transcription service.
// It neither downloads audio nor invokes local/cloud fallback providers. A
// pending response is returned as-is: polling requires another explicit call.
func (e *Engine) TranscribeNative(parent context.Context, key engine.MessageKey) (engine.NativeTranscript, error) {
	ctx, cancel := e.mediaContext(parent)
	defer cancel()
	m, err := e.fetchMediaMessage(ctx, key)
	if err != nil {
		return engine.NativeTranscript{}, err
	}
	f, err := mediaFileFor(m)
	if err != nil {
		return engine.NativeTranscript{}, err
	}
	switch f.kind {
	case engine.MediaAudio, engine.MediaVoice, engine.MediaVideoNote, engine.MediaVideo:
	default:
		return engine.NativeTranscript{}, engine.ErrNotSupported
	}
	peer, err := e.inputPeer(ctx, key.Peer)
	if err != nil {
		return engine.NativeTranscript{}, err
	}
	// Refresh the local guard after metadata/peer lookup, before the explicit RPC.
	if err = e.checkMedia(ctx, key); err != nil {
		return engine.NativeTranscript{}, err
	}
	got, err := e.api.MessagesTranscribeAudio(ctx, &tg.MessagesTranscribeAudioRequest{Peer: peer, MsgID: key.ID})
	if err != nil {
		return engine.NativeTranscript{}, rpcError(err)
	}
	if err = e.checkRawMedia(m); err != nil {
		return engine.NativeTranscript{}, err
	}
	if err = e.checkMedia(ctx, key); err != nil {
		return engine.NativeTranscript{}, err
	}
	return engine.NativeTranscript{Text: got.Text, Pending: got.Pending, ID: got.TranscriptionID}, nil
}
