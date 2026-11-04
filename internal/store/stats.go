package store

import (
	"context"
	"sort"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// StatsFilter narrows local statistics and exports. Peer selects precisely one
// typed peer (no migration expansion). Direction must already be normalized to
// "in", "out", or "". MediaType accepts "text", "any", an exact type, or "".
// Since is inclusive and Until exclusive, at the store's Unix-second precision.
type StatsFilter struct {
	Peer                 *engine.PeerRef
	Since, Until         time.Time
	Direction, MediaType string
	ExcludeGroups        bool
}

type StatsBucket struct {
	Key   string    `json:"key"`
	Name  string    `json:"name,omitempty"`
	Count int64     `json:"count"`
	First time.Time `json:"first"`
	Last  time.Time `json:"last"`
}

type StatsResult struct {
	Buckets []StatsBucket `json:"buckets"`
	Total   int64         `json:"total"`
	Groups  int           `json:"groups"`
}

// Integer-second message timestamps compare exactly to fractional RFC3339
// bounds when both inclusive-since and exclusive-until use the ceiling second.
func statsBoundSecond(t time.Time) int64 {
	n := t.Unix()
	if t.Nanosecond() != 0 {
		n++
	}
	return n
}

func statsWhere(account engine.AccountID, now time.Time, f StatsFilter) (string, []any, error) {
	if f.Peer != nil && !validPeer(*f.Peer) {
		return "", nil, engine.ErrInvalid
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && !f.Since.Before(f.Until) {
		return "", nil, engine.ErrInvalid
	}
	w := localVisibleMessage
	a := []any{account, now.Unix(), now.Unix()}
	if f.Peer != nil {
		w += " AND m.peer_type=? AND m.peer_id=?"
		a = append(a, f.Peer.Type, f.Peer.ID)
	}
	if !f.Since.IsZero() {
		w += " AND m.date>=?"
		a = append(a, statsBoundSecond(f.Since))
	}
	if !f.Until.IsZero() {
		w += " AND m.date<?"
		a = append(a, statsBoundSecond(f.Until))
	}
	switch f.Direction {
	case "":
	case "in":
		w += " AND m.out=0"
	case "out":
		w += " AND m.out=1"
	default:
		return "", nil, engine.ErrInvalid
	}
	switch f.MediaType {
	case "":
	case "text":
		w += " AND coalesce(m.media_type,'')=''"
	case "any":
		w += " AND coalesce(m.media_type,'')<>''"
	default:
		w += " AND m.media_type=?"
		a = append(a, f.MediaType)
	}
	if f.ExcludeGroups {
		w += " AND m.peer_type='user'"
	}
	return w, a, nil
}

// StatsMessages aggregates visible local messages, never decoding message bodies
// or contacting Telegram. Totals and Groups describe the entire filtered set,
// before limit. Empty groupBy means chat; limit 0 means 50 (valid range 1..1000).
// Chats/senders sort by descending count then key; calendars by newest key.
// First and Last are UTC instants; calendar keys use Go time.Local, including
// historical timezone rules rather than SQLite/libc's unrelated localtime.
func (s *Store) StatsMessages(ctx context.Context, account engine.AccountID, f StatsFilter, groupBy string, limit int) (StatsResult, error) {
	out := StatsResult{Buckets: []StatsBucket{}}
	if err := s.checkReadAccount(ctx, account); err != nil {
		return out, err
	}
	if limit < 0 || limit > 1000 {
		return out, engine.ErrInvalid
	}
	if limit == 0 {
		limit = 50
	}
	if groupBy == "" {
		groupBy = "chat"
	}
	if groupBy != "chat" && groupBy != "sender" && groupBy != "day" && groupBy != "month" {
		return out, engine.ErrInvalid
	}
	w, a, err := statsWhere(account, s.now(), f)
	if err != nil {
		return out, err
	}
	if groupBy == "day" || groupBy == "month" {
		return s.statsCalendar(ctx, w, a, groupBy, limit)
	}
	key := `m.peer_type||':'||m.peer_id`
	name := `coalesce((SELECT coalesce(nullif(p.title,''),nullif(trim(p.first_name||' '||p.last_name),''),'') FROM peers p WHERE p.account=m.account AND p.peer_type=m.peer_type AND p.peer_id=m.peer_id),'')`
	if groupBy == "sender" {
		key = `CASE WHEN m.out=1 THEN 'me' ELSE coalesce(m.sender_type||':'||m.sender_id,'') END`
		name = `CASE WHEN m.out=1 THEN 'the account itself' ELSE coalesce((SELECT coalesce(nullif(p.title,''),nullif(trim(p.first_name||' '||p.last_name),'')) FROM peers p WHERE p.account=m.account AND p.peer_type=m.sender_type AND p.peer_id=m.sender_id),m.sender_name,'') END`
	}
	// Window totals and top buckets come from the same statement/snapshot. Neither
	// total nor group cardinality is accidentally bounded by presentation limit.
	rows, err := s.reader.QueryContext(ctx, `WITH buckets AS (
 SELECT `+key+` AS k,max(`+name+`) AS name,count(*) AS n,min(m.date) AS first,max(m.date) AS last
 FROM messages m WHERE `+w+` GROUP BY k)
 SELECT k,name,n,first,last,sum(n) OVER (),count(*) OVER () FROM buckets ORDER BY n DESC,k LIMIT ?`, append(a, limit)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var b StatsBucket
		var first, last int64
		if err := rows.Scan(&b.Key, &b.Name, &b.Count, &first, &last, &out.Total, &out.Groups); err != nil {
			return out, err
		}
		b.First = time.Unix(first, 0).UTC()
		b.Last = time.Unix(last, 0).UTC()
		out.Buckets = append(out.Buckets, b)
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	return out, rows.Err()
}

func (s *Store) statsCalendar(ctx context.Context, where string, args []any, groupBy string, limit int) (StatsResult, error) {
	out := StatsResult{Buckets: []StatsBucket{}}
	// Aggregate equal Unix seconds in SQLite, then merge those small scalar rows
	// by Go's calendar. No message JSON/content is read or retained. Using SQL
	// strftime(...,'localtime') would not honor a runtime-assigned time.Local and
	// cannot reliably match Go's historical/DST timezone database.
	rows, err := s.reader.QueryContext(ctx, `SELECT m.date,count(*) FROM messages m WHERE `+where+` GROUP BY m.date`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	layout := "2006-01-02"
	if groupBy == "month" {
		layout = "2006-01"
	}
	loc := time.Local
	buckets := map[string]*StatsBucket{}
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		var date, count int64
		if err := rows.Scan(&date, &count); err != nil {
			return out, err
		}
		at := time.Unix(date, 0).UTC()
		key := at.In(loc).Format(layout)
		b := buckets[key]
		if b == nil {
			b = &StatsBucket{Key: key, First: at, Last: at}
			buckets[key] = b
		}
		b.Count += count
		if at.Before(b.First) {
			b.First = at
		}
		if at.After(b.Last) {
			b.Last = at
		}
		out.Total += count
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Groups = len(buckets)
	for _, b := range buckets {
		out.Buckets = append(out.Buckets, *b)
	}
	sort.Slice(out.Buckets, func(i, j int) bool { return out.Buckets[i].Key > out.Buckets[j].Key })
	if len(out.Buckets) > limit {
		out.Buckets = out.Buckets[:limit]
	}
	return out, nil
}
