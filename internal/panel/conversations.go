package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"github.com/bwoliveira/telegram-mcp-local/internal/index"
	"github.com/bwoliveira/telegram-mcp-local/internal/mcp"
	"github.com/bwoliveira/telegram-mcp-local/internal/store"
)

// conversationReadStore is an optional structural extension of ReadStore.
// Pagination belongs in SQLite, not in an unbounded ReadMessages snapshot.
// No engine fallback: a panel read must never make a Telegram request.
type conversationReadStore interface {
	PanelChats(context.Context, engine.AccountID, store.PanelChatsQuery) (store.PanelChatsPage, error)
	PanelConversation(context.Context, engine.AccountID, engine.Peer, store.PanelMessagesQuery) (store.PanelMessagesPage, error)
	PanelSearch(context.Context, engine.AccountID, store.PanelMessagesQuery) (store.PanelMessagesPage, error)
}

func (p *Panel) conversationStore() (conversationReadStore, error) {
	s, ok := p.ReadStore.(conversationReadStore)
	if !ok || s == nil {
		return nil, userError{i18n.T("O armazenamento local de conversas está indisponível.")}
	}
	if p.AccountID == "" {
		return nil, engine.ErrAccountMismatch
	}
	return s, nil
}

func conversationInvalid(field string) error {
	return fmt.Errorf("invalid %s: %w", field, engine.ErrInvalid)
}

// Reject ambiguous duplicate values and malformed escapes rather than relying
// on URL.Query, which silently discards malformed query parameters.
func conversationQuery(r *http.Request, allowed ...string) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, conversationInvalid("query")
	}
	for k, v := range q {
		known := false
		for _, name := range allowed {
			if k == name {
				known = true
				break
			}
		}
		if !known || len(v) != 1 {
			return nil, conversationInvalid("query")
		}
	}
	return q, nil
}

func conversationBounds(q url.Values) (limit, offset int, err error) {
	limit = 30
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			return 0, 0, conversationInvalid("limit")
		}
	}
	if q.Has("offset") {
		offset, err = strconv.Atoi(q.Get("offset"))
		if err != nil || offset < 0 || offset > int(^uint(0)>>1)-100 {
			return 0, 0, conversationInvalid("offset")
		}
	}
	return limit, offset, nil
}

// Typed identifiers are literal canonical namespaces, never names to resolve
// remotely or Telegram's negative/-100-prefixed Bot API identifiers.
func conversationPeer(id string) (engine.Peer, error) {
	typ, number, ok := strings.Cut(id, ":")
	n, err := strconv.ParseInt(number, 10, 64)
	if !ok || err != nil || n <= 0 || strconv.FormatInt(n, 10) != number {
		return engine.Peer{}, conversationInvalid("chat_id")
	}
	if typ != "user" && typ != "chat" && typ != "channel" {
		return engine.Peer{}, conversationInvalid("chat_id")
	}
	return engine.Peer{Ref: engine.PeerRef{Type: engine.PeerType(typ), ID: n}}, nil
}

func (p *Panel) apiChats(r *http.Request) (any, error) {
	q, err := conversationQuery(r, "q", "unread", "type", "limit", "offset")
	if err != nil {
		return nil, err
	}
	limit, offset, err := conversationBounds(q)
	if err != nil {
		return nil, err
	}
	if !q.Has("limit") {
		limit = recentChats
	}
	filter := store.PanelChatsQuery{Query: q.Get("q"), Limit: limit, Offset: offset, Type: engine.PeerType(q.Get("type"))}
	if q.Has("type") && filter.Type != engine.PeerUser && filter.Type != engine.PeerChat && filter.Type != engine.PeerChannel {
		return nil, conversationInvalid("type")
	}
	if q.Has("unread") {
		value := q.Get("unread")
		if value != "true" && value != "false" {
			return nil, conversationInvalid("unread")
		}
		unread := value == "true"
		filter.Unread = &unread
	}
	s, err := p.conversationStore()
	if err != nil {
		return nil, err
	}
	page, err := s.PanelChats(r.Context(), p.AccountID, filter)
	if err != nil {
		return nil, err
	}
	if page.Chats == nil {
		page.Chats = []store.PanelChat{}
	}
	for i := range page.Chats {
		page.Chats[i].Name = index.ShownName(page.Chats[i].Name)
	}
	var history *mcp.HistoryJob
	if p.Server != nil {
		history = p.Server.HistoryStatus()
	}
	return struct {
		store.PanelChatsPage
		History *mcp.HistoryJob `json:"history"`
	}{page, history}, nil
}

func (p *Panel) apiConversation(r *http.Request) (any, error) {
	q, err := conversationQuery(r, "q", "limit", "offset", "before", "since")
	if err != nil {
		return nil, err
	}
	limit, offset, err := conversationBounds(q)
	if err != nil {
		return nil, err
	}
	if !q.Has("limit") {
		limit = conversationSize
	}
	filter := store.PanelMessagesQuery{Query: q.Get("q"), Limit: limit, Offset: offset}
	// Keyset bounds replace the offset: mixing both would page by neither.
	for name, target := range map[string]**store.PanelCursor{"before": &filter.Before, "since": &filter.Since} {
		if !q.Has(name) {
			continue
		}
		if q.Has("offset") {
			return nil, conversationInvalid(name)
		}
		cursor, err := store.ParsePanelCursor(q.Get(name))
		if err != nil {
			return nil, conversationInvalid(name)
		}
		*target = &cursor
	}
	peer, err := conversationPeer(r.PathValue("jid"))
	if err != nil {
		return nil, err
	}
	s, err := p.conversationStore()
	if err != nil {
		return nil, err
	}
	page, err := s.PanelConversation(r.Context(), p.AccountID, peer, filter)
	if err != nil {
		return nil, conversationReadError(err)
	}
	if page.Messages == nil {
		page.Messages = []store.PanelMessage{}
	}
	// Select the newest bounded page in SQLite, then present oldest first,
	// exactly as index.Conversation and Conta's inherited script expect.
	for i, j := 0, len(page.Messages)-1; i < j; i, j = i+1, j-1 {
		page.Messages[i], page.Messages[j] = page.Messages[j], page.Messages[i]
	}
	if page.Chat != nil {
		page.Chat.Name = index.ShownName(page.Chat.Name)
	}
	for i := range page.Messages {
		page.Messages[i].ChatName = index.ShownName(page.Messages[i].ChatName)
		if page.Messages[i].FromMe {
			page.Messages[i].Sender = ""
		}
	}
	return page, nil
}

func conversationReadError(err error) error {
	if errors.Is(err, engine.ErrPeerNotFound) {
		return userError{i18n.T("Esta conversa não está disponível no armazenamento local.")}
	}
	return err
}

func (p *Panel) apiSearch(r *http.Request) (any, error) {
	q, err := conversationQuery(r, "q", "chat_id", "limit", "offset")
	if err != nil {
		return nil, err
	}
	limit, offset, err := conversationBounds(q)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(q.Get("q")) == "" {
		return nil, conversationInvalid("q")
	}
	filter := store.PanelMessagesQuery{Query: q.Get("q"), Limit: limit, Offset: offset}
	if q.Has("chat_id") {
		peer, err := conversationPeer(q.Get("chat_id"))
		if err != nil {
			return nil, err
		}
		filter.Peer = &peer
	}
	s, err := p.conversationStore()
	if err != nil {
		return nil, err
	}
	return s.PanelSearch(r.Context(), p.AccountID, filter)
}

// apiHistory preserves the inherited explicit POST contract. A history import
// is a bounded Telegram read, not a send/read-receipt mutation; it remains
// available in read_only mode. GET browsing never invokes this path.
func (p *Panel) apiHistory(r *http.Request) (any, error) {
	if _, err := conversationQuery(r); err != nil {
		return nil, err
	}
	var body *struct {
		ChatJID string `json:"chat_jid"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body == nil {
		return nil, conversationInvalid("body")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, conversationInvalid("body")
	}
	if body.ChatJID != "" {
		if _, err := conversationPeer(body.ChatJID); err != nil {
			return nil, err
		}
	}
	// Never substitute a local page or claim that a job started when the
	// configured sync_history adapter is unavailable.
	if p.Server == nil || p.Engine == nil {
		return nil, userError{i18n.T("A busca de histórico no Telegram está indisponível; nenhuma importação foi iniciada. As mensagens locais continuam disponíveis.")}
	}
	account, err := p.Engine.Account(r.Context())
	if err != nil {
		return nil, err
	}
	if p.AccountID == "" || account.ID != p.AccountID {
		return nil, engine.ErrAccountMismatch
	}
	rounds := 1
	if body.ChatJID != "" {
		rounds = 3
	}
	job, started, err := p.Server.RequestHistory(r.Context(), body.ChatJID, 50, rounds, recentChats)
	if err != nil {
		return nil, userError{i18n.T("Não foi possível buscar histórico no Telegram; nenhuma importação foi iniciada.")}
	}
	return map[string]any{"started": started, "job": job}, nil
}
