package store

import (
	"context"
	"database/sql"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// backfillForumTopics is also used when forum metadata arrives after messages.
// It changes neither message versions nor content/lifecycle/cursor metadata.
// v1 did not retain forum_topic: recover explicit tops first, follow local reply
// chains, and use a missing reply target as the root fallback. Unthreaded forum
// messages belong to General (1); non-forum messages are left untouched.
func backfillForumTopics(ctx context.Context, tx *sql.Tx) error {
	type legacy struct {
		row               int64
		topic, top, reply int
		local             bool
	}
	messages := map[engine.MessageKey]*legacy{}
	var pending []*legacy
	rows, err := tx.QueryContext(ctx, `SELECT m.row_id,m.account,m.peer_type,m.peer_id,m.msg_id,m.topic_id,coalesce(m.reply_top_id,0),coalesce(m.reply_to_msg_id,0),(m.reply_to_peer_type IS NULL OR (m.reply_to_peer_type=m.peer_type AND m.reply_to_peer_id=m.peer_id)) FROM messages m JOIN peers p ON p.account=m.account AND p.peer_type=m.peer_type AND p.peer_id=m.peer_id WHERE p.forum=1`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k engine.MessageKey
		var v legacy
		if err = rows.Scan(&v.row, &k.Account, &k.Peer.Type, &k.Peer.ID, &k.ID, &v.topic, &v.top, &v.reply, &v.local); err != nil {
			rows.Close()
			return err
		}
		messages[k] = &v
		if v.topic == 0 {
			pending = append(pending, &v)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(pending) == 0 {
		return nil
	}
	// Explicit top IDs also identify roots that are present in the local history.
	roots := map[engine.MessageKey]bool{}
	for k, v := range messages {
		if v.top > 0 {
			k.ID = v.top
			roots[k] = true
		}
	}
	visiting := map[engine.MessageKey]bool{}
	var resolve func(engine.MessageKey) int
	resolve = func(k engine.MessageKey) int {
		v := messages[k]
		if v == nil {
			return k.ID
		}
		if v.topic > 0 {
			return v.topic
		}
		if visiting[k] {
			return k.ID
		} // malformed/cyclic historical replies must terminate.
		visiting[k] = true
		switch {
		case v.top > 0:
			v.topic = v.top
		case roots[k]:
			v.topic = k.ID
		case v.local && v.reply > 0:
			target := k
			target.ID = v.reply
			v.topic = resolve(target)
		default:
			v.topic = 1
		}
		delete(visiting, k)
		return v.topic
	}
	for k := range messages {
		resolve(k)
	}
	// Resolution memoizes parents; persist after the whole reply graph is resolved.
	// Existing nonzero topics remain unchanged.
	for _, v := range pending {
		if _, err = tx.ExecContext(ctx, `UPDATE messages SET topic_id=? WHERE row_id=? AND topic_id=0`, v.topic, v.row); err != nil {
			return err
		}
	}
	return nil
}

// migrateV2 repairs topic identities and rebuilds derived mention links atomically.
func (s *Store) migrateV2(ctx context.Context) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = backfillForumTopics(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT account FROM accounts`)
	if err != nil {
		return err
	}
	var accounts []engine.AccountID
	for rows.Next() {
		var a engine.AccountID
		if err = rows.Scan(&a); err != nil {
			rows.Close()
			return err
		}
		accounts = append(accounts, a)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, a := range accounts {
		if err = indexMentions(ctx, tx, a, s.now().Unix()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version=2`); err != nil {
		return err
	}
	if err = s.fail("migration_v2_before_commit"); err != nil {
		return err
	}
	return tx.Commit()
}
