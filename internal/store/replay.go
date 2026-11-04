package store

import (
	"context"
	"database/sql"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"sort"
)

// Skip actual SQL writes for idempotent replay, not merely event delivery.
func peerUnchanged(ctx context.Context, tx *sql.Tx, a engine.AccountID, u engine.PeerUpsert) (bool, error) {
	p := u.Peer
	if !validPeer(p.Ref) {
		return false, engine.ErrInvalid
	}
	old, err := queryScanPeer(tx.QueryRowContext(ctx, "SELECT "+queryPeerColumns+" FROM peers p WHERE p.account=? AND p.peer_type=? AND p.peer_id=?", a, p.Ref.Type, p.Ref.ID))
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, v := range [][2]string{{string(p.Kind), string(old.Kind)}, {p.Title, old.Title}, {p.FirstName, old.FirstName}, {p.LastName, old.LastName}, {p.Username, old.Username}, {p.Phone, old.Phone}, {string(p.Access), string(old.Access)}} {
		if v[0] != "" && v[0] != v[1] {
			return false, nil
		}
	}
	if p.IsContact && !old.IsContact || p.Forum && !old.Forum || p.Protected && !old.Protected || p.Participants > old.Participants {
		return false, nil
	}
	if p.MigratedTo != nil && (old.MigratedTo == nil || *p.MigratedTo != *old.MigratedTo) {
		return false, nil
	}
	names := map[string]bool{}
	for _, n := range old.Usernames {
		names[n] = true
	}
	for _, n := range append(append([]string{}, p.Usernames...), p.Username) {
		if n != "" && !names[n] {
			return false, nil
		}
	}
	var h sql.NullInt64
	var min bool
	if err = tx.QueryRowContext(ctx, `SELECT access_hash,hash_min FROM peers WHERE account=? AND peer_type=? AND peer_id=?`, a, p.Ref.Type, p.Ref.ID).Scan(&h, &min); err != nil {
		return false, err
	}
	if u.AccessHash != 0 && (!h.Valid || min || !u.HashMin) && (h.Int64 != u.AccessHash || min != u.HashMin) {
		return false, nil
	}
	return true, nil
}
func dialogUnchanged(ctx context.Context, tx *sql.Tx, a engine.AccountID, d engine.Dialog) (bool, error) {
	old := engine.Dialog{Peer: d.Peer}
	var mute sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT folder_id,pinned,top_message_id,read_inbox_max_id,read_outbox_max_id,unread_count,unread_mentions,unread_reactions,unread_mark,mute_until,ttl_period FROM dialogs WHERE account=? AND peer_type=? AND peer_id=?`, a, d.Peer.Type, d.Peer.ID).Scan(&old.FolderID, &old.Pinned, &old.TopMessageID, &old.ReadInboxMaxID, &old.ReadOutboxMaxID, &old.UnreadCount, &old.UnreadMentions, &old.UnreadReactions, &old.UnreadMark, &mute, &old.TTLPeriod)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	old.MuteUntil = queryTime(mute.Int64, mute.Valid)
	d.TopMessageID = max(d.TopMessageID, old.TopMessageID)
	d.ReadInboxMaxID = max(d.ReadInboxMaxID, old.ReadInboxMaxID)
	d.ReadOutboxMaxID = max(d.ReadOutboxMaxID, old.ReadOutboxMaxID)
	return encode(old) == encode(d), nil
}
func participantsUnchanged(ctx context.Context, tx *sql.Tx, a engine.AccountID, p engine.ParticipantsSnapshot) (bool, error) {
	var about string
	var size int
	var partial, amAdmin bool
	var owner, created, linkedID sql.NullInt64
	var linkedType sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT about,size,partial,owner_id,created_at,linked_type,linked_id,am_admin FROM groups_full WHERE account=? AND peer_type=? AND peer_id=?`, a, p.Group.Type, p.Group.ID).Scan(&about, &size, &partial, &owner, &created, &linkedType, &linkedID, &amAdmin); err != nil {
		return false, err
	}
	if about != p.About || size != p.Size || partial != p.Partial || amAdmin != p.AmAdmin {
		return false, nil
	}
	if owner.Valid != (p.Owner != nil) || p.Owner != nil && owner.Int64 != p.Owner.ID {
		return false, nil
	}
	if created.Valid != (p.CreatedAt != nil) || p.CreatedAt != nil && created.Int64 != p.CreatedAt.Unix() {
		return false, nil
	}
	if linkedType.Valid != (p.LinkedChat != nil) || linkedID.Valid != (p.LinkedChat != nil) || p.LinkedChat != nil && (linkedType.String != string(p.LinkedChat.Type) || linkedID.Int64 != p.LinkedChat.ID) {
		return false, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.user_type,t.user_id,t.role,t.joined_at,t.admin_rights,t.banned_rights,t.banned_until,t.can_edit FROM participants t WHERE t.account=? AND t.group_type=? AND t.group_id=?`, a, p.Group.Type, p.Group.ID)
	if err != nil {
		return false, err
	}
	canonical := func(v engine.Participant) string {
		return encode([]any{v.Peer.Type, v.Peer.ID, v.Role, nullableTime(v.JoinedAt), v.AdminRights, v.BannedRights, v.BannedUntil, v.CanEdit})
	}
	var old, next []string
	for rows.Next() {
		var v engine.Participant
		var date sql.NullInt64
		if err = rows.Scan(&v.Peer.Type, &v.Peer.ID, &v.Role, &date, &v.AdminRights, &v.BannedRights, &v.BannedUntil, &v.CanEdit); err != nil {
			rows.Close()
			return false, err
		}
		v.JoinedAt = queryTime(date.Int64, date.Valid)
		old = append(old, canonical(v))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	peers, err := participantPeerMetadataFor(ctx, tx, a, p.Participants)
	if err != nil {
		return false, err
	}
	for _, v := range p.Participants {
		next = append(next, canonical(v))
		oldPeer, exists := peers[v.Peer]
		if !exists || v.Name != "" && v.Name != oldPeer.first || v.Username != "" && (v.Username != oldPeer.username || !oldPeer.usernameKnown) {
			return false, nil
		}
	}
	sort.Strings(old)
	sort.Strings(next)
	return encode(old) == encode(next), nil
}
func cursorUnchanged(ctx context.Context, tx *sql.Tx, a engine.AccountID, c engine.Cursor) (bool, error) {
	old := engine.Cursor{Peer: c.Peer}
	var date, flood sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT newest_id,oldest_id,oldest_date,complete,state,flood_until,last_error,fetched FROM backfill_cursors WHERE account=? AND peer_type=? AND peer_id=?`, a, c.Peer.Type, c.Peer.ID).Scan(&old.NewestID, &old.OldestID, &date, &old.Complete, &old.State, &flood, &old.LastError, &old.Fetched)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	old.OldestDate = queryTime(date.Int64, date.Valid)
	old.FloodUntil = queryTime(flood.Int64, flood.Valid)
	c.NewestID = max(c.NewestID, old.NewestID)
	c.Fetched = max(c.Fetched, old.Fetched)
	c.Complete = c.Complete || old.Complete
	if c.OldestID == 0 || old.OldestID > 0 && old.OldestID <= c.OldestID {
		c.OldestID = old.OldestID
		c.OldestDate = old.OldestDate
	}
	if c.State == "" {
		c.State = old.State
		c.FloodUntil = old.FloodUntil
		c.LastError = old.LastError
	}
	if c.Complete && (c.State == "running" || c.State == "flood_wait") {
		c.State = "idle"
		c.FloodUntil = nil
	}
	return encode(old) == encode(c), nil
}
