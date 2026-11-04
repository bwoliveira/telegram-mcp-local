package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/i18n"
	"modernc.org/sqlite"
)

func init() {
	// SQLite lower() is ASCII-only. Use identical Unicode normalization for
	// query and column while retaining filtering/pagination inside SQLite.
	sqlite.MustRegisterDeterministicScalarFunction("panel_unicode_lower", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if args[0] == nil {
			return "", nil
		}
		value, ok := args[0].(string)
		if !ok {
			return nil, engine.ErrInvalid
		}
		return strings.ToLower(value), nil
	})
}

// PanelChat is the panel's public, transport-free local chat projection.
type PanelChat struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Type          engine.PeerType `json:"type"`
	UnreadCount   int             `json:"unread_count"`
	LastMessageAt *time.Time      `json:"last_message_at"`

	// The inherited Conta preview contract remains canonical. The fields above
	// are additive aliases for SQL-paginated discovery in Conversas.
	JID         string     `json:"jid"`
	Group       bool       `json:"group"`
	Unread      int        `json:"unread"`
	Pinned      bool       `json:"pinned,omitempty"`
	At          *time.Time `json:"at"`
	LastAt      *time.Time `json:"last_at"`
	LastFromMe  bool       `json:"last_from_me"`
	LastSender  string     `json:"last_sender,omitempty"`
	LastText    string     `json:"last_text"`
	LastMedia   string     `json:"last_media,omitempty"`
	OldestKnown *time.Time `json:"oldest_known"`
	Messages    int64      `json:"messages"`
}

// PanelMessage never exposes physical row IDs, access hashes or remote media.
type PanelMessage struct {
	ID     string    `json:"id"`
	ChatID string    `json:"chat_id"`
	Text   string    `json:"text"`
	Sender string    `json:"sender,omitempty"`
	Date   time.Time `json:"date"`
	Out    bool      `json:"out"`

	ChatJID    string    `json:"chat_jid"`
	ChatName   string    `json:"chat_name,omitempty"`
	FromMe     bool      `json:"from_me"`
	At         time.Time `json:"at"`
	Media      string    `json:"media,omitempty"`
	Group      bool      `json:"group,omitempty"`
	Transcript string    `json:"transcript,omitempty"`
	Corrected  bool      `json:"transcript_corrected,omitempty"`
	// Cursor is this message's position in the conversation's total order,
	// (date, msg_id). It is set only for one-conversation reads, where it is
	// unambiguous, and is accepted back as PanelMessagesQuery.Before/Since.
	Cursor string `json:"cursor,omitempty"`
}

// PanelCursor is a keyset position inside one conversation: Telegram dates
// have one-second resolution, so msg_id breaks ties exactly as the SQL order.
type PanelCursor struct {
	Date int64
	ID   int64
}

// String is the opaque wire form "date:msg_id" accepted by ParsePanelCursor.
func (c PanelCursor) String() string {
	return strconv.FormatInt(c.Date, 10) + ":" + strconv.FormatInt(c.ID, 10)
}

// ParsePanelCursor accepts only the canonical form String produces.
func ParsePanelCursor(value string) (PanelCursor, error) {
	date, id, ok := strings.Cut(value, ":")
	d, err1 := strconv.ParseInt(date, 10, 64)
	n, err2 := strconv.ParseInt(id, 10, 64)
	if !ok || err1 != nil || err2 != nil || n <= 0 || d < 0 ||
		strconv.FormatInt(d, 10) != date || strconv.FormatInt(n, 10) != id {
		return PanelCursor{}, engine.ErrInvalid
	}
	return PanelCursor{Date: d, ID: n}, nil
}

// PanelChatsQuery filters before pagination; Type is a Telegram namespace.
type PanelChatsQuery struct {
	Query  string
	Unread *bool
	Type   engine.PeerType
	Limit  int
	Offset int
}

// PanelMessagesQuery uses FTS5 syntax over text and transcripts, never sender.
// Before (exclusive) and Since (inclusive) bound a one-conversation read by
// its total order instead of an offset, so rows arriving or leaving between
// two pages neither repeat nor skip a row. They require Peer and no Offset.
type PanelMessagesQuery struct {
	Query  string
	Peer   *engine.Peer
	Limit  int
	Offset int
	Before *PanelCursor
	Since  *PanelCursor
}

type PanelChatsPage struct {
	Chats      []PanelChat `json:"chats"`
	HasMore    bool        `json:"has_more"`
	NextOffset int         `json:"next_offset"`
}

type PanelMessagesPage struct {
	Chat       *PanelChat     `json:"chat,omitempty"`
	Messages   []PanelMessage `json:"messages"`
	HasMore    bool           `json:"has_more"`
	NextOffset int            `json:"next_offset"`
	Oldest     *time.Time     `json:"oldest"`
}

// panelPageLimit is intentionally independent of the older engine query cap.
// Reject invalid bounds rather than silently changing the caller's page size.
func panelPageLimit(limit, offset int) (int, error) {
	if limit < 0 || limit > 100 || offset < 0 {
		return 0, engine.ErrInvalid
	}
	if limit == 0 {
		limit = 30
	}
	return limit, nil
}

const panelChatName = `CASE WHEN p.title<>'' THEN p.title ELSE trim(p.first_name||' '||p.last_name) END`

// panelChatSQL derives every preview field from the same currently visible
// local messages. Neither dialog dates nor stale transcript/FTS rows can revive
// deleted/expired content. Media TTL keeps a caption but hides its media kind.
const panelChatSQL = `WITH visible AS (
 SELECT m.peer_type,m.peer_id,max(m.date) AS last_message_at,min(m.date) AS oldest_known,count(*) AS messages
 FROM messages m WHERE ` + localVisibleMessage + ` GROUP BY m.peer_type,m.peer_id
)
SELECT p.peer_type||':'||p.peer_id,` + panelChatName + `,p.peer_type,coalesce(d.unread_count,0),v.last_message_at,
 coalesce(d.pinned,0),coalesce(l.out,0),
 coalesce(nullif(CASE WHEN sp.title<>'' THEN sp.title ELSE trim(sp.first_name||' '||sp.last_name) END,''),nullif(l.sender_name,''),l.post_author,''),
 coalesce(l.text,''),CASE WHEN l.media_ttl=0 THEN coalesce(l.media_type,'') ELSE '' END,v.oldest_known,coalesce(v.messages,0)
 FROM peers p
 LEFT JOIN dialogs d ON d.account=p.account AND d.peer_type=p.peer_type AND d.peer_id=p.peer_id
 LEFT JOIN visible v ON v.peer_type=p.peer_type AND v.peer_id=p.peer_id
 LEFT JOIN messages l ON l.row_id=(SELECT m.row_id FROM messages m WHERE ` + localVisibleMessage + `
 AND m.peer_type=p.peer_type AND m.peer_id=p.peer_id ORDER BY m.date DESC,m.msg_id DESC LIMIT 1)
 LEFT JOIN peers sp ON sp.account=p.account AND sp.peer_type=l.sender_type AND sp.peer_id=l.sender_id
 WHERE p.account=?`

func panelChatArgs(account engine.AccountID, now int64) []any {
	return []any{account, now, now, account, now, now, account}
}

func panelScanChat(row interface{ Scan(...any) error }) (PanelChat, error) {
	var chat PanelChat
	var latest, oldest sql.NullInt64
	if err := row.Scan(&chat.ID, &chat.Name, &chat.Type, &chat.UnreadCount, &latest,
		&chat.Pinned, &chat.LastFromMe, &chat.LastSender, &chat.LastText, &chat.LastMedia, &oldest, &chat.Messages); err != nil {
		return PanelChat{}, err
	}
	chat.LastMessageAt = queryTime(latest.Int64, latest.Valid)
	chat.At, chat.LastAt = chat.LastMessageAt, chat.LastMessageAt
	chat.OldestKnown = queryTime(oldest.Int64, oldest.Valid)
	chat.JID, chat.Unread = chat.ID, chat.UnreadCount
	chat.Group = chat.Type != engine.PeerUser
	if !chat.Group || chat.LastFromMe {
		chat.LastSender = ""
	}
	if chat.Name == "" {
		switch chat.Type {
		case engine.PeerChat:
			chat.Name = i18n.N("Grupo sem nome")
		case engine.PeerChannel:
			chat.Name = i18n.N("Canal do Telegram")
		default:
			chat.Name = chat.ID
		}
	}
	chat.LastMedia = panelMediaKind(chat.LastMedia)
	return chat, nil
}

// Telegram media kinds are adapted to the original panel's presentation names.
func panelMediaKind(kind string) string {
	switch kind {
	case "photo":
		return "image"
	case "voice":
		return "audio"
	case "video_note", "animation":
		return "video"
	default:
		return kind
	}
}

// PanelChats is a strictly local, SQL-paginated read. Discovery matches the
// snapshot seam: dialogs, observed groups/channels and visible message peers,
// but not contact-only users or migrated source peers. Their typed history
// remains directly readable. Server unread counters/marks remain authoritative.
func (s *Store) PanelChats(ctx context.Context, account engine.AccountID, q PanelChatsQuery) (PanelChatsPage, error) {
	page := PanelChatsPage{Chats: []PanelChat{}}
	if err := s.checkReadAccount(ctx, account); err != nil {
		return page, err
	}
	limit, err := panelPageLimit(q.Limit, q.Offset)
	if err != nil {
		return page, err
	}
	if q.Type != "" && !queryValidPeer(engine.PeerRef{Type: q.Type, ID: 1}) {
		return page, engine.ErrInvalid
	}
	now := s.now().Unix()
	query := panelChatSQL + ` AND p.migrated_to_id IS NULL AND (d.peer_id IS NOT NULL OR p.peer_type IN ('chat','channel') OR v.messages>0)`
	args := panelChatArgs(account, now)
	if q.Type != "" {
		query += ` AND p.peer_type=?`
		args = append(args, q.Type)
	}
	if q.Unread != nil {
		if *q.Unread {
			query += ` AND (coalesce(d.unread_count,0)>0 OR coalesce(d.unread_mark,0)<>0)`
		} else {
			query += ` AND coalesce(d.unread_count,0)<=0 AND coalesce(d.unread_mark,0)=0`
		}
	}
	if term := strings.ToLower(strings.TrimSpace(q.Query)); term != "" {
		// instr treats %, _ and quotes literally, unlike an unescaped LIKE.
		query += ` AND (instr(panel_unicode_lower(` + panelChatName + `),?)>0
 OR instr(p.peer_type||':'||p.peer_id,?)>0 OR instr(lower(p.username),?)>0
 OR instr(p.phone,?)>0 OR EXISTS (SELECT 1 FROM peer_usernames u
 WHERE u.account=p.account AND u.peer_type=p.peer_type AND u.peer_id=p.peer_id AND instr(lower(u.username),?)>0))`
		args = append(args, term, term, term, term, term)
	}
	query += ` ORDER BY v.last_message_at DESC,p.peer_type DESC,p.peer_id DESC LIMIT ? OFFSET ?`
	args = append(args, limit+1, q.Offset)
	rows, err := s.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		chat, err := panelScanChat(rows)
		if err != nil {
			return page, err
		}
		page.Chats = append(page.Chats, chat)
	}
	if err := rows.Err(); err != nil {
		return page, err
	}
	if len(page.Chats) > limit {
		page.Chats = page.Chats[:limit]
		page.HasMore = true
		page.NextOffset = q.Offset + limit
	}
	return page, nil
}

// PanelConversation reads the persisted peer metadata, not caller-supplied
// names or counters. Retained history stays readable after access/auth loss.
// It selects only this typed peer; migrated peers retain separate identities.
func (s *Store) PanelConversation(ctx context.Context, account engine.AccountID, peer engine.Peer, q PanelMessagesQuery) (PanelMessagesPage, error) {
	page := PanelMessagesPage{Messages: []PanelMessage{}}
	if err := s.checkReadAccount(ctx, account); err != nil {
		return page, err
	}
	if !queryValidPeer(peer.Ref) || (q.Peer != nil && q.Peer.Ref != peer.Ref) {
		return page, engine.ErrInvalid
	}
	if _, err := panelPageLimit(q.Limit, q.Offset); err != nil {
		return page, err
	}
	now := s.now().Unix()
	args := append(panelChatArgs(account, now), peer.Ref.Type, peer.Ref.ID)
	chat, err := panelScanChat(s.reader.QueryRowContext(ctx, panelChatSQL+` AND p.peer_type=? AND p.peer_id=?`, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return page, engine.ErrPeerNotFound
	}
	if err != nil {
		return page, err
	}
	q.Peer = &peer
	page, err = s.panelMessages(ctx, account, q, false, now)
	if err != nil {
		return page, err
	}
	page.Chat = &chat
	page.Oldest = chat.OldestKnown
	return page, nil
}

// PanelSearch runs SQLite FTS5, with SQL filtering and limit+1 pagination.
// An empty expression is invalid; this method never silently lists all content.
func (s *Store) PanelSearch(ctx context.Context, account engine.AccountID, q PanelMessagesQuery) (PanelMessagesPage, error) {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return PanelMessagesPage{Messages: []PanelMessage{}}, err
	}
	return s.panelMessages(ctx, account, q, true, s.now().Unix())
}

// panelFTSError preserves cancellation and storage failures but does not echo
// malformed user expressions in public errors (same policy as SearchLocal).
func panelFTSError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, marker := range []string{"fts5", "syntax error", "unterminated", "no such column", "expected integer"} {
		if strings.Contains(err.Error(), marker) {
			return fmt.Errorf("store: invalid full-text expression: %w", engine.ErrInvalid)
		}
	}
	return err
}

func (s *Store) panelMessages(ctx context.Context, account engine.AccountID, q PanelMessagesQuery, requireQuery bool, now int64) (PanelMessagesPage, error) {
	page := PanelMessagesPage{Messages: []PanelMessage{}}
	limit, err := panelPageLimit(q.Limit, q.Offset)
	if err != nil {
		return page, err
	}
	if q.Peer != nil && !queryValidPeer(q.Peer.Ref) {
		return page, engine.ErrInvalid
	}
	if (q.Before != nil || q.Since != nil) && (q.Peer == nil || q.Offset != 0) {
		return page, engine.ErrInvalid
	}
	where := localVisibleMessage
	args := []any{account, now, now}
	if q.Peer != nil {
		where += ` AND m.peer_type=? AND m.peer_id=?`
		args = append(args, q.Peer.Ref.Type, q.Peer.Ref.ID)
	}
	// Within one peer, queryMessageOrder is exactly (date, msg_id) descending.
	// Row values let messages_by_date seek the date range.
	if q.Before != nil {
		where += ` AND (m.date,m.msg_id)<(?,?)`
		args = append(args, q.Before.Date, q.Before.ID)
	}
	if q.Since != nil {
		where += ` AND (m.date,m.msg_id)>=(?,?)`
		args = append(args, q.Since.Date, q.Since.ID)
	}
	if strings.TrimSpace(q.Query) == "" {
		if requireQuery {
			return page, engine.ErrInvalid
		}
	} else {
		// Validate the original expression before adding the column wrapper:
		// unmatched parentheses must not escape into the indexed sender column.
		var probe int
		err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ? LIMIT 1)`, q.Query).Scan(&probe)
		if err != nil {
			return page, panelFTSError(ctx, err)
		}
		// Protected media transcripts must not influence a match, including
		// negative FTS expressions. Keep captions searchable independently.
		where += ` AND ((m.media_ttl=0 AND m.row_id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?))
 OR (m.media_ttl<>0 AND m.row_id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)))`
		args = append(args, `{text transcript} : (`+q.Query+`)`, `{text} : (`+q.Query+`)`)
	}
	// Project only public columns. In particular, this does not decode or
	// retrieve remote media, physical keys, phone numbers or access hashes.
	query := `SELECT m.msg_id,m.peer_type||':'||m.peer_id,m.text,
 coalesce((SELECT nullif(CASE WHEN p.title<>'' THEN p.title ELSE trim(p.first_name||' '||p.last_name) END,'')
 FROM peers p WHERE p.account=m.account AND p.peer_type=m.sender_type AND p.peer_id=m.sender_id),
 nullif(m.sender_name,''),nullif(m.post_author,''),''),m.date,m.out,
 CASE WHEN m.media_ttl=0 THEN coalesce(m.media_type,'') ELSE '' END,
 CASE WHEN cp.title<>'' THEN cp.title ELSE trim(cp.first_name||' '||cp.last_name) END,
 coalesce(t.text,''),coalesce(t.raw_text,'')
 FROM messages m
 JOIN peers cp ON cp.account=m.account AND cp.peer_type=m.peer_type AND cp.peer_id=m.peer_id
 LEFT JOIN transcripts t ON t.row_id=m.row_id AND m.media_ttl=0 AND m.media_type IN ('audio','voice')
 WHERE ` + where + queryMessageOrder + ` LIMIT ? OFFSET ?`
	args = append(args, limit+1, q.Offset)
	rows, err := s.reader.QueryContext(ctx, query, args...)
	if err != nil {
		return page, panelFTSError(ctx, err)
	}
	defer rows.Close()
	for rows.Next() {
		var message PanelMessage
		var id, date int64
		var rawTranscript string
		if err := rows.Scan(&id, &message.ChatID, &message.Text, &message.Sender, &date, &message.Out,
			&message.Media, &message.ChatName, &message.Transcript, &rawTranscript); err != nil {
			return page, err
		}
		message.ID = strconv.FormatInt(id, 10)
		message.Date = time.Unix(date, 0).UTC()
		message.ChatJID, message.FromMe, message.At = message.ChatID, message.Out, message.Date
		message.Media = panelMediaKind(message.Media)
		message.Group = !strings.HasPrefix(message.ChatID, "user:")
		message.Corrected = rawTranscript != "" && rawTranscript != message.Transcript
		if q.Peer != nil {
			message.Cursor = PanelCursor{Date: date, ID: id}.String()
		}
		page.Messages = append(page.Messages, message)
	}
	if err := rows.Err(); err != nil {
		return page, panelFTSError(ctx, err)
	}
	if len(page.Messages) > limit {
		page.Messages = page.Messages[:limit]
		page.HasMore = true
		page.NextOffset = q.Offset + limit
	}
	return page, nil
}
