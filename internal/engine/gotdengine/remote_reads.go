package gotdengine

import (
	"context"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/gotd/td/tg"
)

func (e *Engine) SearchRemote(ctx context.Context, req engine.SearchRequest) (engine.SearchResult, error) {
	var result engine.SearchResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if strings.TrimSpace(req.Query) == "" || req.Limit < 0 || (!req.Since.IsZero() && !req.Until.IsZero() && !req.Since.Before(req.Until)) {
		return result, engine.ErrInvalid
	}
	if err := e.require(); err != nil {
		return result, err
	}
	filter, err := remoteMediaFilter(req.Media)
	if err != nil {
		return result, err
	}
	limit := req.Limit
	if limit == 0 {
		limit = 50
	}
	limit = min(limit, 100)
	var got tg.MessagesMessagesClass
	if req.Peer != nil {
		ip, err := e.inputPeer(ctx, *req.Peer)
		if err != nil {
			return result, err
		}
		got, err = e.api.MessagesSearch(ctx, &tg.MessagesSearchRequest{Peer: ip, Q: req.Query, Filter: filter, MinDate: searchUnix(req.Since), MaxDate: searchUnix(req.Until), Limit: limit})
		if err != nil {
			return result, rpcError(err)
		}
	} else {
		got, err = e.api.MessagesSearchGlobal(ctx, &tg.MessagesSearchGlobalRequest{Q: req.Query, Filter: filter, MinDate: searchUnix(req.Since), MaxDate: searchUnix(req.Until), OffsetPeer: &tg.InputPeerEmpty{}, Limit: limit})
		if err != nil {
			return result, rpcError(err)
		}
	}
	fallback := engine.PeerRef{}
	if req.Peer != nil {
		fallback = *req.Peer
	}
	messages, err := e.retainReadPage(ctx, got, fallback)
	if err != nil {
		return result, err
	}
	result.Messages = make([]engine.Message, 0, len(messages))
	for _, message := range messages {
		if req.Media == "" || (message.Media != nil && message.Media.Type == req.Media) {
			result.Messages = append(result.Messages, message)
		}
	}
	// Some Telegram attachment kinds have no exact search filter. A bounded
	// broader search followed by local filtering cannot prove completeness.
	result.Partial = len(result.Messages) != len(messages)
	switch v := got.(type) {
	case *tg.MessagesMessagesSlice:
		result.Partial = result.Partial || v.Count > len(messages)
	case *tg.MessagesChannelMessages:
		result.Partial = result.Partial || v.Count > len(messages)
	}
	return result, nil
}

func (e *Engine) FetchWindow(ctx context.Context, req engine.WindowRequest) ([]engine.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req.Key.Account != e.cfg.Account {
		return nil, engine.ErrAccountMismatch
	}
	if req.Key.ID <= 0 || req.Before < 0 || req.After < 0 || req.Before > 50 || req.After > 50 {
		return nil, engine.ErrInvalid
	}
	if err := e.require(); err != nil {
		return nil, err
	}
	ip, err := e.inputPeer(ctx, req.Key.Peer)
	if err != nil {
		return nil, err
	}
	// Negative add_offset includes newer messages before Telegram's descending
	// history boundary. The anchor is included in the requested page size.
	got, err := e.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: ip, OffsetID: req.Key.ID, AddOffset: -req.After - 1, Limit: req.Before + req.After + 1})
	if err != nil {
		return nil, rpcError(err)
	}
	return e.retainReadPage(ctx, got, req.Key.Peer)
}

func (e *Engine) retainReadPage(ctx context.Context, got tg.MessagesMessagesClass, fallback engine.PeerRef) ([]engine.Message, error) {
	raw, users, chats := messagePage(got)
	// Search results and context windows recover history, not live arrivals.
	// Preserve their refresh provenance while marking every derived event.
	b := engine.Batch{Account: e.cfg.Account, Source: engine.SourceRefresh, CatchUp: true, Peers: peerUpserts(users, chats)}
	for _, m := range raw {
		if fallback.ID > 0 {
			var peer engine.PeerRef
			switch msg := m.(type) {
			case *tg.Message:
				peer = peerRef(msg.PeerID)
			case *tg.MessageService:
				peer = peerRef(msg.PeerID)
			case *tg.MessageEmpty:
				peer = peerRef(msg.PeerID)
			}
			if peer.ID > 0 && peer != fallback {
				return nil, engine.ErrInvalid
			}
		}
		appendMessage(&b, m, false, fallback)
	}
	if _, err := e.apply(ctx, b); err != nil {
		return nil, err
	}
	// Read back exactly the accepted rows, rather than reviving expired content,
	// tombstones or stale server edits from the transport DTOs.
	messages := make([]engine.Message, 0, len(b.Messages))
	for _, m := range b.Messages {
		current, ok, err := e.cfg.Store.Message(ctx, m.Key)
		if err != nil {
			return nil, err
		}
		if ok {
			messages = append(messages, current)
		}
	}
	return messages, nil
}
func searchUnix(t time.Time) int {
	if t.IsZero() {
		return 0
	}
	return int(t.Unix())
}
func remoteMediaFilter(media engine.MediaType) (tg.MessagesFilterClass, error) {
	switch media {
	case "":
		return &tg.InputMessagesFilterEmpty{}, nil
	case engine.MediaPhoto:
		return &tg.InputMessagesFilterPhotos{}, nil
	case engine.MediaVideo:
		return &tg.InputMessagesFilterVideo{}, nil
	case engine.MediaDocument:
		return &tg.InputMessagesFilterDocument{}, nil
	case engine.MediaVoice:
		return &tg.InputMessagesFilterVoice{}, nil
	case engine.MediaAudio:
		return &tg.InputMessagesFilterMusic{}, nil
	case engine.MediaAnimation:
		return &tg.InputMessagesFilterGif{}, nil
	case engine.MediaVideoNote:
		return &tg.InputMessagesFilterRoundVideo{}, nil
	case engine.MediaSticker, engine.MediaContact, engine.MediaGeo,
		engine.MediaVenue, engine.MediaLiveGeo, engine.MediaPoll, engine.MediaWebPage:
		return &tg.InputMessagesFilterEmpty{}, nil
	default:
		return nil, engine.ErrInvalid
	}
}
