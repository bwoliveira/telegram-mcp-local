package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func timeFromUnix(v int64) time.Time { return time.Unix(v, 0).UTC() }
func saveCursor(ctx context.Context, tx *sql.Tx, a engine.AccountID, c engine.Cursor, now int64) error {
	if !validPeer(c.Peer) || c.OldestID < 0 || c.NewestID < 0 || c.Fetched < 0 {
		return engine.ErrInvalid
	}
	var access string
	if e := tx.QueryRowContext(ctx, `SELECT access FROM peers WHERE account=? AND peer_type=? AND peer_id=?`, a, c.Peer.Type, c.Peer.ID).Scan(&access); e != nil && e != sql.ErrNoRows {
		return e
	}
	if access != "" && access != "ok" {
		c.State = "no_access"
		c.FloodUntil = nil
	}
	same, err := cursorUnchanged(ctx, tx, a, c)
	if err != nil {
		return err
	}
	if same {
		return nil
	}
	state := c.State
	if state == "" {
		state = "idle"
	}
	if c.Complete && (state == "running" || state == "flood_wait") {
		state = "idle"
		c.FloodUntil = nil
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO backfill_cursors(account,peer_type,peer_id,newest_id,oldest_id,oldest_date,complete,state,flood_until,last_error,fetched,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(account,peer_type,peer_id) DO UPDATE SET newest_id=max(backfill_cursors.newest_id,excluded.newest_id),
 oldest_id=CASE WHEN excluded.oldest_id>0 AND (backfill_cursors.oldest_id=0 OR excluded.oldest_id<backfill_cursors.oldest_id) THEN excluded.oldest_id ELSE backfill_cursors.oldest_id END,
 oldest_date=CASE WHEN excluded.oldest_id>0 AND (backfill_cursors.oldest_id=0 OR excluded.oldest_id<backfill_cursors.oldest_id) THEN excluded.oldest_date ELSE backfill_cursors.oldest_date END,
 complete=max(backfill_cursors.complete,excluded.complete),state=CASE WHEN ?='' THEN backfill_cursors.state ELSE excluded.state END,
 flood_until=CASE WHEN ?='' THEN backfill_cursors.flood_until ELSE excluded.flood_until END,last_error=CASE WHEN ?='' THEN backfill_cursors.last_error ELSE excluded.last_error END,
 fetched=max(backfill_cursors.fetched,excluded.fetched),updated_at=excluded.updated_at`, a, c.Peer.Type, c.Peer.ID, c.NewestID, c.OldestID, nullableTime(c.OldestDate), c.Complete, state, nullableTime(c.FloodUntil), c.LastError, c.Fetched, now, c.State, c.State, c.State)
	return err
}
func canonicalCounts(v []engine.ReactionCount) []engine.ReactionCount {
	v = append([]engine.ReactionCount{}, v...)
	sort.Slice(v, func(i, j int) bool { return encode(v[i]) < encode(v[j]) })
	return v
}
func canonicalRecent(v []engine.PeerReaction) []engine.PeerReaction {
	v = append([]engine.PeerReaction{}, v...)
	sort.Slice(v, func(i, j int) bool { return encode(v[i]) < encode(v[j]) })
	return v
}
func applyReaction(ctx context.Context, tx *sql.Tx, a engine.AccountID, r engine.ReactionUpdate, now int64, ap *engine.Applied) error {
	if err := validKey(a, r.Key); err != nil {
		return err
	}
	var row int64
	err := tx.QueryRowContext(ctx, `SELECT row_id FROM messages WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=? AND (expires_at IS NULL OR expires_at>?)`, a, r.Key.Peer.Type, r.Key.Peer.ID, r.Key.ID, now).Scan(&row)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var counts, recent string
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT counts_json,coalesce(recent_json,'[]'),version FROM reactions WHERE row_id=?`, row).Scan(&counts, &recent, &version)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == sql.ErrNoRows {
		counts, recent = "[]", "[]"
	}
	if r.Recent == nil && recent != "" {
		if err := json.Unmarshal([]byte(recent), &r.Recent); err != nil {
			return err
		}
	}
	if r.Min && counts != "" {
		var previous []engine.ReactionCount
		if err := json.Unmarshal([]byte(counts), &previous); err != nil {
			return err
		}
		// A min snapshot may omit entire emoji/count and recent-peer rows.
		// Merge positive observations without treating absence/zero as removal
		// or mutating the caller's slices. Chosen is omitted in min results.
		merged := append([]engine.ReactionCount{}, r.Counts...)
		for _, old := range previous {
			found := false
			for i := range merged {
				if old.Emoji == merged[i].Emoji {
					merged[i].Chosen = old.Chosen
					if merged[i].Count == 0 {
						merged[i].Count = old.Count
					}
					found = true
				}
			}
			if !found {
				merged = append(merged, old)
			}
		}
		r.Counts = merged
		var oldRecent []engine.PeerReaction
		if err := json.Unmarshal([]byte(recent), &oldRecent); err != nil {
			return err
		}
		mergedRecent := append([]engine.PeerReaction{}, r.Recent...)
		for _, old := range oldRecent {
			found := false
			for _, current := range mergedRecent {
				if current.By == old.By && current.Emoji == old.Emoji {
					found = true
					break
				}
			}
			if !found {
				mergedRecent = append(mergedRecent, old)
			}
		}
		r.Recent = mergedRecent
	}
	active := map[string]bool{}
	for _, c := range r.Counts {
		if c.Count < 0 || c.Emoji == "" {
			return engine.ErrInvalid
		}
		if c.Count > 0 {
			active[c.Emoji] = true
		}
	}
	filtered := make([]engine.PeerReaction, 0, len(r.Recent))
	for _, v := range r.Recent {
		if active[v.Emoji] {
			filtered = append(filtered, v)
		}
	}
	r.Recent = filtered
	r.Counts = canonicalCounts(r.Counts)
	r.Recent = canonicalRecent(r.Recent)
	nc, nr := encode(r.Counts), encode(r.Recent)
	if counts == nc && recent == nr {
		return nil
	}
	sum := sha256.Sum256([]byte(nc))
	// Assign each transition its own revision inside this transaction, including
	// hydration and multiple changes to the same message in a single batch.
	version++
	_, err = tx.ExecContext(ctx, `INSERT INTO reactions(row_id,counts_json,recent_json,counts_hash,updated_at,version) VALUES(?,?,?,?,?,?) ON CONFLICT(row_id) DO UPDATE SET counts_json=excluded.counts_json,recent_json=excluded.recent_json,counts_hash=excluded.counts_hash,updated_at=excluded.updated_at,version=excluded.version`, row, nc, nr, hex.EncodeToString(sum[:]), now, version)
	if err != nil {
		return err
	}
	var previous []engine.PeerReaction
	if recent != "" {
		if err = json.Unmarshal([]byte(recent), &previous); err != nil {
			return err
		}
	}
	ev := engine.ReactionEvent{Key: r.Key, Counts: r.Counts, Version: version}
	contains := func(hay []engine.PeerReaction, n engine.PeerReaction) bool {
		for _, v := range hay {
			if encode(v) == encode(n) {
				return true
			}
		}
		return false
	}
	for _, v := range r.Recent {
		if !contains(previous, v) {
			ev.Added = append(ev.Added, v)
		}
	}
	for _, v := range previous {
		if !contains(r.Recent, v) {
			ev.Removed = append(ev.Removed, v)
		}
	}
	ap.Reactions = append(ap.Reactions, ev)
	return nil
}
func applyRead(ctx context.Context, tx *sql.Tx, a engine.AccountID, r engine.ReadUpdate, now int64, ap *engine.Applied) error {
	if !validPeer(r.Peer) || r.MaxID < 0 || r.StillUnread < 0 {
		return engine.ErrInvalid
	}
	if err := ensurePeer(ctx, tx, a, r.Peer, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO dialogs(account,peer_type,peer_id,refreshed_at) VALUES(?,?,?,?)`, a, r.Peer.Type, r.Peer.ID, now); err != nil {
		return err
	}
	col := "read_inbox_max_id"
	if r.Outbox {
		col = "read_outbox_max_id"
	}
	var old int
	if err := tx.QueryRowContext(ctx, `SELECT `+col+` FROM dialogs WHERE account=? AND peer_type=? AND peer_id=?`, a, r.Peer.Type, r.Peer.ID).Scan(&old); err != nil {
		return err
	}
	if r.MaxID <= old {
		return nil
	}
	r.MessageIDs = nil
	if r.Outbox {
		rows, err := tx.QueryContext(ctx, `SELECT msg_id FROM messages WHERE account=? AND peer_type=? AND peer_id=? AND out=1 AND msg_id>? AND msg_id<=? AND (expires_at IS NULL OR expires_at>?) ORDER BY msg_id LIMIT 100`, a, r.Peer.Type, r.Peer.ID, old, r.MaxID, now)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			r.MessageIDs = append(r.MessageIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	extra := ""
	args := []any{r.MaxID}
	if !r.Outbox {
		extra = ",unread_count=?,unread_mark=0"
		args = append(args, r.StillUnread)
	}
	args = append(args, a, r.Peer.Type, r.Peer.ID)
	if _, err := tx.ExecContext(ctx, `UPDATE dialogs SET `+col+`=?`+extra+` WHERE account=? AND peer_type=? AND peer_id=?`, args...); err != nil {
		return err
	}
	ap.Reads = append(ap.Reads, r)
	return nil
}
func applyParticipants(ctx context.Context, tx *sql.Tx, a engine.AccountID, p engine.ParticipantsSnapshot, now int64) error {
	if err := ensurePeer(ctx, tx, a, p.Group, now); err != nil {
		return err
	}
	if p.Group.Type == engine.PeerUser {
		return engine.ErrInvalid
	}
	var at int64
	err := tx.QueryRowContext(ctx, `SELECT snapshot_at FROM groups_full WHERE account=? AND peer_type=? AND peer_id=?`, a, p.Group.Type, p.Group.ID).Scan(&at)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && at > p.At.Unix() {
		return nil
	}
	if err == nil && at == p.At.Unix() {
		same, e := participantsUnchanged(ctx, tx, a, p)
		if e != nil {
			return e
		}
		if same {
			return nil
		}
	}
	var owner, linkedType, linkedID any
	if p.Owner != nil {
		owner = p.Owner.ID
	}
	if p.LinkedChat != nil {
		linkedType, linkedID = p.LinkedChat.Type, p.LinkedChat.ID
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO groups_full(account,peer_type,peer_id,about,owner_id,created_at,linked_type,linked_id,size,partial,am_admin,snapshot_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account,peer_type,peer_id) DO UPDATE SET about=excluded.about,owner_id=excluded.owner_id,created_at=excluded.created_at,linked_type=excluded.linked_type,linked_id=excluded.linked_id,size=excluded.size,partial=excluded.partial,am_admin=excluded.am_admin,snapshot_at=excluded.snapshot_at`, a, p.Group.Type, p.Group.ID, p.About, owner, nullableTime(p.CreatedAt), linkedType, linkedID, p.Size, p.Partial, p.AmAdmin, p.At.Unix()); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM participants WHERE account=? AND group_type=? AND group_id=?`, a, p.Group.Type, p.Group.ID); err != nil {
		return err
	}
	return insertParticipantMembers(ctx, tx, a, p, now)
}
func applyGap(ctx context.Context, tx *sql.Tx, a engine.AccountID, g engine.GapMark, fill bool, now int64, ap *engine.Applied) error {
	var pt, pi any
	if g.Peer != nil {
		if !validPeer(*g.Peer) {
			return engine.ErrInvalid
		}
		pt = g.Peer.Type
		pi = g.Peer.ID
	}
	if g.Detected.IsZero() {
		g.Detected = timeFromUnix(now)
	}
	if fill {
		// state_lost can only be closed by SaveDifferenceCheckpoint, which
		// validates the original anchor chain and commits its checkpoint atomically.
		if g.Reason == engine.GapStateLost {
			return nil
		}
		filled := now
		if g.Filled != nil {
			filled = g.Filled.Unix()
		}
		// FilledGaps is explicit proof for this reason and scope through Detected.
		// Close older duplicate callbacks too, but never newer observations or
		// ranges outside a bounded proof (zero bounds mean scope-wide proof).
		r, err := tx.ExecContext(ctx, `UPDATE gaps SET filled_at=? WHERE account=? AND peer_type IS ? AND peer_id IS ? AND reason=? AND detected_at<=? AND filled_at IS NULL
		 AND (?=0 OR (coalesce(from_id,0)>0 AND from_id>=?))
		 AND (?=0 OR (coalesce(to_id,0)>0 AND to_id<=?))`, filled, a, pt, pi, g.Reason, g.Detected.Unix(), g.FromID, g.FromID, g.ToID, g.ToID)
		if err != nil {
			return err
		}
		n, _ := r.RowsAffected()
		if n > 0 {
			g.Filled = queryTime(filled, true)
			ap.Gaps = append(ap.Gaps, g)
		}
		return nil
	}
	if g.Reason == engine.GapAccessLost {
		if refreshed, err := refreshAccessLoss(ctx, tx, a, g); err != nil || refreshed {
			return err
		}
	}
	var exists bool
	// Wall-clock callback timestamps are not gap identities. Keep one pending
	// warning when existing evidence already covers this reason/scope/range.
	// An exact replay of filled evidence must not reopen the same detection.
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM gaps WHERE account=? AND peer_type IS ? AND peer_id IS ? AND reason=?
	 AND (filled_at IS NULL OR detected_at=?)
	 AND (coalesce(from_id,0)=0 OR (?<>0 AND from_id<=?))
	 AND (coalesce(to_id,0)=0 OR (?<>0 AND to_id>=?)))`, a, pt, pi, g.Reason, g.Detected.Unix(), g.FromID, g.FromID, g.ToID, g.ToID).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	return insertGap(ctx, tx, a, g, ap)
}

// insertGap refreshes terminal access-loss summaries instead of journaling
// every observed access-state transition. Other causes retain their gap identity.
func insertGap(ctx context.Context, tx *sql.Tx, a engine.AccountID, g engine.GapMark, ap *engine.Applied) error {
	if g.Reason == engine.GapAccessLost {
		if refreshed, err := refreshAccessLoss(ctx, tx, a, g); err != nil || refreshed {
			return err
		}
	}
	var pt, pi any
	if g.Peer != nil {
		pt, pi = g.Peer.Type, g.Peer.ID
	}
	var anchor any
	if g.Reason == engine.GapStateLost && g.RecoveryAnchor != nil {
		// The callback's claimed baseline must still be the durable checkpoint
		// in THIS transaction; never trust a stale or invented caller value.
		var st engine.UpdatesState
		var err error
		if g.Peer == nil {
			err = tx.QueryRowContext(ctx, `SELECT pts,qts,date,seq FROM updates_state WHERE account=?`, a).Scan(&st.Pts, &st.Qts, &st.Date, &st.Seq)
		} else if g.Peer.Type == engine.PeerChannel {
			err = tx.QueryRowContext(ctx, `SELECT pts FROM channel_state WHERE account=? AND channel_id=?`, a, g.Peer.ID).Scan(&st.Pts)
		} else {
			err = sql.ErrNoRows
		}
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil && st == *g.RecoveryAnchor {
			anchor = encode(st)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO gaps(account,peer_type,peer_id,reason,from_id,to_id,detected_at,filled_at,recovery_anchor,recovery_cursor) VALUES(?,?,?,?,?,?,?,?,?,?)`, a, pt, pi, g.Reason, g.FromID, g.ToID, g.Detected.Unix(), nullableTime(g.Filled), anchor, anchor); err != nil {
		return err
	}
	ap.Gaps = append(ap.Gaps, g)
	return nil
}
func applyAccess(ctx context.Context, tx *sql.Tx, a engine.AccountID, v engine.AccessChange, now int64, ap *engine.Applied) error {
	if err := ensurePeer(ctx, tx, a, v.Peer, now); err != nil {
		return err
	}
	var old string
	if err := tx.QueryRowContext(ctx, `SELECT access FROM peers WHERE account=? AND peer_type=? AND peer_id=?`, a, v.Peer.Type, v.Peer.ID).Scan(&old); err != nil {
		return err
	}
	if old == string(v.State) {
		if v.State != engine.AccessOK {
			_, err := refreshAccessLoss(ctx, tx, a, engine.GapMark{Peer: &v.Peer, Detected: timeFromUnix(now)})
			return err
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE peers SET access=?,access_reason=?,access_changed_at=?,updated_at=? WHERE account=? AND peer_type=? AND peer_id=?`, v.State, v.Reason, now, now, a, v.Peer.Type, v.Peer.ID); err != nil {
		return err
	}
	ap.Access = append(ap.Access, v)
	if v.State != engine.AccessOK {
		if v.Peer.Type == engine.PeerChannel {
			if _, err := tx.ExecContext(ctx, `DELETE FROM channel_state WHERE account=? AND channel_id=?`, a, v.Peer.ID); err != nil {
				return err
			}
		}
		if err := saveCursor(ctx, tx, a, engine.Cursor{Peer: v.Peer, State: "no_access"}, now); err != nil {
			return err
		}
		return insertGap(ctx, tx, a, engine.GapMark{Peer: &v.Peer, Reason: engine.GapAccessLost, Detected: timeFromUnix(now)}, ap)
	}
	return saveCursor(ctx, tx, a, engine.Cursor{Peer: v.Peer, State: "idle"}, now)
}
func (s *Store) applyDownload(ctx context.Context, tx *sql.Tx, d engine.DownloadRecord, now int64) error {
	if err := validKey(s.account, d.Key); err != nil {
		return err
	}
	path := d.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(s.dir, path)
	}
	var err error
	if !purgeBelow(s.dir, path) {
		if !purgeBelow(s.mediaRoot(), path) {
			return engine.ErrInvalid
		}
		if err := purgeValidateFile(s.mediaRoot(), path, nil); err != nil {
			return err
		}
	}
	var row int64
	var ttl int
	var protected bool
	var access string
	var expiry sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT m.row_id,m.media_ttl,(m.protected OR p.protected),m.expires_at,p.access FROM messages m JOIN peers p ON p.account=m.account AND p.peer_type=m.peer_type AND p.peer_id=m.peer_id WHERE m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=?`, d.Key.Account, d.Key.Peer.Type, d.Key.Peer.ID, d.Key.ID).Scan(&row, &ttl, &protected, &expiry, &access)
	if errors.Is(err, sql.ErrNoRows) {
		return engine.ErrMessageNotFound
	}
	if err != nil {
		return err
	}
	if ttl > 0 || protected || (expiry.Valid && expiry.Int64 <= now) {
		return engine.ErrProtectedContent
	}
	if access != "ok" {
		return engine.ErrNoAccess
	}
	var old string
	err = tx.QueryRowContext(ctx, `SELECT path FROM media_files WHERE row_id=? AND variant='full'`, row).Scan(&old)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if old != "" && old != path {
		if _, err = tx.ExecContext(ctx, `DELETE FROM media_files WHERE row_id=? AND variant='full'`, row); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO media_files(row_id,variant,path,bytes,sha256,downloaded_at) VALUES(?,'full',?,?,?,?) ON CONFLICT(row_id,variant) DO UPDATE SET path=excluded.path,bytes=excluded.bytes,sha256=excluded.sha256,downloaded_at=excluded.downloaded_at
 WHERE media_files.path<>excluded.path OR media_files.bytes<>excluded.bytes OR media_files.sha256 IS NOT excluded.sha256 OR media_files.downloaded_at<>excluded.downloaded_at`, row, path, d.Bytes, d.SHA256, d.At.Unix())
	return err
}

// indexMentions matches entity offsets in UTF-16 and derives answers from our
// outgoing messages, not from Telegram's independent mention-unread flag.
func indexMentions(ctx context.Context, tx *sql.Tx, a engine.AccountID, now int64) error {
	var self int64
	var username string
	if err := tx.QueryRowContext(ctx, `SELECT self_id,username FROM accounts WHERE account=?`, a).Scan(&self, &username); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT row_id,peer_type,peer_id,msg_id,topic_id,date,text,entities_json,mentioned,media_unread,reply_to_msg_id,reply_to_peer_type,reply_to_peer_id FROM messages WHERE account=? AND out=0 AND (expires_at IS NULL OR expires_at>?)`, a, now)
	if err != nil {
		return err
	}
	type item struct {
		row, peer, id, topic, date int64
		pt                         string
		text, entities             string
		flag, unread               bool
		reply, rpeer               sql.NullInt64
		rpt                        sql.NullString
	}
	var items []item
	for rows.Next() {
		var v item
		var e sql.NullString
		if err = rows.Scan(&v.row, &v.pt, &v.peer, &v.id, &v.topic, &v.date, &v.text, &e, &v.flag, &v.unread, &v.reply, &v.rpt, &v.rpeer); err != nil {
			rows.Close()
			return err
		}
		v.entities = e.String
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, v := range items {
		var ents []engine.Entity
		if v.entities != "" {
			if err = json.Unmarshal([]byte(v.entities), &ents); err != nil {
				return err
			}
		}
		byName, byUser, byReply := false, false, false
		u := utf16.Encode([]rune(v.text))
		for _, e := range ents {
			if e.Type == engine.EntityMentionName && self > 0 && e.UserID == self {
				byName = true
			}
			if e.Type == engine.EntityMention && username != "" && e.Offset >= 0 && e.Length > 0 && e.Offset <= len(u) && e.Length <= len(u)-e.Offset {
				part := string(utf16.Decode(u[e.Offset : e.Offset+e.Length]))
				if strings.EqualFold(strings.TrimPrefix(part, "@"), username) {
					byUser = true
				}
			}
		}
		if v.reply.Valid {
			pt, pi := v.pt, v.peer
			if v.rpt.Valid && v.rpeer.Valid {
				pt = v.rpt.String
				pi = v.rpeer.Int64
			}
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM messages WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=? AND out=1 AND (expires_at IS NULL OR expires_at>?))`, a, pt, pi, v.reply.Int64, now).Scan(&byReply); err != nil {
				return err
			}
		}
		if !byName && !byUser && !byReply && !v.flag {
			if _, err = tx.ExecContext(ctx, `DELETE FROM mentions WHERE row_id=?`, v.row); err != nil {
				return err
			}
			continue
		}
		var answer, date sql.NullInt64
		err = tx.QueryRowContext(ctx, `SELECT msg_id,date FROM messages WHERE account=? AND peer_type=? AND peer_id=? AND out=1 AND (expires_at IS NULL OR expires_at>?) AND (date,msg_id)>(?,?) AND topic_id=? ORDER BY date,msg_id LIMIT 1`, a, v.pt, v.peer, now, v.date, v.id, v.topic).Scan(&answer, &date)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO mentions(row_id,account,peer_type,peer_id,msg_id,topic_id,date,by_name,by_username,by_reply,by_flag,answered_at,answered_by,server_unread) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(row_id) DO UPDATE SET topic_id=excluded.topic_id,by_name=excluded.by_name,by_username=excluded.by_username,by_reply=excluded.by_reply,by_flag=excluded.by_flag,answered_at=excluded.answered_at,answered_by=excluded.answered_by,server_unread=excluded.server_unread
    WHERE mentions.topic_id<>excluded.topic_id OR mentions.by_name<>excluded.by_name OR mentions.by_username<>excluded.by_username OR mentions.by_reply<>excluded.by_reply OR mentions.by_flag<>excluded.by_flag OR mentions.answered_at IS NOT excluded.answered_at OR mentions.answered_by IS NOT excluded.answered_by OR mentions.server_unread<>excluded.server_unread`, v.row, a, v.pt, v.peer, v.id, v.topic, v.date, byName, byUser, byReply, v.flag, date, answer, v.unread)
		if err != nil {
			return err
		}
	}
	return nil
}
