package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"golang.org/x/text/unicode/norm"
	"unicode"
)

// VersionKey matches the engine event ID key across restarts.
func VersionKey(k engine.MessageKey) string {
	return fmt.Sprintf("%s/%s:%d/%d", k.Account, k.Peer.Type, k.Peer.ID, k.ID)
}

// Apply commits a complete update/page before returning event-worthy changes.
// No Applied is exposed on rollback. Checkpoint hold belongs to the engine.
func (s *Store) Apply(ctx context.Context, b engine.Batch) (engine.Applied, error) {
	var a engine.Applied
	if err := s.checkAccount(b.Account); err != nil {
		return a, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return a, err
	}
	defer tx.Rollback()
	now := s.now().Unix()
	run := func() error {
		for _, p := range b.Peers {
			// Access transitions must run their lifecycle, not just set a column.
			access := p.Peer.Access
			p.Peer.Access = ""
			if err := upsertPeer(ctx, tx, b.Account, p, now); err != nil {
				return err
			}
			if access != "" {
				if err := applyAccess(ctx, tx, b.Account, engine.AccessChange{Peer: p.Peer.Ref, State: access}, now, &a); err != nil {
					return err
				}
			}
		}
		for _, d := range b.Dialogs {
			if err := applyDialog(ctx, tx, b.Account, d, now); err != nil {
				return err
			}
		}
		for i, m := range append(append([]engine.Message{}, b.Messages...), b.Edits...) {
			if err := s.applyMessage(ctx, tx, b.Account, m, b.Source, i >= len(b.Messages), now, &a); err != nil {
				return err
			}
		}
		for _, d := range b.Deletes {
			if d.Peer != nil && !validPeer(*d.Peer) {
				return engine.ErrInvalid
			}
			for _, id := range d.IDs {
				if id <= 0 {
					return engine.ErrInvalid
				}
			}
			keys := []engine.MessageKey{}
			if d.Peer != nil {
				for _, id := range d.IDs {
					keys = append(keys, engine.MessageKey{Account: b.Account, Peer: *d.Peer, ID: id})
				}
			} else {
				var e error
				keys, e = resolveIDs(ctx, tx, b.Account, d.IDs)
				if e != nil {
					return e
				}
				known := map[int]bool{}
				for _, k := range keys {
					known[k.ID] = true
				}
				for _, id := range d.IDs {
					r, e := tx.ExecContext(ctx, `INSERT OR IGNORE INTO pending_deletes(account,msg_id,received_at) VALUES(?,?,?)`, b.Account, id, now)
					if e != nil {
						return e
					}
					n, e := r.RowsAffected()
					if e != nil {
						return e
					}
					if n > 0 && !known[id] {
						a.UnresolvedDeletes = append(a.UnresolvedDeletes, id)
					}
				}
			}
			for _, k := range keys {
				changed, e := deleteMessage(ctx, tx, k, "deleted", now)
				if e != nil {
					return e
				}
				if changed {
					a.Deleted = append(a.Deleted, k)
				}
			}
		}
		for _, r := range b.Reactions {
			if err := applyReaction(ctx, tx, b.Account, r, now, &a); err != nil {
				return err
			}
		}
		for _, p := range b.Polls {
			if err := applyPoll(ctx, tx, b.Account, p, now); err != nil {
				return err
			}
		}
		for _, r := range b.Reads {
			if err := applyRead(ctx, tx, b.Account, r, now, &a); err != nil {
				return err
			}
		}
		for _, p := range b.Participants {
			if err := applyParticipants(ctx, tx, b.Account, p, now); err != nil {
				return err
			}
		}
		for _, v := range b.Access {
			if err := applyAccess(ctx, tx, b.Account, v, now, &a); err != nil {
				return err
			}
		}
		for _, g := range b.Gaps {
			if err := applyGap(ctx, tx, b.Account, g, false, now, &a); err != nil {
				return err
			}
		}
		for _, g := range b.FilledGaps {
			if err := applyGap(ctx, tx, b.Account, g, true, now, &a); err != nil {
				return err
			}
		}
		if b.Cursor != nil {
			if err := saveCursor(ctx, tx, b.Account, *b.Cursor, now); err != nil {
				return err
			}
		}
		for _, d := range b.Downloads {
			if err := s.applyDownload(ctx, tx, d, now); err != nil {
				return err
			}
		}
		// A forum flag can arrive after headerless General messages. Repair
		// their metadata even when a duplicate message was skipped above.
		for _, p := range b.Peers {
			if p.Peer.Forum {
				if err := backfillForumTopics(ctx, tx); err != nil {
					return err
				}
				break
			}
		}
		// Recompute mentions after the whole batch: reply targets and outgoing
		// answers can arrive in either order in a catch-up/backfill page.
		if err := indexMentions(ctx, tx, b.Account, now); err != nil {
			return err
		}
		return s.fail("apply_before_commit")
	}
	if err = run(); err != nil {
		return engine.Applied{}, err
	}
	if err = tx.Commit(); err != nil {
		return engine.Applied{}, err
	}
	if err = s.fail("apply_after_commit"); err != nil {
		return engine.Applied{}, err
	}
	return a, nil
}
func ensurePeer(ctx context.Context, tx *sql.Tx, a engine.AccountID, p engine.PeerRef, now int64) error {
	if !validPeer(p) {
		return engine.ErrInvalid
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO peers(account,peer_type,peer_id,kind,updated_at) VALUES(?,?,?,?,?)`, a, p.Type, p.ID, defaultKind(p), now)
	return err
}
func normalize(s string) string {
	var b strings.Builder
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		if !unicode.Is(unicode.Mn, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
func upsertPeer(ctx context.Context, tx *sql.Tx, a engine.AccountID, u engine.PeerUpsert, now int64) error {
	p := u.Peer
	unchanged, err := peerUnchanged(ctx, tx, a, u)
	if err != nil {
		return err
	}
	if unchanged {
		return nil
	}
	if err := ensurePeer(ctx, tx, a, p.Ref, now); err != nil {
		return err
	}
	kind := p.Kind
	if kind == "" {
		kind = defaultKind(p.Ref)
	}
	var mt, mi any
	if p.MigratedTo != nil {
		if !validPeer(*p.MigratedTo) {
			return engine.ErrInvalid
		}
		mt = p.MigratedTo.Type
		mi = p.MigratedTo.ID
	}
	var hash any
	if u.AccessHash != 0 {
		hash = u.AccessHash
	}
	access := p.Access
	if access == "" {
		access = engine.AccessOK
	}
	_, err = tx.ExecContext(ctx, `UPDATE peers SET
 access_hash=CASE WHEN ? IS NOT NULL AND (access_hash IS NULL OR hash_min=1 OR ?=0) THEN ? ELSE access_hash END,
 hash_min=CASE WHEN ? IS NOT NULL AND (access_hash IS NULL OR hash_min=1 OR ?=0) THEN ? ELSE hash_min END,
 kind=CASE WHEN ?<>'' THEN ? ELSE kind END,
 title=CASE WHEN ?<>'' THEN ? ELSE title END,first_name=CASE WHEN ?<>'' THEN ? ELSE first_name END,
 last_name=CASE WHEN ?<>'' THEN ? ELSE last_name END,username=CASE WHEN ?<>'' THEN ? ELSE username END,
 phone=CASE WHEN ?<>'' THEN ? ELSE phone END,is_contact=max(is_contact,?),forum=max(forum,?),protected=max(protected,?),participants=max(participants,?),
 access=CASE WHEN ?<>'' THEN ? ELSE access END,migrated_to_type=coalesce(?,migrated_to_type),migrated_to_id=coalesce(?,migrated_to_id),updated_at=?
 WHERE account=? AND peer_type=? AND peer_id=?`, hash, u.HashMin, hash, hash, u.HashMin, u.HashMin, p.Kind, kind, p.Title, p.Title, p.FirstName, p.FirstName, p.LastName, p.LastName, p.Username, p.Username, p.Phone, p.Phone, p.IsContact, p.Forum, p.Protected, p.Participants, p.Access, access, mt, mi, now, a, p.Ref.Type, p.Ref.ID)
	if err != nil {
		return err
	}
	var title, first, last, username, phone string
	if err = tx.QueryRowContext(ctx, `SELECT title,first_name,last_name,username,phone FROM peers WHERE account=? AND peer_type=? AND peer_id=?`, a, p.Ref.Type, p.Ref.ID).Scan(&title, &first, &last, &username, &phone); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE peers SET search_name=? WHERE account=? AND peer_type=? AND peer_id=?`, normalize(strings.Join([]string{title, first, last, username, phone}, " ")), a, p.Ref.Type, p.Ref.ID); err != nil {
		return err
	}
	for _, name := range append(append([]string{}, p.Usernames...), p.Username) {
		if name != "" {
			if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO peer_usernames(account,peer_type,peer_id,username) VALUES(?,?,?,?)`, a, p.Ref.Type, p.Ref.ID, strings.TrimPrefix(name, "@")); err != nil {
				return err
			}
		}
	}
	return nil
}
func applyDialog(ctx context.Context, tx *sql.Tx, a engine.AccountID, d engine.Dialog, now int64) error {
	same, err := dialogUnchanged(ctx, tx, a, d)
	if err != nil {
		return err
	}
	if same {
		return nil
	}
	if err := ensurePeer(ctx, tx, a, d.Peer, now); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO dialogs(account,peer_type,peer_id,folder_id,pinned,top_message_id,read_inbox_max_id,read_outbox_max_id,unread_count,unread_mentions,unread_reactions,unread_mark,mute_until,ttl_period,refreshed_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(account,peer_type,peer_id) DO UPDATE SET folder_id=excluded.folder_id,pinned=excluded.pinned,top_message_id=max(dialogs.top_message_id,excluded.top_message_id),
 read_inbox_max_id=max(dialogs.read_inbox_max_id,excluded.read_inbox_max_id),read_outbox_max_id=max(dialogs.read_outbox_max_id,excluded.read_outbox_max_id),unread_count=excluded.unread_count,unread_mentions=excluded.unread_mentions,unread_reactions=excluded.unread_reactions,unread_mark=excluded.unread_mark,mute_until=excluded.mute_until,ttl_period=excluded.ttl_period,refreshed_at=excluded.refreshed_at`, a, d.Peer.Type, d.Peer.ID, d.FolderID, d.Pinned, d.TopMessageID, d.ReadInboxMaxID, d.ReadOutboxMaxID, d.UnreadCount, d.UnreadMentions, d.UnreadReactions, d.UnreadMark, nullableTime(d.MuteUntil), d.TTLPeriod, now)
	return err
}
func deleteMessage(ctx context.Context, tx *sql.Tx, k engine.MessageKey, reason string, now int64) (bool, error) {
	r, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO message_tombstones(account,peer_type,peer_id,msg_id,reason,deleted_at) VALUES(?,?,?,?,?,?)`, k.Account, k.Peer.Type, k.Peer.ID, k.ID, reason, now)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return false, err
	}
	r, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=?`, k.Account, k.Peer.Type, k.Peer.ID, k.ID)
	if err != nil {
		return false, err
	}
	removed, err := r.RowsAffected()
	return n > 0 || removed > 0, err
}

// retainedMediaTTL preserves an acquired timer. Zero is unknown, never
// permission to clear or shorten the original policy on a partial copy.
func retainedMediaTTL(previous, incoming int) int {
	if previous > 0 {
		return previous
	}
	return incoming
}

func earliestExpiry(previous, incoming *time.Time) *time.Time {
	if previous != nil && (incoming == nil || previous.Before(*incoming)) {
		return previous
	}
	return incoming
}

// tightenMessageSecurityMetadata persists the already-merged security policy
// independently of content ordering. No edit marker, hash or version is changed;
// an exact replay performs no writes. Temporary content also loses derivatives.
func tightenMessageSecurityMetadata(ctx context.Context, tx *sql.Tx, a engine.AccountID, m engine.Message, now int64) error {
	k := m.Key
	mediaTTL := 0
	var mediaType, mediaJSON any
	if m.Media != nil && m.Media.TTLSeconds > 0 {
		mediaTTL = m.Media.TTLSeconds
		mediaType = m.Media.Type
		mediaJSON = encode(engine.Media{Type: m.Media.Type, TTLSeconds: mediaTTL})
	}
	if m.Protected || mediaTTL > 0 {
		for _, table := range []string{"media_files", "transcripts"} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE row_id=(SELECT row_id FROM messages WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=?)`, a, k.Peer.Type, k.Peer.ID, k.ID); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, `UPDATE messages SET protected=max(protected,?),ttl_period=?,expires_at=?,
 media_type=CASE WHEN ?>0 THEN ? ELSE media_type END,
 media_json=CASE WHEN ?>0 THEN ? WHEN ? AND json_valid(media_json) THEN json_set(media_json,'$.downloadable',json('false')) ELSE media_json END,
 content_identity=CASE WHEN media_ttl>0 OR ?>0 THEN '' ELSE content_identity END,
 media_remote=CASE WHEN ?>0 THEN NULL ELSE media_remote END,
 media_ttl=CASE WHEN ?>0 THEN ? ELSE media_ttl END,
 text=CASE WHEN ?>0 THEN '[temporary media]' ELSE text END,
 entities_json=CASE WHEN ?>0 THEN ? ELSE entities_json END,
 location_json=CASE WHEN ?>0 THEN ? ELSE location_json END,
 poll_json=CASE WHEN ?>0 THEN ? ELSE poll_json END,updated_at=?
 WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=? AND (
 protected<>max(protected,?) OR ttl_period<>? OR expires_at IS NOT ?
 OR (?>0 AND (media_type IS NOT ? OR media_json IS NOT ? OR media_ttl<>? OR media_remote IS NOT NULL OR text<>'[temporary media]' OR entities_json IS NOT ? OR location_json IS NOT ? OR poll_json IS NOT ?))
 OR (? AND CASE WHEN json_valid(media_json) THEN coalesce(json_extract(media_json,'$.downloadable'),0)<>0 ELSE 0 END))`,
		m.Protected, m.TTLPeriod, nullableTime(m.ExpiresAt),
		mediaTTL, mediaType, mediaTTL, mediaJSON, m.Protected, mediaTTL, mediaTTL, mediaTTL, mediaTTL,
		mediaTTL, mediaTTL, encode(nil), mediaTTL, encode(nil), mediaTTL, encode(nil), now,
		a, k.Peer.Type, k.Peer.ID, k.ID,
		m.Protected, m.TTLPeriod, nullableTime(m.ExpiresAt),
		mediaTTL, mediaType, mediaJSON, mediaTTL, encode(nil), encode(nil), encode(nil), m.Protected)
	return err
}

func (s *Store) applyMessage(ctx context.Context, tx *sql.Tx, a engine.AccountID, m engine.Message, source engine.Source, isEdit bool, now int64, ap *engine.Applied) error {
	if err := validKey(a, m.Key); err != nil {
		return err
	}
	k := m.Key
	var blocked bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM message_tombstones WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=?) OR (?<>'channel' AND EXISTS(SELECT 1 FROM pending_deletes WHERE account=? AND msg_id=?))`, a, k.Peer.Type, k.Peer.ID, k.ID, k.Peer.Type, a, k.ID).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		if _, err := deleteMessage(ctx, tx, k, "deleted", now); err != nil {
			return err
		}
		ap.Skipped++
		return nil
	}
	if err := ensurePeer(ctx, tx, a, k.Peer, now); err != nil {
		return err
	}
	// Content ordering and lifecycle policy are independent: even an old copy
	// can reveal a restriction. Load the durable policy before every early return.
	var oldEdit, oldDate, version int64
	var oldContentHash string
	var oldTTL, oldMediaTTL int
	var oldProtected bool
	var oldMediaType sql.NullString
	var oldExpiry sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT max(edit_date,server_edit_date),date,version,ttl_period,expires_at,content_hash,protected,media_ttl,media_type FROM messages WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=?`, a, k.Peer.Type, k.Peer.ID, k.ID).Scan(&oldEdit, &oldDate, &version, &oldTTL, &oldExpiry, &oldContentHash, &oldProtected, &oldMediaTTL, &oldMediaType)
	exists := err == nil
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	revision := editUnix(m.ServerEditDate)
	if visible := editUnix(m.EditDate); visible > revision {
		revision = visible
	}
	// Already expired rows remain unreadable and are owned by Sweep. An old
	// replay must not manufacture a second lifecycle transition before that job.
	if exists && revision <= oldEdit && oldExpiry.Valid && oldExpiry.Int64 <= now {
		ap.Skipped++
		return nil
	}
	sameContent := m.ContentHash != "" && m.ContentHash == oldContentHash
	if exists && !sameContent && (revision >= oldEdit || oldContentHash == "") {
		// Compare retained evidence before security normalization redacts it.
		previous, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages m WHERE m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=?`, a, k.Peer.Type, k.Peer.ID, k.ID))
		if err != nil {
			return err
		}
		sameContent = securityContentEqual(previous, m, revision > oldEdit)
	}
	// Security is independent of content ordering and identity. A contradictory
	// snapshot can reveal restrictions even when its content cannot be accepted.
	var protected, forum bool
	var dialogTTL int
	var refreshed int64
	if err := tx.QueryRowContext(ctx, `SELECT p.protected,p.forum,coalesce((SELECT d.ttl_period FROM dialogs d WHERE d.account=p.account AND d.peer_type=p.peer_type AND d.peer_id=p.peer_id),0),coalesce((SELECT d.refreshed_at FROM dialogs d WHERE d.account=p.account AND d.peer_type=p.peer_type AND d.peer_id=p.peer_id),0) FROM peers p WHERE p.account=? AND p.peer_type=? AND p.peer_id=?`, a, k.Peer.Type, k.Peer.ID).Scan(&protected, &forum, &dialogTTL, &refreshed); err != nil {
		return err
	}
	// General messages may have no reply header at all. Only persisted peer
	// metadata can distinguish them from non-forum messages (topic zero).
	if forum && m.TopicID == 0 {
		m.TopicID = 1
	}
	m.Protected = m.Protected || protected || oldProtected
	if exists {
		// A snapshot cannot move creation time and thereby extend a timer.
		m.Date = timeFromUnix(oldDate)
		// Message expiration is an immutable creation policy, not a shortest-
		// timer merge. Edits cannot acquire it, even when carrying explicit TTL.
		// A full read may reveal an unknown policy, but never replace a known one.
		if oldTTL > 0 || oldExpiry.Valid || isEdit {
			m.TTLPeriod = oldTTL
			m.ExpiresAt = queryTime(oldExpiry.Int64, oldExpiry.Valid)
		}
	}
	if m.TTLPeriod > 0 {
		v := m.Date.Add(time.Duration(m.TTLPeriod) * time.Second)
		m.ExpiresAt = earliestExpiry(m.ExpiresAt, &v)
	} else if !exists && !isEdit && m.EditDate == nil && m.ExpiresAt == nil && dialogTTL > 0 && m.Date.Unix() >= refreshed && (source == engine.SourceUpdate || source == engine.SourceSend) {
		// Dialog inference belongs only to a newly-created live/send message;
		// explicit per-message restrictions above apply to every source/revision.
		m.TTLPeriod = dialogTTL
		v := m.Date.Add(time.Duration(dialogTTL) * time.Second)
		m.ExpiresAt = &v
	}
	if m.ExpiresAt != nil && m.ExpiresAt.Unix() <= now {
		changed, err := deleteMessage(ctx, tx, k, "ttl", now)
		if err != nil {
			return err
		}
		if changed {
			ap.Deleted = append(ap.Deleted, k)
		}
		ap.Skipped++
		return nil
	}
	// Legacy subtype-repair snapshots have no adapter identity and cannot
	// establish a replacement media policy across different media types.
	// Real server snapshots carry ContentHash, including temporary media.
	legacyTemporaryRepair := exists && source == engine.SourceRefresh && revision == oldEdit && oldContentHash == "" && m.ContentHash == "" && oldMediaType.String == string(engine.MediaDocument) && m.Media != nil && m.Media.TTLSeconds > 0 && documentSubtype(m.Media.Type)
	if legacyTemporaryRepair {
		media := *m.Media
		media.TTLSeconds = oldMediaTTL
		m.Media = &media
	}
	if oldMediaTTL > 0 || (m.Media != nil && m.Media.TTLSeconds > 0) {
		mediaType := engine.MediaType(oldMediaType.String)
		incomingTTL := 0
		if m.Media != nil {
			incomingTTL = m.Media.TTLSeconds
			if oldMediaTTL == 0 {
				mediaType = m.Media.Type
			}
		}
		// Once temporary, even replacement/omitted media remains a placeholder.
		m.ContentIdentity = ""
		m.Media = &engine.Media{Type: mediaType, TTLSeconds: retainedMediaTTL(oldMediaTTL, incomingTTL)}
		m.Text, m.Entities, m.Location, m.Poll = "[temporary media]", nil, nil, nil
	}
	if exists {
		if err := tightenMessageSecurityMetadata(ctx, tx, a, m, now); err != nil {
			return err
		}
	}
	if exists && revision <= oldEdit {
		// Content and reaction ordering remain unchanged by policy tightening.
		if isEdit && (revision == 0 || revision == oldEdit) {
			if err := applyMessageReactions(ctx, tx, a, m, now, ap); err != nil {
				return err
			}
		}
		if revision == oldEdit && sameContent && m.ContentIdentity != "" {
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET content_identity=? WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=?`, m.ContentIdentity, a, k.Peer.Type, k.Peer.ID, k.ID); err != nil {
				return err
			}
		}
		if revision == oldEdit && !legacyTemporaryRepair {
			if err := enrichMessageMetadata(ctx, tx, a, m, now); err != nil {
				return err
			}
		}
		ap.Skipped++
		return nil
	}
	if exists && revision > 0 && sameContent {
		// Reaction refreshes can advance edit_date even with EditHide=false.
		// Equal retained content advances only server ordering, preserving the
		// last genuine visible edit date, content version and edit event stream.
		// Changed payloads (including hidden edits) still use the upsert below.
		// Enforce the original expiry above even for these unchanged snapshots.
		if _, err := tx.ExecContext(ctx, `UPDATE messages SET server_edit_date=?,content_hash=?,content_identity=? WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=?`, revision, m.ContentHash, m.ContentIdentity, a, k.Peer.Type, k.Peer.ID, k.ID); err != nil {
			return err
		}
		// Poll votes/answer flags are excluded from content identity. Preserve
		// their normal complete-versus-minimal merge without inventing an edit.
		if m.Poll != nil && m.Poll.ID != 0 && (m.Media == nil || m.Media.TTLSeconds == 0) {
			total := m.Poll.TotalVoters
			if err := applyPoll(ctx, tx, a, engine.PollUpdate{ID: m.Poll.ID, Definition: m.Poll, Results: m.Poll.Options, TotalVoters: &total, Min: m.Poll.Partial}, now); err != nil {
				return err
			}
		}
		reactionApplied := &engine.Applied{}
		if isEdit {
			reactionApplied = ap
		}
		if err := applyMessageReactions(ctx, tx, a, m, now, reactionApplied); err != nil {
			return err
		}
		ap.Skipped++
		return nil
	}
	var mediaType, remote, mediaJSON any
	mediaTTL := 0
	if m.Media != nil {
		v := *m.Media
		mediaType = v.Type
		mediaTTL = v.TTLSeconds
		remote = v.Remote
		v.Remote = nil
		if mediaTTL > 0 {
			v = engine.Media{Type: v.Type, TTLSeconds: mediaTTL}
			remote = nil
			m.Text = "[temporary media]"
			m.Entities = nil
			m.Location = nil
			m.Poll = nil
		}
		if m.Protected {
			v.Downloadable = false
		}
		mediaJSON = encode(v)
	}
	// A partial poll snapshot omits account-specific answers. Keep the old
	// results through the upsert, then let the normal protective poll-update
	// merge apply the new definition/counts without clearing chosen/correct.
	// Match the poll ID as well as the message key: replacement media must not
	// inherit answers from a different poll (or a temporary-media placeholder).
	pollJSON := encode(m.Poll)
	mergePoll := false
	if exists && m.Poll != nil && m.Poll.Partial {
		err := tx.QueryRowContext(ctx, `SELECT poll_json FROM messages WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=? AND media_ttl=0 AND json_valid(poll_json) AND CAST(json_extract(poll_json,'$.id') AS TEXT)=CAST(? AS TEXT)`, a, k.Peer.Type, k.Peer.ID, k.ID, m.Poll.ID).Scan(&pollJSON)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		mergePoll = err == nil
	}
	var senderType, senderID, replyID, replyType, replyPeer, replyTop, serviceType any
	if m.Sender != nil {
		if !validPeer(*m.Sender) {
			return engine.ErrInvalid
		}
		senderType = m.Sender.Type
		senderID = m.Sender.ID
	}
	if m.ReplyTo != nil {
		replyID = m.ReplyTo.ID
		replyTop = m.ReplyTo.TopID
		if m.ReplyTo.Peer != nil {
			replyType = m.ReplyTo.Peer.Type
			replyPeer = m.ReplyTo.Peer.ID
		}
	}
	if m.Service != nil {
		serviceType = m.Service.Type
	}
	fields := []string{"account", "peer_type", "peer_id", "msg_id", "date", "edit_date", "server_edit_date", "content_hash", "content_identity", "out", "sender_type", "sender_id", "sender_name", "post_author", "text", "entities_json", "media_type", "media_json", "media_remote", "media_ttl", "location_json", "poll_json", "reply_to_msg_id", "reply_to_peer_type", "reply_to_peer_id", "reply_top_id", "forward_json", "topic_id", "grouped_id", "service_type", "service_json", "mentioned", "media_unread", "silent", "post", "pinned", "protected", "views", "forwards", "ttl_period", "expires_at", "source", "first_seen_at", "updated_at"}
	vals := []any{a, k.Peer.Type, k.Peer.ID, k.ID, m.Date.Unix(), editUnix(m.EditDate), revision, m.ContentHash, m.ContentIdentity, m.Out, senderType, senderID, m.SenderName, m.PostAuthor, m.Text, encode(m.Entities), mediaType, mediaJSON, remote, mediaTTL, encode(m.Location), pollJSON, replyID, replyType, replyPeer, replyTop, encode(m.Forward), m.TopicID, m.GroupedID, serviceType, encode(m.Service), m.Mentioned, m.MediaUnread, m.Silent, m.Post, m.Pinned, m.Protected, m.Views, m.Forwards, m.TTLPeriod, nullableTime(m.ExpiresAt), source, now, now}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(fields)), ",")
	updates := []string{}
	for _, f := range fields[4:] {
		if f != "first_seen_at" {
			updates = append(updates, f+"=excluded."+f)
		}
	}
	updates = append(updates, "version=messages.version+1")
	_, err = tx.ExecContext(ctx, `INSERT INTO messages(`+strings.Join(fields, ",")+`) VALUES(`+placeholders+`) ON CONFLICT(account,peer_type,peer_id,msg_id) DO UPDATE SET `+strings.Join(updates, ","), vals...)
	if err != nil {
		return err
	}
	if mergePoll {
		total := m.Poll.TotalVoters
		if err := applyPoll(ctx, tx, a, engine.PollUpdate{ID: m.Poll.ID, Definition: m.Poll, Results: m.Poll.Options, TotalVoters: &total, Min: true}, now); err != nil {
			return err
		}
	}
	if !exists {
		ap.Inserted = append(ap.Inserted, k)
	} else {
		ap.Edited = append(ap.Edited, k)
	}
	if ap.Versions == nil {
		ap.Versions = map[string]int64{}
	}
	ap.Versions[VersionKey(k)] = version + 1
	// History copies hydrate reaction state without pretending it arrived live;
	// edit updates must expose changed snapshots as committed reaction events.
	reactionApplied := &engine.Applied{}
	if isEdit {
		reactionApplied = ap
	}
	if err := applyMessageReactions(ctx, tx, a, m, now, reactionApplied); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE dialogs SET last_message_date=max(coalesce(last_message_date,0),?),top_message_id=max(top_message_id,?) WHERE account=? AND peer_type=? AND peer_id=?`, m.Date.Unix(), k.ID, a, k.Peer.Type, k.Peer.ID); err != nil {
		return err
	}
	return saveCursor(ctx, tx, a, engine.Cursor{Peer: k.Peer, NewestID: k.ID}, now)
}

// Message copies enrich reactions; only standalone authoritative updates replace.
func applyMessageReactions(ctx context.Context, tx *sql.Tx, a engine.AccountID, m engine.Message, now int64, ap *engine.Applied) error {
	if m.Reactions == nil && m.ReactionSnapshot == nil {
		return nil
	}
	r := engine.ReactionUpdate{Key: m.Key, Counts: m.Reactions}
	if m.ReactionSnapshot != nil {
		r = *m.ReactionSnapshot
		r.Key = m.Key
	}
	r.Min = true
	return applyReaction(ctx, tx, a, r, now, ap)
}
