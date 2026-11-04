package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// messageColumns deliberately omits physical row ids and media_remote.
// Every SELECT using it must name its messages table m.
const messageColumns = `m.account,m.peer_type,m.peer_id,m.msg_id,m.date,m.edit_date,m.server_edit_date,m.content_hash,m.content_identity,m.out,
m.sender_type,m.sender_id,m.sender_name,m.post_author,m.text,m.entities_json,
m.media_type,m.media_json,m.media_ttl,m.location_json,m.poll_json,
m.reply_to_msg_id,m.reply_to_peer_type,m.reply_to_peer_id,m.reply_top_id,m.forward_json,
m.topic_id,m.grouped_id,m.service_type,m.service_json,m.mentioned,m.media_unread,
m.silent,m.post,m.pinned,(m.protected OR coalesce((SELECT p.protected FROM peers p WHERE p.account=m.account AND p.peer_type=m.peer_type AND p.peer_id=m.peer_id),0)),m.views,m.forwards,m.ttl_period,m.expires_at,
(SELECT r.counts_json FROM reactions r WHERE r.row_id=m.row_id),
(SELECT CASE WHEN p.title<>'' THEN p.title ELSE trim(p.first_name||' '||p.last_name) END
 FROM peers p WHERE p.account=m.account AND p.peer_type=m.peer_type AND p.peer_id=m.peer_id)`

func scanMessage(scanner interface{ Scan(...any) error }) (engine.Message, error) {
	var m engine.Message
	var date, edit, serverEdit int64
	var senderType, mediaType, entities, media, location, poll, replyType, forward, serviceType, service, reactions, chatName sql.NullString
	var senderID, replyID, replyPeerID, replyTop, expires sql.NullInt64
	var mediaTTL int
	err := scanner.Scan(&m.Key.Account, &m.Key.Peer.Type, &m.Key.Peer.ID, &m.Key.ID, &date, &edit, &serverEdit, &m.ContentHash, &m.ContentIdentity, &m.Out,
		&senderType, &senderID, &m.SenderName, &m.PostAuthor, &m.Text, &entities, &mediaType, &media, &mediaTTL, &location, &poll,
		&replyID, &replyType, &replyPeerID, &replyTop, &forward, &m.TopicID, &m.GroupedID, &serviceType, &service,
		&m.Mentioned, &m.MediaUnread, &m.Silent, &m.Post, &m.Pinned, &m.Protected, &m.Views, &m.Forwards, &m.TTLPeriod, &expires, &reactions, &chatName)
	if err != nil {
		return m, err
	}
	m.Date = time.Unix(date, 0).UTC()
	m.EditDate = queryTime(edit, edit != 0)
	m.ServerEditDate = queryTime(serverEdit, serverEdit != 0)
	m.ExpiresAt = queryTime(expires.Int64, expires.Valid)
	m.ChatName = chatName.String
	if senderType.Valid && senderID.Valid {
		m.Sender = &engine.PeerRef{Type: engine.PeerType(senderType.String), ID: senderID.Int64}
	}
	for _, v := range []struct {
		name string
		raw  sql.NullString
		dst  any
	}{
		{"entities", entities, &m.Entities}, {"media", media, &m.Media}, {"location", location, &m.Location}, {"poll", poll, &m.Poll},
		{"forward", forward, &m.Forward}, {"service", service, &m.Service}, {"reactions", reactions, &m.Reactions},
	} {
		if v.raw.Valid && v.raw.String != "" {
			if err = json.Unmarshal([]byte(v.raw.String), v.dst); err != nil {
				return engine.Message{}, fmt.Errorf("store: invalid %s JSON: %w", v.name, err)
			}
		}
	}
	if mediaType.Valid || mediaTTL > 0 {
		if m.Media == nil {
			m.Media = &engine.Media{}
		}
		m.Media.Type = engine.MediaType(mediaType.String)
		m.Media.TTLSeconds = mediaTTL
	}
	if m.Media != nil {
		m.Media.Remote = nil
		if mediaTTL > 0 || m.Protected {
			m.Media.Downloadable = false
		}
	}
	if replyID.Valid || replyTop.Valid {
		m.ReplyTo = &engine.ReplyRef{ID: int(replyID.Int64), TopID: int(replyTop.Int64)}
		if replyType.Valid && replyPeerID.Valid {
			m.ReplyTo.Peer = &engine.PeerRef{Type: engine.PeerType(replyType.String), ID: replyPeerID.Int64}
		}
	}
	if serviceType.Valid {
		if m.Service == nil {
			m.Service = &engine.ServiceAction{}
		}
		m.Service.Type = serviceType.String
	}
	return m, nil
}
func queryTime(v int64, valid bool) *time.Time {
	if !valid {
		return nil
	}
	t := time.Unix(v, 0).UTC()
	return &t
}
func queryLimit(n int) (int, error) {
	if n < 0 || n > 1000 {
		return 0, engine.ErrInvalid
	}
	if n == 0 {
		n = 100
	}
	return n, nil
}
func queryValidPeer(p engine.PeerRef) bool {
	return p.ID > 0 && (p.Type == engine.PeerUser || p.Type == engine.PeerChat || p.Type == engine.PeerChannel)
}

// MessagePosition is an exclusive keyset boundary, not a physical SQLite row id.
type MessagePosition struct {
	Date time.Time         `json:"date"`
	Key  engine.MessageKey `json:"key"`
}
type MessageOptions struct {
	Limit           int
	Before          *MessagePosition
	Since, Until    time.Time // inclusive lower, exclusive upper bound
	TopicID         *int
	IncludeMigrated *bool // nil means true (D4)
}
type MessagePage struct {
	Messages     []engine.Message   `json:"messages"`
	Next         *MessagePosition   `json:"next,omitempty"`
	Access       engine.AccessState `json:"access,omitempty"`
	AccessLostAt *time.Time         `json:"access_lost_at,omitempty"`
}

// queryHistoryPeers traverses migration links in both directions, retaining each
// peer's namespace. Recursive UNION deduplicates even malformed cyclic links.
func (s *Store) queryHistoryPeers(ctx context.Context, acc engine.AccountID, p engine.PeerRef, merge *bool) ([]engine.PeerRef, error) {
	if !queryValidPeer(p) {
		return nil, engine.ErrInvalid
	}
	if merge != nil && !*merge {
		return []engine.PeerRef{p}, nil
	}
	rows, err := s.reader.QueryContext(ctx, `WITH RECURSIVE links(t,i,u,j) AS (
 SELECT peer_type,peer_id,migrated_to_type,migrated_to_id FROM peers WHERE account=? AND migrated_to_type IS NOT NULL
 UNION SELECT migrated_to_type,migrated_to_id,peer_type,peer_id FROM peers WHERE account=? AND migrated_to_type IS NOT NULL
 ), history(t,i) AS (SELECT ?,? UNION SELECT l.u,l.j FROM links l JOIN history h ON l.t=h.t AND l.i=h.i)
 SELECT t,i FROM history ORDER BY t,i`, acc, acc, p.Type, p.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []engine.PeerRef{}
	for rows.Next() {
		var ref engine.PeerRef
		if err = rows.Scan(&ref.Type, &ref.ID); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}
func queryPeerWhere(peers []engine.PeerRef) (string, []any) {
	parts := make([]string, 0, len(peers))
	args := []any{}
	for _, p := range peers {
		parts = append(parts, "(m.peer_type=? AND m.peer_id=?)")
		args = append(args, p.Type, p.ID)
	}
	return "(" + strings.Join(parts, " OR ") + ")", args
}
func queryMessageWhere(acc engine.AccountID, now time.Time, o MessageOptions) (string, []any, error) {
	if _, err := queryLimit(o.Limit); err != nil {
		return "", nil, err
	}
	if !o.Since.IsZero() && !o.Until.IsZero() && !o.Since.Before(o.Until) {
		return "", nil, engine.ErrInvalid
	}
	w := `m.account=? AND (m.expires_at IS NULL OR m.expires_at>?)`
	a := []any{acc, now.Unix()}
	if !o.Since.IsZero() {
		w += " AND m.date>=?"
		a = append(a, o.Since.Unix())
	}
	if !o.Until.IsZero() {
		w += " AND m.date<?"
		a = append(a, o.Until.Unix())
	}
	if o.TopicID != nil {
		if *o.TopicID < 0 {
			return "", nil, engine.ErrInvalid
		}
		w += " AND m.topic_id=?"
		a = append(a, *o.TopicID)
	}
	if o.Before != nil {
		b := o.Before
		if b.Key.Account != acc {
			return "", nil, engine.ErrAccountMismatch
		}
		if !queryValidPeer(b.Key.Peer) || b.Key.ID <= 0 || b.Date.IsZero() {
			return "", nil, engine.ErrInvalid
		}
		w += " AND (m.date,m.peer_type,m.peer_id,m.msg_id)<(?,?,?,?)"
		a = append(a, b.Date.Unix(), b.Key.Peer.Type, b.Key.Peer.ID, b.Key.ID)
	}
	return w, a, nil
}

const queryMessageOrder = ` ORDER BY m.date DESC,m.peer_type DESC,m.peer_id DESC,m.msg_id DESC`

func (s *Store) queryMessages(ctx context.Context, w string, a []any, limit int) (MessagePage, error) {
	page := MessagePage{Messages: []engine.Message{}}
	rows, err := s.reader.QueryContext(ctx, "SELECT "+messageColumns+" FROM messages m WHERE "+w+queryMessageOrder+" LIMIT ?", append(a, limit+1)...)
	if err != nil {
		return page, err
	}
	defer rows.Close()
	for rows.Next() {
		m, e := scanMessage(rows)
		if e != nil {
			return page, e
		}
		page.Messages = append(page.Messages, m)
	}
	if err = rows.Err(); err != nil {
		return page, err
	}
	if len(page.Messages) > limit {
		page.Messages = page.Messages[:limit]
		last := page.Messages[limit-1]
		page.Next = &MessagePosition{Date: last.Date, Key: last.Key}
	}
	return page, nil
}
func (s *Store) ListMessages(ctx context.Context, acc engine.AccountID, p engine.PeerRef, o MessageOptions) (MessagePage, error) {
	if err := s.checkAccount(acc); err != nil {
		return MessagePage{}, err
	}
	w, a, err := queryMessageWhere(acc, s.now(), o)
	if err != nil {
		return MessagePage{}, err
	}
	peers, err := s.queryHistoryPeers(ctx, acc, p, o.IncludeMigrated)
	if err != nil {
		return MessagePage{}, err
	}
	pw, pa := queryPeerWhere(peers)
	w += " AND " + pw
	a = append(a, pa...)
	n, _ := queryLimit(o.Limit)
	page, err := s.queryMessages(ctx, w, a, n)
	if err != nil {
		return page, err
	}
	var changed sql.NullInt64
	err = s.reader.QueryRowContext(ctx, `SELECT access,CASE WHEN access<>'ok' THEN access_changed_at END FROM peers WHERE account=? AND peer_type=? AND peer_id=?`, acc, p.Type, p.ID).Scan(&page.Access, &changed)
	if err == sql.ErrNoRows {
		return page, nil
	}
	page.AccessLostAt = queryTime(changed.Int64, changed.Valid)
	return page, err
}

// Context returns nearest neighbours and the anchor in chronological order.
// The anchor is an exact key; migrated peers contribute only neighbours.
func (s *Store) Context(ctx context.Context, key engine.MessageKey, before, after int) ([]engine.Message, error) {
	if err := s.checkAccount(key.Account); err != nil {
		return nil, err
	}
	if !queryValidPeer(key.Peer) || key.ID <= 0 || before < 0 || after < 0 || before > 1000 || after > 1000 {
		return nil, engine.ErrInvalid
	}
	now := s.now()
	anchor, err := scanMessage(s.reader.QueryRowContext(ctx, "SELECT "+messageColumns+` FROM messages m WHERE m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=? AND (m.expires_at IS NULL OR m.expires_at>?)`, key.Account, key.Peer.Type, key.Peer.ID, key.ID, now.Unix()))
	if err == sql.ErrNoRows {
		return nil, engine.ErrMessageNotFound
	}
	if err != nil {
		return nil, err
	}
	peers, err := s.queryHistoryPeers(ctx, key.Account, key.Peer, nil)
	if err != nil {
		return nil, err
	}
	pw, pa := queryPeerWhere(peers)
	w := `m.account=? AND (m.expires_at IS NULL OR m.expires_at>?) AND ` + pw
	args := append([]any{key.Account, now.Unix()}, pa...)
	side := func(n int, op, order string) ([]engine.Message, error) {
		out := []engine.Message{}
		if n == 0 {
			return out, nil
		}
		a := append(append([]any{}, args...), anchor.Date.Unix(), key.Peer.Type, key.Peer.ID, key.ID, n)
		rows, e := s.reader.QueryContext(ctx, "SELECT "+messageColumns+" FROM messages m WHERE "+w+" AND (m.date,m.peer_type,m.peer_id,m.msg_id)"+op+"(?,?,?,?) ORDER BY m.date "+order+",m.peer_type "+order+",m.peer_id "+order+",m.msg_id "+order+" LIMIT ?", a...)
		if e != nil {
			return nil, e
		}
		defer rows.Close()
		for rows.Next() {
			m, e := scanMessage(rows)
			if e != nil {
				return nil, e
			}
			out = append(out, m)
		}
		return out, rows.Err()
	}
	left, err := side(before, "<", "DESC")
	if err != nil {
		return nil, err
	}
	right, err := side(after, ">", "ASC")
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(left)-1; i < j; i, j = i+1, j-1 {
		left[i], left[j] = left[j], left[i]
	}
	out := append(left, anchor)
	return append(out, right...), nil
}

type SearchOptions struct {
	MessageOptions
	Query  string
	Peer   *engine.PeerRef
	Media  engine.MediaType
	Sender *engine.PeerRef
}
type SearchHit struct {
	engine.Message
	Transcript string `json:"transcript,omitempty"`
}
type Coverage struct {
	HistorySince *time.Time       `json:"history_since,omitempty"`
	Complete     bool             `json:"complete"`
	Cursors      []engine.Cursor  `json:"cursors"`
	Gaps         []engine.GapMark `json:"gaps"`
}
type SearchPage struct {
	Messages []SearchHit      `json:"messages"`
	Next     *MessagePosition `json:"next,omitempty"`
	FullText bool             `json:"full_text"`
	Coverage Coverage         `json:"coverage"`
}

func (s *Store) SearchMessages(ctx context.Context, acc engine.AccountID, o SearchOptions) (SearchPage, error) {
	out := SearchPage{Messages: []SearchHit{}}
	if err := s.checkAccount(acc); err != nil {
		return out, err
	}
	if strings.TrimSpace(o.Query) == "" {
		return out, engine.ErrInvalid
	}
	w, a, err := queryMessageWhere(acc, s.now(), o.MessageOptions)
	if err != nil {
		return out, err
	}
	var peers []engine.PeerRef
	if o.Peer != nil {
		peers, err = s.queryHistoryPeers(ctx, acc, *o.Peer, o.IncludeMigrated)
		if err != nil {
			return out, err
		}
		pw, pa := queryPeerWhere(peers)
		w += " AND " + pw
		a = append(a, pa...)
	}
	if o.Media != "" {
		w += " AND m.media_type=?"
		a = append(a, o.Media)
	}
	if o.Sender != nil {
		if !queryValidPeer(*o.Sender) {
			return out, engine.ErrInvalid
		}
		w += " AND m.sender_type=? AND m.sender_id=?"
		a = append(a, o.Sender.Type, o.Sender.ID)
	}
	// Validate MATCH separately so syntax errors have a stable sentinel without
	// copying the query, message text or other SQLite details into diagnostics.
	fts := "{text transcript} : (" + o.Query + ")"
	var probe int
	err = s.reader.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ? LIMIT 1)`, fts).Scan(&probe)
	if err != nil {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if strings.Contains(err.Error(), "fts5") || strings.Contains(err.Error(), "syntax error") || strings.Contains(err.Error(), "unterminated") || strings.Contains(err.Error(), "no such column") || strings.Contains(err.Error(), "expected integer") {
			return out, fmt.Errorf("store: invalid full-text expression: %w", engine.ErrInvalid)
		}
		return out, err
	}
	w += " AND m.row_id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)"
	a = append(a, fts)
	n, _ := queryLimit(o.Limit)
	page, err := s.queryMessages(ctx, w, a, n)
	if err != nil {
		return out, err
	}
	for _, m := range page.Messages {
		var transcript sql.NullString
		err = s.reader.QueryRowContext(ctx, `SELECT t.text FROM messages m LEFT JOIN transcripts t ON t.row_id=m.row_id WHERE m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=? AND (m.expires_at IS NULL OR m.expires_at>?)`, acc, m.Key.Peer.Type, m.Key.Peer.ID, m.Key.ID, s.now().Unix()).Scan(&transcript)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return out, err
		}
		out.Messages = append(out.Messages, SearchHit{Message: m, Transcript: transcript.String})
	}
	out.Next = page.Next
	out.FullText = true
	out.Coverage, err = s.queryCoverage(ctx, acc, peers)
	return out, err
}
func (s *Store) queryCoverage(ctx context.Context, acc engine.AccountID, peers []engine.PeerRef) (Coverage, error) {
	c := Coverage{Complete: true, Cursors: []engine.Cursor{}, Gaps: []engine.GapMark{}}
	global := peers == nil
	if global {
		rows, err := s.reader.QueryContext(ctx, `SELECT peer_type,peer_id FROM dialogs WHERE account=? UNION SELECT peer_type,peer_id FROM messages WHERE account=? AND (expires_at IS NULL OR expires_at>?) UNION SELECT peer_type,peer_id FROM backfill_cursors WHERE account=?`, acc, acc, s.now().Unix(), acc)
		if err != nil {
			return c, err
		}
		for rows.Next() {
			var p engine.PeerRef
			if err = rows.Scan(&p.Type, &p.ID); err != nil {
				rows.Close()
				return c, err
			}
			peers = append(peers, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return c, err
		}
	}
	if len(peers) == 0 {
		c.Complete = false
	}
	for _, p := range peers {
		var cur engine.Cursor
		cur.Peer = p
		var oldest, flood sql.NullInt64
		err := s.reader.QueryRowContext(ctx, `SELECT newest_id,oldest_id,oldest_date,complete,state,flood_until,last_error,fetched FROM backfill_cursors WHERE account=? AND peer_type=? AND peer_id=?`, acc, p.Type, p.ID).Scan(&cur.NewestID, &cur.OldestID, &oldest, &cur.Complete, &cur.State, &flood, &cur.LastError, &cur.Fetched)
		if err == sql.ErrNoRows {
			c.Complete = false
			continue
		}
		if err != nil {
			return c, err
		}
		cur.OldestDate = queryTime(oldest.Int64, oldest.Valid)
		cur.FloodUntil = queryTime(flood.Int64, flood.Valid)
		c.Cursors = append(c.Cursors, cur)
		if !cur.Complete || cur.State == "error" || cur.State == "no_access" {
			c.Complete = false
		}
		if cur.OldestDate != nil && (c.HistorySince == nil || cur.OldestDate.Before(*c.HistorySince)) {
			c.HistorySince = cur.OldestDate
		}
	}
	w := `account=? AND filled_at IS NULL`
	a := []any{acc}
	if !global && len(peers) > 0 {
		parts := []string{"peer_type IS NULL"}
		for _, p := range peers {
			parts = append(parts, "(peer_type=? AND peer_id=?)")
			a = append(a, p.Type, p.ID)
		}
		w += " AND (" + strings.Join(parts, " OR ") + ")"
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT g.peer_type,g.peer_id,g.reason,g.from_id,g.to_id,g.detected_at,`+gapRecoveryCauseSQL+` FROM gaps g WHERE `+w+` ORDER BY detected_at,id`, a...)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var g engine.GapMark
		var typ sql.NullString
		var id, from, to sql.NullInt64
		var date int64
		if err = rows.Scan(&typ, &id, &g.Reason, &from, &to, &date, &g.RecoveryCause); err != nil {
			return c, err
		}
		if typ.Valid && id.Valid {
			g.Peer = &engine.PeerRef{Type: engine.PeerType(typ.String), ID: id.Int64}
		}
		g.FromID = int(from.Int64)
		g.ToID = int(to.Int64)
		g.Detected = time.Unix(date, 0).UTC()
		classifyGap(&g)
		c.Gaps = append(c.Gaps, g)
		c.Complete = false
	}
	return c, rows.Err()
}

const queryPeerColumns = `p.peer_type,p.peer_id,p.kind,p.title,p.first_name,p.last_name,p.username,p.phone,p.is_contact,p.forum,p.protected,p.participants,p.access,p.migrated_to_type,p.migrated_to_id,
(SELECT json_group_array(u.username) FROM peer_usernames u WHERE u.account=p.account AND u.peer_type=p.peer_type AND u.peer_id=p.peer_id)`

func queryScanPeer(row interface{ Scan(...any) error }) (engine.Peer, error) {
	var p engine.Peer
	var typ sql.NullString
	var id sql.NullInt64
	var usernames string
	err := row.Scan(&p.Ref.Type, &p.Ref.ID, &p.Kind, &p.Title, &p.FirstName, &p.LastName, &p.Username, &p.Phone, &p.IsContact, &p.Forum, &p.Protected, &p.Participants, &p.Access, &typ, &id, &usernames)
	if err != nil {
		return p, err
	}
	if typ.Valid && id.Valid {
		p.MigratedTo = &engine.PeerRef{Type: engine.PeerType(typ.String), ID: id.Int64}
	}
	err = json.Unmarshal([]byte(usernames), &p.Usernames)
	return p, err
}
func (s *Store) queryLookupPeer(ctx context.Context, acc engine.AccountID, p engine.PeerRef) (engine.Peer, bool, error) {
	peer, err := queryScanPeer(s.reader.QueryRowContext(ctx, "SELECT "+queryPeerColumns+" FROM peers p WHERE p.account=? AND p.peer_type=? AND p.peer_id=?", acc, p.Type, p.ID))
	if err == sql.ErrNoRows {
		return peer, false, nil
	}
	return peer, err == nil, err
}

type PeerOptions struct {
	Query         string
	Limit, Offset int
}

func queryPeerFilter(q string) (string, []any) {
	if strings.TrimSpace(q) == "" {
		return "", nil
	}
	q = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(q), "@"))
	q = strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(q)
	like := "%" + q + "%"
	return ` AND (lower(p.title||' '||p.first_name||' '||p.last_name) LIKE ? ESCAPE '\' OR p.search_name LIKE ? ESCAPE '\' OR lower(p.username) LIKE ? ESCAPE '\' OR p.phone LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM peer_usernames u WHERE u.account=p.account AND u.peer_type=p.peer_type AND u.peer_id=p.peer_id AND lower(u.username) LIKE ? ESCAPE '\'))`, []any{like, "%" + normalize(q) + "%", like, like, like}
}
func (s *Store) queryPeers(ctx context.Context, acc engine.AccountID, o PeerOptions, filter string) ([]engine.Peer, error) {
	if err := s.checkAccount(acc); err != nil {
		return nil, err
	}
	n, err := queryLimit(o.Limit)
	if err != nil || o.Offset < 0 {
		return nil, engine.ErrInvalid
	}
	w, a := queryPeerFilter(o.Query)
	args := append([]any{acc}, a...)
	args = append(args, n, o.Offset)
	rows, err := s.reader.QueryContext(ctx, "SELECT "+queryPeerColumns+" FROM peers p WHERE p.account=? AND "+filter+w+" ORDER BY p.search_name,p.peer_type,p.peer_id LIMIT ? OFFSET ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []engine.Peer{}
	for rows.Next() {
		p, e := queryScanPeer(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Store) ListContacts(ctx context.Context, acc engine.AccountID, o PeerOptions) ([]engine.Peer, error) {
	return s.queryPeers(ctx, acc, o, `p.peer_type='user' AND p.is_contact=1`)
}
func (s *Store) ListGroups(ctx context.Context, acc engine.AccountID, o PeerOptions) ([]engine.Peer, error) {
	return s.queryPeers(ctx, acc, o, `p.kind IN ('group','supergroup','channel')`)
}
func (s *Store) Group(ctx context.Context, acc engine.AccountID, ref engine.PeerRef) (engine.Group, bool, error) {
	var g engine.Group
	if err := s.checkAccount(acc); err != nil {
		return g, false, err
	}
	if !queryValidPeer(ref) || ref.Type == engine.PeerUser {
		return g, false, engine.ErrInvalid
	}
	peer, ok, err := s.queryLookupPeer(ctx, acc, ref)
	if err != nil || !ok {
		return g, ok, err
	}
	g.Peer = peer
	var owner, created, linkedID sql.NullInt64
	var linkedType sql.NullString
	var at int64
	err = s.reader.QueryRowContext(ctx, `SELECT about,owner_id,created_at,linked_type,linked_id,size,partial,am_admin,snapshot_at FROM groups_full WHERE account=? AND peer_type=? AND peer_id=?`, acc, ref.Type, ref.ID).Scan(&g.About, &owner, &created, &linkedType, &linkedID, &g.Size, &g.Partial, &g.AmAdmin, &at)
	if err == sql.ErrNoRows {
		g.Partial = true
		g.Size = peer.Participants
		g.Participants = []engine.Participant{}
		return g, true, nil
	}
	if err != nil {
		return g, false, err
	}
	g.CreatedAt = queryTime(created.Int64, created.Valid)
	g.SnapshotAt = time.Unix(at, 0).UTC()
	if owner.Valid {
		g.Owner = &engine.PeerRef{Type: engine.PeerUser, ID: owner.Int64}
	}
	if linkedType.Valid && linkedID.Valid {
		g.LinkedChat = &engine.PeerRef{Type: engine.PeerType(linkedType.String), ID: linkedID.Int64}
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT t.user_type,t.user_id,coalesce(nullif(p.title,''),trim(p.first_name||' '||p.last_name),''),coalesce(p.username,''),t.role,t.joined_at,t.admin_rights,t.banned_rights,t.banned_until,t.can_edit FROM participants t LEFT JOIN peers p ON p.account=t.account AND p.peer_type=t.user_type AND p.peer_id=t.user_id WHERE t.account=? AND t.group_type=? AND t.group_id=? ORDER BY t.user_type,t.user_id`, acc, ref.Type, ref.ID)
	if err != nil {
		return g, false, err
	}
	defer rows.Close()
	g.Participants = []engine.Participant{}
	for rows.Next() {
		var p engine.Participant
		var joined sql.NullInt64
		if err = rows.Scan(&p.Peer.Type, &p.Peer.ID, &p.Name, &p.Username, &p.Role, &joined, &p.AdminRights, &p.BannedRights, &p.BannedUntil, &p.CanEdit); err != nil {
			return g, false, err
		}
		p.JoinedAt = queryTime(joined.Int64, joined.Valid)
		g.Participants = append(g.Participants, p)
	}
	return g, true, rows.Err()
}

type ChatOptions struct {
	PeerOptions
	FolderID   *int
	Kind       engine.PeerKind
	UnreadOnly bool
}
type Chat struct {
	Peer         engine.Peer     `json:"peer"`
	Dialog       engine.Dialog   `json:"dialog"`
	LastMessage  *engine.Message `json:"last_message,omitempty"`
	AccessLostAt *time.Time      `json:"access_lost_at,omitempty"`
}

func (s *Store) ListChats(ctx context.Context, acc engine.AccountID, o ChatOptions) ([]Chat, error) {
	if err := s.checkAccount(acc); err != nil {
		return nil, err
	}
	n, err := queryLimit(o.Limit)
	if err != nil || o.Offset < 0 {
		return nil, engine.ErrInvalid
	}
	w, a := queryPeerFilter(o.Query)
	args := append([]any{acc}, a...)
	if o.FolderID != nil {
		if *o.FolderID < 0 {
			return nil, engine.ErrInvalid
		}
		w += " AND coalesce(d.folder_id,0)=?"
		args = append(args, *o.FolderID)
	}
	if o.Kind != "" {
		w += " AND p.kind=?"
		args = append(args, o.Kind)
	}
	if o.UnreadOnly {
		w += " AND (d.unread_count>0 OR d.unread_mark=1)"
	}
	args = append(args, s.now().Unix(), n, o.Offset)
	rows, err := s.reader.QueryContext(ctx, `SELECT p.peer_type,p.peer_id,coalesce(d.folder_id,0),coalesce(d.pinned,0),coalesce(d.top_message_id,0),coalesce(d.read_inbox_max_id,0),coalesce(d.read_outbox_max_id,0),coalesce(d.unread_count,0),coalesce(d.unread_mentions,0),coalesce(d.unread_reactions,0),coalesce(d.unread_mark,0),d.mute_until,coalesce(d.ttl_period,0),CASE WHEN p.access<>'ok' THEN p.access_changed_at END
 FROM peers p LEFT JOIN dialogs d ON p.account=d.account AND p.peer_type=d.peer_type AND p.peer_id=d.peer_id WHERE p.account=?
 AND p.migrated_to_id IS NULL
 AND (d.peer_id IS NOT NULL OR p.peer_type IN ('chat','channel') OR EXISTS (SELECT 1 FROM messages seen WHERE seen.account=p.account AND seen.peer_type=p.peer_type AND seen.peer_id=p.peer_id))`+w+`
 ORDER BY coalesce(d.pinned,0) DESC,coalesce(d.pin_order,0),(SELECT max(m.date) FROM messages m WHERE m.account=p.account AND m.peer_type=p.peer_type AND m.peer_id=p.peer_id AND (m.expires_at IS NULL OR m.expires_at>?)) DESC,p.peer_type,p.peer_id LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	out := []Chat{}
	for rows.Next() {
		var c Chat
		var mute, lost sql.NullInt64
		d := &c.Dialog
		if err = rows.Scan(&d.Peer.Type, &d.Peer.ID, &d.FolderID, &d.Pinned, &d.TopMessageID, &d.ReadInboxMaxID, &d.ReadOutboxMaxID, &d.UnreadCount, &d.UnreadMentions, &d.UnreadReactions, &d.UnreadMark, &mute, &d.TTLPeriod, &lost); err != nil {
			rows.Close()
			return nil, err
		}
		d.MuteUntil = queryTime(mute.Int64, mute.Valid)
		c.AccessLostAt = queryTime(lost.Int64, lost.Valid)
		out = append(out, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		c := &out[i]
		c.Peer, _, err = s.queryLookupPeer(ctx, acc, c.Dialog.Peer)
		if err != nil {
			return nil, err
		}
		no := false
		page, e := s.ListMessages(ctx, acc, c.Dialog.Peer, MessageOptions{Limit: 1, IncludeMigrated: &no})
		if e != nil {
			return nil, e
		}
		if len(page.Messages) > 0 {
			c.LastMessage = &page.Messages[0]
		}
	}
	return out, nil
}

type UnreadOptions struct {
	MessageOptions
	Peer        *engine.PeerRef
	IncludeBots bool // Include known bot user conversations; false by default.
}

func (s *Store) ListUnread(ctx context.Context, acc engine.AccountID, o UnreadOptions) (MessagePage, error) {
	if err := s.checkAccount(acc); err != nil {
		return MessagePage{}, err
	}
	w, a, err := queryMessageWhere(acc, s.now(), o.MessageOptions)
	if err != nil {
		return MessagePage{}, err
	}
	w += ` AND m.out=0 AND EXISTS (SELECT 1 FROM dialogs d WHERE d.account=m.account AND d.peer_type=m.peer_type AND d.peer_id=m.peer_id AND m.msg_id>d.read_inbox_max_id)`
	if !o.IncludeBots {
		// Correlate the conversation peer, not the sender/participants, and
		// keep unknown metadata eligible. Apply before the keyset page limit.
		w += ` AND NOT EXISTS (SELECT 1 FROM peers p WHERE p.account=m.account AND p.peer_type=m.peer_type AND p.peer_id=m.peer_id AND p.peer_type='user' AND p.kind='bot')`
	}
	if o.Peer != nil {
		peers, e := s.queryHistoryPeers(ctx, acc, *o.Peer, o.IncludeMigrated)
		if e != nil {
			return MessagePage{}, e
		}
		pw, pa := queryPeerWhere(peers)
		w += " AND " + pw
		a = append(a, pa...)
	}
	n, _ := queryLimit(o.Limit)
	return s.queryMessages(ctx, w, a, n)
}

type MentionOptions struct {
	MessageOptions
	Peer         *engine.PeerRef
	Answered     *bool
	ServerUnread *bool
}
type Mention struct {
	engine.Message
	ByName       bool               `json:"by_name"`
	ByUsername   bool               `json:"by_username"`
	ByReply      bool               `json:"by_reply"`
	ByFlag       bool               `json:"by_flag"`
	AnsweredAt   *time.Time         `json:"answered_at,omitempty"`
	AnsweredBy   *engine.MessageKey `json:"answered_by,omitempty"`
	ServerUnread bool               `json:"server_unread"`
}
type MentionPage struct {
	Mentions []Mention        `json:"mentions"`
	Next     *MessagePosition `json:"next,omitempty"`
}

func (s *Store) ListMentions(ctx context.Context, acc engine.AccountID, o MentionOptions) (MentionPage, error) {
	out := MentionPage{Mentions: []Mention{}}
	if err := s.checkAccount(acc); err != nil {
		return out, err
	}
	w, a, err := queryMessageWhere(acc, s.now(), o.MessageOptions)
	if err != nil {
		return out, err
	}
	mw := `x.row_id=m.row_id AND x.account=m.account`
	if o.Answered != nil {
		if *o.Answered {
			mw += " AND x.answered_at IS NOT NULL"
		} else {
			mw += " AND x.answered_at IS NULL"
		}
	}
	if o.ServerUnread != nil {
		mw += " AND x.server_unread=?"
		a = append(a, *o.ServerUnread)
	}
	w += " AND EXISTS (SELECT 1 FROM mentions x WHERE " + mw + ")"
	if o.Peer != nil {
		peers, e := s.queryHistoryPeers(ctx, acc, *o.Peer, o.IncludeMigrated)
		if e != nil {
			return out, e
		}
		pw, pa := queryPeerWhere(peers)
		w += " AND " + pw
		a = append(a, pa...)
	}
	n, _ := queryLimit(o.Limit)
	page, err := s.queryMessages(ctx, w, a, n)
	if err != nil {
		return out, err
	}
	out.Next = page.Next
	for _, m := range page.Messages {
		v := Mention{Message: m}
		var at, by sql.NullInt64
		err = s.reader.QueryRowContext(ctx, `SELECT x.by_name,x.by_username,x.by_reply,x.by_flag,x.answered_at,x.answered_by,x.server_unread FROM mentions x JOIN messages m ON m.row_id=x.row_id WHERE m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=? AND (m.expires_at IS NULL OR m.expires_at>?)`, acc, m.Key.Peer.Type, m.Key.Peer.ID, m.Key.ID, s.now().Unix()).Scan(&v.ByName, &v.ByUsername, &v.ByReply, &v.ByFlag, &at, &by, &v.ServerUnread)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return out, err
		}
		v.AnsweredAt = queryTime(at.Int64, at.Valid)
		if by.Valid {
			key := m.Key
			key.ID = int(by.Int64)
			v.AnsweredBy = &key
		}
		out.Mentions = append(out.Mentions, v)
	}
	return out, nil
}
