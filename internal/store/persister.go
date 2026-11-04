package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"time"
)

var _ engine.Persister = (*Store)(nil)

func (s *Store) LoadUpdatesState(ctx context.Context, a engine.AccountID) (engine.UpdatesState, bool, error) {
	var st engine.UpdatesState
	if err := s.checkAccount(a); err != nil {
		return st, false, err
	}
	err := s.reader.QueryRowContext(ctx, "SELECT pts,qts,date,seq FROM updates_state WHERE account=?", a).Scan(&st.Pts, &st.Qts, &st.Date, &st.Seq)
	if errors.Is(err, sql.ErrNoRows) {
		return st, false, nil
	}
	return st, err == nil, err
}
func (s *Store) SaveUpdatesState(ctx context.Context, a engine.AccountID, st engine.UpdatesState) error {
	if err := s.checkAccount(a); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writer.ExecContext(ctx, `INSERT INTO updates_state(account,pts,qts,date,seq,updated_at) VALUES(?,?,?,?,?,unixepoch()) ON CONFLICT(account) DO UPDATE SET pts=excluded.pts,qts=excluded.qts,date=excluded.date,seq=excluded.seq`, a, st.Pts, st.Qts, st.Date, st.Seq)
	return err
}
func (s *Store) PatchUpdatesState(ctx context.Context, a engine.AccountID, p engine.UpdatesPatch) error {
	if err := s.checkAccount(a); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	res, err := s.writer.ExecContext(ctx, `UPDATE updates_state SET pts=coalesce(?,pts),qts=coalesce(?,qts),date=coalesce(?,date),seq=coalesce(?,seq) WHERE account=?`, p.Pts, p.Qts, p.Date, p.Seq, a)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return err
}
func (s *Store) LoadChannelPts(ctx context.Context, a engine.AccountID, id int64) (int, bool, error) {
	if err := s.checkAccount(a); err != nil {
		return 0, false, err
	}
	if id <= 0 {
		return 0, false, engine.ErrInvalid
	}
	var pts int
	err := s.reader.QueryRowContext(ctx, `SELECT pts FROM channel_state WHERE account=? AND channel_id=?`, a, id).Scan(&pts)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return pts, err == nil, err
}
func (s *Store) SaveChannelPts(ctx context.Context, a engine.AccountID, id int64, pts int) error {
	if err := s.checkAccount(a); err != nil {
		return err
	}
	if id <= 0 {
		return engine.ErrInvalid
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writer.ExecContext(ctx, `INSERT INTO channel_state(account,channel_id,pts,updated_at) VALUES(?,?,?,unixepoch()) ON CONFLICT(account,channel_id) DO UPDATE SET pts=excluded.pts`, a, id, pts)
	return err
}
func (s *Store) ForEachChannelPts(ctx context.Context, a engine.AccountID, fn func(int64, int) error) error {
	if err := s.checkAccount(a); err != nil {
		return err
	}
	if fn == nil {
		return engine.ErrInvalid
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT channel_id,pts FROM channel_state WHERE account=? ORDER BY channel_id`, a)
	if err != nil {
		return err
	}
	var pairs [][2]int64
	for rows.Next() {
		var p [2]int64
		if err = rows.Scan(&p[0], &p[1]); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range pairs {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = fn(p[0], int(p[1])); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) DropChannelState(ctx context.Context, a engine.AccountID, id int64) error {
	if err := s.checkAccount(a); err != nil {
		return err
	}
	if id <= 0 {
		return engine.ErrInvalid
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writer.ExecContext(ctx, `DELETE FROM channel_state WHERE account=? AND channel_id=?`, a, id)
	return err
}
func (s *Store) ResetUpdates(ctx context.Context, a engine.AccountID) error {
	if err := s.checkAccount(a); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"updates_state", "channel_state"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE account=?", a); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (s *Store) AccessHash(ctx context.Context, a engine.AccountID, p engine.PeerRef) (int64, bool, error) {
	if err := s.checkAccount(a); err != nil {
		return 0, false, err
	}
	if !validPeer(p) {
		return 0, false, engine.ErrInvalid
	}
	var h sql.NullInt64
	err := s.reader.QueryRowContext(ctx, `SELECT access_hash FROM peers WHERE account=? AND peer_type=? AND peer_id=? AND hash_min=0`, a, p.Type, p.ID).Scan(&h)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return h.Int64, h.Valid && h.Int64 != 0, err
}
func (s *Store) LookupPeer(ctx context.Context, a engine.AccountID, p engine.PeerRef) (engine.Peer, bool, error) {
	if err := s.checkAccount(a); err != nil {
		return engine.Peer{}, false, err
	}
	if !validPeer(p) {
		return engine.Peer{}, false, engine.ErrInvalid
	}
	return s.queryLookupPeer(ctx, a, p)
}
func (s *Store) FindPeers(ctx context.Context, a engine.AccountID, q string, n int) ([]engine.Peer, error) {
	return s.queryPeers(ctx, a, PeerOptions{Query: q, Limit: n}, "1=1")
}

// Message is the engine-only lookup: unlike local read DTOs it restores the
// opaque media location. It is never JSON-visible (engine.Media.Remote).
func (s *Store) Message(ctx context.Context, k engine.MessageKey) (engine.Message, bool, error) {
	if err := s.checkAccount(k.Account); err != nil {
		return engine.Message{}, false, err
	}
	if err := validKey(s.account, k); err != nil {
		return engine.Message{}, false, err
	}
	m, err := scanMessage(s.reader.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages m WHERE m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=? AND (m.expires_at IS NULL OR m.expires_at>?)`, k.Account, k.Peer.Type, k.Peer.ID, k.ID, s.now().Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return engine.Message{}, false, nil
	}
	if err != nil {
		return m, false, err
	}
	if m.Media != nil && m.Media.TTLSeconds == 0 {
		err = s.reader.QueryRowContext(ctx, `SELECT media_remote FROM messages WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=? AND (expires_at IS NULL OR expires_at>?)`, k.Account, k.Peer.Type, k.Peer.ID, k.ID, s.now().Unix()).Scan(&m.Media.Remote)
		if errors.Is(err, sql.ErrNoRows) {
			return engine.Message{}, false, nil
		}
	}
	return m, err == nil, err
}

type queryDB interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func resolveIDs(ctx context.Context, db queryDB, a engine.AccountID, ids []int) ([]engine.MessageKey, error) {
	keys := []engine.MessageKey{}
	seen := map[engine.MessageKey]bool{}
	for _, id := range ids {
		if id <= 0 {
			return nil, engine.ErrInvalid
		}
		rows, err := db.QueryContext(ctx, `SELECT peer_type,peer_id FROM messages WHERE account=? AND msg_id=? AND peer_type<>'channel' UNION SELECT peer_type,peer_id FROM message_tombstones WHERE account=? AND msg_id=? AND peer_type<>'channel'`, a, id, a, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			k := engine.MessageKey{Account: a, ID: id}
			if err = rows.Scan(&k.Peer.Type, &k.Peer.ID); err != nil {
				rows.Close()
				return nil, err
			}
			if !seen[k] {
				keys = append(keys, k)
				seen[k] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return keys, nil
}
func (s *Store) ResolveMessageIDs(ctx context.Context, a engine.AccountID, ids []int) ([]engine.MessageKey, error) {
	if err := s.checkAccount(a); err != nil {
		return nil, err
	}
	return resolveIDs(ctx, s.reader, a, ids)
}
func (s *Store) Cursor(ctx context.Context, a engine.AccountID, p engine.PeerRef) (engine.Cursor, bool, error) {
	c := engine.Cursor{Peer: p}
	if err := s.checkAccount(a); err != nil {
		return c, false, err
	}
	if !validPeer(p) {
		return c, false, engine.ErrInvalid
	}
	var oldest, flood sql.NullInt64
	err := s.reader.QueryRowContext(ctx, `SELECT newest_id,oldest_id,oldest_date,complete,state,flood_until,last_error,fetched FROM backfill_cursors WHERE account=? AND peer_type=? AND peer_id=?`, a, p.Type, p.ID).Scan(&c.NewestID, &c.OldestID, &oldest, &c.Complete, &c.State, &flood, &c.LastError, &c.Fetched)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	c.OldestDate = queryTime(oldest.Int64, oldest.Valid)
	c.FloodUntil = queryTime(flood.Int64, flood.Valid)
	return c, err == nil, err
}
func (s *Store) SelfID(ctx context.Context, a engine.AccountID) (int64, error) {
	if err := s.checkAccount(a); err != nil {
		return 0, err
	}
	var id int64
	err := s.reader.QueryRowContext(ctx, "SELECT self_id FROM accounts WHERE account=?", a).Scan(&id)
	return id, err
}
func (s *Store) BindSelf(ctx context.Context, a engine.AccountID, self engine.Account) error {
	if err := s.checkAccount(a); err != nil {
		return err
	}
	if self.SelfID <= 0 {
		return engine.ErrInvalid
	}
	if self.ID != "" && self.ID != a {
		return engine.ErrAccountMismatch
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	r, err := s.writer.ExecContext(ctx, `UPDATE accounts SET self_id=?,phone=?,username=?,first_name=?,last_name=?,premium=?,bound_at=coalesce(bound_at,unixepoch()),auth_state='authorized',auth_reason='',updated_at=? WHERE account=? AND (self_id=0 OR self_id=?)`, self.SelfID, self.Phone, self.Username, self.FirstName, self.LastName, self.Premium, s.now().Unix(), a, self.SelfID)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return engine.ErrAccountMismatch
	}
	return err
}
func (s *Store) SetAuthState(ctx context.Context, a engine.AccountID, st engine.AuthState, reason string) error {
	if err := s.checkAccount(a); err != nil {
		return err
	}
	if st != engine.AuthNone && st != engine.AuthAuthorized && st != engine.AuthLoggedOut && st != engine.AuthRevoked {
		return engine.ErrInvalid
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.writer.ExecContext(ctx, `UPDATE accounts SET auth_state=?,auth_reason=?,updated_at=? WHERE account=?`, st, reason, s.now().Unix(), a)
	return err
}
func encode(v any) string {
	if v == nil {
		return "null"
	}
	b, _ := json.Marshal(v)
	return string(b)
}
func editUnix(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}
