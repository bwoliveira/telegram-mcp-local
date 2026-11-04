package pocstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// VersionKey is the text key used in Applied.Versions. The account is included
// even though a PoC database normally contains just one slot.
func VersionKey(k engine.MessageKey) string {
	return fmt.Sprintf("%s/%s:%d/%d", k.Account, k.Peer.Type, k.Peer.ID, k.ID)
}

// Apply commits the entire page atomically and returns changes only after commit.
// Safety policy for this spike: ALL protected, expiring and view-once messages
// are omitted (including future TTL), with content-free tombstones. Consequently
// this store never needs a TTL sweeper and cannot expose expired copies. Downloads
// are deliberately unsupported: file retention/unlink belongs to production store.
func (s *Store) Apply(ctx context.Context, b engine.Batch) (engine.Applied, error) {
	if b.Account == "" {
		return engine.Applied{}, engine.ErrInvalid
	}
	if len(b.Downloads) > 0 {
		return engine.Applied{}, engine.ErrNotSupported
	}
	var applied engine.Applied
	err := s.transaction(ctx, func(tx *sql.Tx) error {
		for _, p := range b.Peers {
			if err := upsertPeer(ctx, tx, b.Account, p); err != nil {
				return err
			}
		}
		for _, d := range b.Dialogs {
			if !validPeer(d.Peer) {
				return engine.ErrInvalid
			}
			var old engine.Dialog
			_, _, err := load(ctx, tx, b.Account, "dialog", key(d.Peer), &old)
			if err != nil {
				return err
			}
			d.ReadInboxMaxID = max(d.ReadInboxMaxID, old.ReadInboxMaxID)
			d.ReadOutboxMaxID = max(d.ReadOutboxMaxID, old.ReadOutboxMaxID)
			if _, err = put(ctx, tx, b.Account, "dialog", key(d.Peer), d); err != nil {
				return err
			}
		}
		// A newly protected peer must not leave previously ingested content behind.
		for _, p := range b.Peers {
			if p.Peer.Protected {
				rs, err := records(ctx, tx, b.Account, "message")
				if err != nil {
					return err
				}
				for _, r := range rs {
					var k engine.MessageKey
					if err = json.Unmarshal([]byte(r.key), &k); err != nil {
						return err
					}
					if k.Peer == p.Peer.Ref {
						if err = deleteMessage(ctx, tx, k, "protected", &applied); err != nil {
							return err
						}
					}
				}
			}
		}
		for _, m := range b.Messages {
			if err := applyMessage(ctx, tx, b.Account, m, &applied); err != nil {
				return err
			}
		}
		for _, m := range b.Edits {
			if err := applyMessage(ctx, tx, b.Account, m, &applied); err != nil {
				return err
			}
		}
		for _, d := range b.Deletes {
			for _, id := range d.IDs {
				if id <= 0 {
					return engine.ErrInvalid
				}
			}
			if d.Peer != nil {
				if !validPeer(*d.Peer) {
					return engine.ErrInvalid
				}
				for _, id := range d.IDs {
					if err := deleteMessage(ctx, tx, engine.MessageKey{Account: b.Account, Peer: *d.Peer, ID: id}, "deleted", &applied); err != nil {
						return err
					}
				}
			} else {
				keys, err := resolve(ctx, tx, b.Account, d.IDs)
				if err != nil {
					return err
				}
				known := map[int]bool{}
				for _, k := range keys {
					known[k.ID] = true
					if err = deleteMessage(ctx, tx, k, "deleted", &applied); err != nil {
						return err
					}
				}
				// Keep an account-wide id-only delete fence even after resolving it. This
				// also prevents a later non-channel copy with an as-yet unknown peer.
				for _, id := range d.IDs {
					changed, err := put(ctx, tx, b.Account, "pending", key(id), id)
					if err != nil {
						return err
					}
					if changed && !known[id] {
						applied.UnresolvedDeletes = append(applied.UnresolvedDeletes, id)
					}
				}
			}
		}
		for _, r := range b.Reactions {
			if err := applyReaction(ctx, tx, b.Account, r, &applied); err != nil {
				return err
			}
		}
		for _, r := range b.Reads {
			if err := applyRead(ctx, tx, b.Account, r, &applied); err != nil {
				return err
			}
		}
		for _, p := range b.Participants {
			if !validPeer(p.Group) {
				return engine.ErrInvalid
			}
			for _, v := range p.Participants {
				if !validPeer(v.Peer) {
					return engine.ErrInvalid
				}
			}
			var old engine.ParticipantsSnapshot
			_, ok, err := load(ctx, tx, b.Account, "participants", key(p.Group), &old)
			if err != nil {
				return err
			}
			if !ok || !p.At.Before(old.At) {
				if _, err = put(ctx, tx, b.Account, "participants", key(p.Group), p); err != nil {
					return err
				}
			}
		}
		for _, a := range b.Access {
			if err := applyAccess(ctx, tx, b.Account, a, &applied); err != nil {
				return err
			}
		}
		for _, g := range b.Gaps {
			if err := applyGap(ctx, tx, b.Account, g, false, &applied); err != nil {
				return err
			}
		}
		for _, g := range b.FilledGaps {
			if err := applyGap(ctx, tx, b.Account, g, true, &applied); err != nil {
				return err
			}
		}
		if b.Cursor != nil {
			if err := saveCursor(ctx, tx, b.Account, *b.Cursor); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return engine.Applied{}, err
	}
	return applied, nil
}

func upsertPeer(ctx context.Context, tx *sql.Tx, acc engine.AccountID, u engine.PeerUpsert) error {
	p := u.Peer
	if !validPeer(p.Ref) {
		return engine.ErrInvalid
	}
	var old peerRecord
	_, _, err := load(ctx, tx, acc, "peer", key(p.Ref), &old)
	if err != nil {
		return err
	}
	mergePeer(&old.Peer, p)
	if old.Hash == 0 || !u.HashMin {
		if u.AccessHash != 0 {
			old.Hash = u.AccessHash
			old.Min = u.HashMin
		}
	}
	_, err = put(ctx, tx, acc, "peer", key(p.Ref), old)
	return err
}
func mergePeer(dst *engine.Peer, p engine.Peer) {
	dst.Ref = p.Ref
	if p.Kind != "" {
		dst.Kind = p.Kind
	}
	if p.Title != "" {
		dst.Title = p.Title
	}
	if p.FirstName != "" {
		dst.FirstName = p.FirstName
	}
	if p.LastName != "" {
		dst.LastName = p.LastName
	}
	if p.Username != "" {
		dst.Username = p.Username
	}
	if len(p.Usernames) > 0 {
		dst.Usernames = p.Usernames
	}
	if p.Phone != "" {
		dst.Phone = p.Phone
	}
	if p.Participants > 0 {
		dst.Participants = p.Participants
	}
	if p.Access != "" {
		dst.Access = p.Access
	}
	if p.MigratedTo != nil {
		dst.MigratedTo = p.MigratedTo
	}
	dst.IsContact = dst.IsContact || p.IsContact
	dst.Forum = dst.Forum || p.Forum
	dst.Protected = dst.Protected || p.Protected
	if dst.Access == "" {
		dst.Access = engine.AccessOK
	}
}
func applyMessage(ctx context.Context, tx *sql.Tx, acc engine.AccountID, m engine.Message, a *engine.Applied) error {
	if err := validKey(acc, m.Key); err != nil {
		return err
	}
	var tomb string
	_, tombstoned, err := load(ctx, tx, acc, "tombstone", key(m.Key), &tomb)
	if err != nil {
		return err
	}
	var pending int
	var deleted bool
	if m.Key.Peer.Type != engine.PeerChannel {
		_, deleted, err = load(ctx, tx, acc, "pending", key(m.Key.ID), &pending)
		if err != nil {
			return err
		}
	}
	if tombstoned || deleted {
		if deleted && !tombstoned {
			if _, err = put(ctx, tx, acc, "tombstone", key(m.Key), "deleted"); err != nil {
				return err
			}
		}
		a.Skipped++
		return nil
	}
	var p peerRecord
	_, _, err = load(ctx, tx, acc, "peer", key(m.Key.Peer), &p)
	if err != nil {
		return err
	}
	var d engine.Dialog
	_, _, err = load(ctx, tx, acc, "dialog", key(m.Key.Peer), &d)
	if err != nil {
		return err
	}
	if m.Protected || p.Peer.Protected || m.TTLPeriod > 0 || m.ExpiresAt != nil || d.TTLPeriod > 0 || (m.Media != nil && m.Media.TTLSeconds > 0) {
		if err = deleteMessage(ctx, tx, m.Key, "protected_or_ttl", a); err != nil {
			return err
		}
		a.Skipped++
		return nil
	}
	var old messageRecord
	version, exists, err := load(ctx, tx, acc, "message", key(m.Key), &old)
	if err != nil {
		return err
	}
	if exists {
		// The server edit timestamp is authoritative, not page arrival order.
		if m.EditDate == nil || (old.Message.EditDate != nil && !m.EditDate.After(*old.Message.EditDate)) {
			return nil
		}
	}
	r := messageRecord{Message: m}
	if m.Media != nil {
		r.Remote = m.Media.Remote
	}
	changed, err := put(ctx, tx, acc, "message", key(m.Key), r)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	if exists {
		a.Edited = append(a.Edited, m.Key)
	} else {
		a.Inserted = append(a.Inserted, m.Key)
	}
	if a.Versions == nil {
		a.Versions = make(map[string]int64)
	}
	a.Versions[VersionKey(m.Key)] = version + 1
	return saveCursor(ctx, tx, acc, engine.Cursor{Peer: m.Key.Peer, NewestID: m.Key.ID})
}
func deleteMessage(ctx context.Context, tx *sql.Tx, k engine.MessageKey, reason string, a *engine.Applied) error {
	removed, err := remove(ctx, tx, k.Account, "message", key(k))
	if err != nil {
		return err
	}
	if _, err = remove(ctx, tx, k.Account, "reaction", key(k)); err != nil {
		return err
	}
	var existing string
	_, exists, err := load(ctx, tx, k.Account, "tombstone", key(k), &existing)
	if err != nil {
		return err
	}
	if !exists {
		if _, err = put(ctx, tx, k.Account, "tombstone", key(k), reason); err != nil {
			return err
		}
	}
	if removed || !exists {
		a.Deleted = append(a.Deleted, k)
	}
	return nil
}
func saveCursor(ctx context.Context, tx *sql.Tx, acc engine.AccountID, c engine.Cursor) error {
	if !validPeer(c.Peer) || c.OldestID < 0 || c.NewestID < 0 || c.Fetched < 0 {
		return engine.ErrInvalid
	}
	if c.State != "" && c.State != "idle" && c.State != "running" && c.State != "flood_wait" && c.State != "no_access" && c.State != "error" {
		return engine.ErrInvalid
	}
	var old engine.Cursor
	_, ok, err := load(ctx, tx, acc, "cursor", key(c.Peer), &old)
	if err != nil {
		return err
	}
	if ok {
		if c.OldestID == 0 || (old.OldestID > 0 && old.OldestID <= c.OldestID) {
			c.OldestID = old.OldestID
			c.OldestDate = old.OldestDate
		}
		c.NewestID = max(c.NewestID, old.NewestID)
		c.Fetched = max(c.Fetched, old.Fetched)
		c.Complete = c.Complete || old.Complete
		if c.State == "" {
			c.State = old.State
			c.LastError = old.LastError
			c.FloodUntil = old.FloodUntil
		}
	}
	if c.State == "" {
		c.State = "idle"
	}
	if c.Complete && (c.State == "running" || c.State == "flood_wait") {
		c.State = "idle"
		c.FloodUntil = nil
	}
	_, err = put(ctx, tx, acc, "cursor", key(c.Peer), c)
	return err
}

func applyReaction(ctx context.Context, tx *sql.Tx, acc engine.AccountID, r engine.ReactionUpdate, a *engine.Applied) error {
	if err := validKey(acc, r.Key); err != nil {
		return err
	}
	var m messageRecord
	_, ok, err := load(ctx, tx, acc, "message", key(r.Key), &m)
	if err != nil || !ok {
		return err
	}
	var old engine.ReactionUpdate
	_, ok, err = load(ctx, tx, acc, "reaction", key(r.Key), &old)
	if err != nil {
		return err
	}
	if !ok {
		old.Key = r.Key
		old.Counts = m.Message.Reactions
	}
	// Canonicalize copies: batches may be replayed concurrently by the adapter.
	r.Counts = canonicalCounts(r.Counts)
	old.Counts = canonicalCounts(old.Counts)
	r.Recent = canonicalRecent(r.Recent)
	old.Recent = canonicalRecent(old.Recent)
	if key(old.Counts) == key(r.Counts) && key(old.Recent) == key(r.Recent) {
		return nil
	}
	if _, err = put(ctx, tx, acc, "reaction", key(r.Key), r); err != nil {
		return err
	}
	m.Message.Reactions = r.Counts
	if _, err = put(ctx, tx, acc, "message", key(r.Key), m); err != nil {
		return err
	}
	ev := engine.ReactionEvent{Key: r.Key, Counts: r.Counts}
	for _, v := range r.Recent {
		if !containsReaction(old.Recent, v) {
			ev.Added = append(ev.Added, v)
		}
	}
	for _, v := range old.Recent {
		if !containsReaction(r.Recent, v) {
			ev.Removed = append(ev.Removed, v)
		}
	}
	a.Reactions = append(a.Reactions, ev)
	return nil
}
func canonicalCounts(list []engine.ReactionCount) []engine.ReactionCount {
	out := append([]engine.ReactionCount(nil), list...)
	sort.Slice(out, func(i, j int) bool { return out[i].Emoji < out[j].Emoji })
	return out
}
func canonicalRecent(list []engine.PeerReaction) []engine.PeerReaction {
	out := append([]engine.PeerReaction(nil), list...)
	sort.Slice(out, func(i, j int) bool { return key(out[i]) < key(out[j]) })
	return out
}
func containsReaction(list []engine.PeerReaction, v engine.PeerReaction) bool {
	for _, old := range list {
		if reflect.DeepEqual(old, v) {
			return true
		}
	}
	return false
}
func applyRead(ctx context.Context, tx *sql.Tx, acc engine.AccountID, r engine.ReadUpdate, a *engine.Applied) error {
	if !validPeer(r.Peer) || r.MaxID < 0 {
		return engine.ErrInvalid
	}
	var d engine.Dialog
	_, _, err := load(ctx, tx, acc, "dialog", key(r.Peer), &d)
	if err != nil {
		return err
	}
	d.Peer = r.Peer
	prev := d.ReadInboxMaxID
	if r.Outbox {
		prev = d.ReadOutboxMaxID
	}
	if r.MaxID <= prev {
		return nil
	}
	if r.Outbox {
		d.ReadOutboxMaxID = r.MaxID
	} else {
		d.ReadInboxMaxID = r.MaxID
		d.UnreadCount = r.StillUnread
	}
	if _, err = put(ctx, tx, acc, "dialog", key(r.Peer), d); err != nil {
		return err
	}
	if r.Outbox {
		r.MessageIDs = nil
		rs, err := records(ctx, tx, acc, "message")
		if err != nil {
			return err
		}
		for _, v := range rs {
			var m messageRecord
			if err = json.Unmarshal(v.body, &m); err != nil {
				return err
			}
			if m.Message.Key.Peer == r.Peer && m.Message.Out && m.Message.Key.ID > prev && m.Message.Key.ID <= r.MaxID {
				r.MessageIDs = append(r.MessageIDs, m.Message.Key.ID)
			}
		}
		sort.Ints(r.MessageIDs)
		if len(r.MessageIDs) > 100 {
			r.MessageIDs = r.MessageIDs[:100]
		}
		a.Reads = append(a.Reads, r)
	}
	return nil
}
func applyAccess(ctx context.Context, tx *sql.Tx, acc engine.AccountID, a engine.AccessChange, out *engine.Applied) error {
	if !validPeer(a.Peer) {
		return engine.ErrInvalid
	}
	switch a.State {
	case engine.AccessOK, engine.AccessLeft, engine.AccessKicked, engine.AccessBanned, engine.AccessLost, engine.AccessDeleted:
	default:
		return engine.ErrInvalid
	}
	var p peerRecord
	_, _, err := load(ctx, tx, acc, "peer", key(a.Peer), &p)
	if err != nil {
		return err
	}
	changed := p.Peer.Access != a.State
	p.Peer.Ref = a.Peer
	p.Peer.Access = a.State
	if _, err = put(ctx, tx, acc, "peer", key(a.Peer), p); err != nil {
		return err
	}
	if a.State != engine.AccessOK {
		if a.Peer.Type == engine.PeerChannel {
			if _, err = remove(ctx, tx, acc, "channel", key(a.Peer.ID)); err != nil {
				return err
			}
		}
		if err = saveCursor(ctx, tx, acc, engine.Cursor{Peer: a.Peer, State: "no_access"}); err != nil {
			return err
		}
		if err = applyGap(ctx, tx, acc, engine.GapMark{Peer: &a.Peer, Reason: engine.GapAccessLost}, false, out); err != nil {
			return err
		}
	} else if changed {
		if err = saveCursor(ctx, tx, acc, engine.Cursor{Peer: a.Peer, State: "idle"}); err != nil {
			return err
		}
	}
	if changed {
		out.Access = append(out.Access, a)
	}
	return nil
}
func gapKey(g engine.GapMark) string { g.Filled = nil; g.Detected = g.Detected.UTC(); return key(g) }
func applyGap(ctx context.Context, tx *sql.Tx, acc engine.AccountID, g engine.GapMark, filled bool, a *engine.Applied) error {
	if g.Peer != nil && !validPeer(*g.Peer) {
		return engine.ErrInvalid
	}
	switch g.Reason {
	case engine.GapChannelTooLong, engine.GapDifferenceTooLong, engine.GapStateLost, engine.GapAccessLost:
	default:
		return engine.ErrInvalid
	}
	if filled && g.Filled == nil {
		return engine.ErrInvalid
	}
	var old engine.GapMark
	_, ok, err := load(ctx, tx, acc, "gap", gapKey(g), &old)
	if err != nil {
		return err
	}
	if ok && old.Filled != nil {
		return nil
	}
	changed, err := put(ctx, tx, acc, "gap", gapKey(g), g)
	if err != nil {
		return err
	}
	if changed {
		a.Gaps = append(a.Gaps, g)
	}
	return nil
}
