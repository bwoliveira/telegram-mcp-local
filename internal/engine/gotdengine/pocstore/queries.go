package pocstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

var _ engine.Persister = (*Store)(nil)

type peerRecord struct {
	Peer engine.Peer
	Hash int64
	Min  bool
}
type messageRecord struct {
	Message engine.Message
	Remote  []byte
}
type accountRecord struct {
	Self   engine.Account
	Auth   engine.AuthState
	Reason string
}

func validPeer(p engine.PeerRef) bool {
	return p.ID > 0 && (p.Type == engine.PeerUser || p.Type == engine.PeerChat || p.Type == engine.PeerChannel)
}
func validKey(acc engine.AccountID, k engine.MessageKey) error {
	if k.Account != acc {
		return engine.ErrAccountMismatch
	}
	if !validPeer(k.Peer) || k.ID <= 0 {
		return engine.ErrInvalid
	}
	return nil
}

func (s *Store) PatchUpdatesState(ctx context.Context, acc engine.AccountID, p engine.UpdatesPatch) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var st engine.UpdatesState
		_, ok, err := load(ctx, tx, acc, "updates", "", &st)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("pocstore: checkpoint missing: %w", engine.ErrInvalid)
		}
		if p.Pts != nil {
			st.Pts = *p.Pts
		}
		if p.Qts != nil {
			st.Qts = *p.Qts
		}
		if p.Date != nil {
			st.Date = *p.Date
		}
		if p.Seq != nil {
			st.Seq = *p.Seq
		}
		_, err = put(ctx, tx, acc, "updates", "", st)
		return err
	})
}
func (s *Store) LoadChannelPts(ctx context.Context, acc engine.AccountID, id int64) (int, bool, error) {
	var pts int
	_, ok, err := load(ctx, s.db, acc, "channel", key(id), &pts)
	return pts, ok, err
}
func (s *Store) SaveChannelPts(ctx context.Context, acc engine.AccountID, id int64, pts int) error {
	if id <= 0 {
		return engine.ErrInvalid
	}
	_, err := put(ctx, s.db, acc, "channel", key(id), pts)
	return err
}
func (s *Store) ForEachChannelPts(ctx context.Context, acc engine.AccountID, fn func(int64, int) error) error {
	rs, err := records(ctx, s.db, acc, "channel")
	if err != nil {
		return err
	}
	// Release the connection before callbacks: adapters may query AccessHash.
	for _, r := range rs {
		var id int64
		var pts int
		if err = json.Unmarshal([]byte(r.key), &id); err != nil {
			return err
		}
		if err = json.Unmarshal(r.body, &pts); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = fn(id, pts); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) DropChannelState(ctx context.Context, acc engine.AccountID, id int64) error {
	_, err := remove(ctx, s.db, acc, "channel", key(id))
	return err
}
func (s *Store) ResetUpdates(ctx context.Context, acc engine.AccountID) error {
	return s.transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM poc_records WHERE account=? AND kind IN ('updates','channel')", acc)
		return err
	})
}
func (s *Store) AccessHash(ctx context.Context, acc engine.AccountID, p engine.PeerRef) (int64, bool, error) {
	var r peerRecord
	_, ok, err := load(ctx, s.db, acc, "peer", key(p), &r)
	if !ok || r.Min || r.Hash == 0 {
		return 0, false, err
	}
	return r.Hash, true, err
}
func (s *Store) LookupPeer(ctx context.Context, acc engine.AccountID, p engine.PeerRef) (engine.Peer, bool, error) {
	var r peerRecord
	_, ok, err := load(ctx, s.db, acc, "peer", key(p), &r)
	return r.Peer, ok, err
}
func (s *Store) FindPeers(ctx context.Context, acc engine.AccountID, query string, limit int) ([]engine.Peer, error) {
	rs, err := records(ctx, s.db, acc, "peer")
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(query), "@"))
	if limit <= 0 {
		limit = 100
	}
	out := make([]engine.Peer, 0)
	for _, r := range rs {
		var p peerRecord
		if err = json.Unmarshal(r.body, &p); err != nil {
			return nil, err
		}
		v := p.Peer
		text := strings.Join(append([]string{v.Title, v.FirstName, v.LastName, v.Username, v.Phone, fmt.Sprintf("%s:%d", v.Ref.Type, v.Ref.ID)}, v.Usernames...), " ")
		if strings.Contains(strings.ToLower(text), q) {
			out = append(out, v)
			if len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}
func (s *Store) Message(ctx context.Context, k engine.MessageKey) (engine.Message, bool, error) {
	var r messageRecord
	_, ok, err := load(ctx, s.db, k.Account, "message", key(k), &r)
	if err != nil || !ok {
		return engine.Message{}, ok, err
	}
	if r.Message.Media != nil {
		r.Message.Media.Remote = r.Remote
	}
	return r.Message, true, nil
}
func resolve(ctx context.Context, db database, acc engine.AccountID, ids []int) ([]engine.MessageKey, error) {
	rs, err := records(ctx, db, acc, "message")
	if err != nil {
		return nil, err
	}
	wanted := make(map[int]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	out := make([]engine.MessageKey, 0)
	for _, r := range rs {
		var k engine.MessageKey
		if err = json.Unmarshal([]byte(r.key), &k); err != nil {
			return nil, err
		}
		if k.Peer.Type != engine.PeerChannel && wanted[k.ID] {
			out = append(out, k)
		}
	}
	return out, nil
}
func (s *Store) ResolveMessageIDs(ctx context.Context, acc engine.AccountID, ids []int) ([]engine.MessageKey, error) {
	return resolve(ctx, s.db, acc, ids)
}
func (s *Store) Cursor(ctx context.Context, acc engine.AccountID, p engine.PeerRef) (engine.Cursor, bool, error) {
	var c engine.Cursor
	_, ok, err := load(ctx, s.db, acc, "cursor", key(p), &c)
	return c, ok, err
}
func (s *Store) SelfID(ctx context.Context, acc engine.AccountID) (int64, error) {
	var r accountRecord
	_, _, err := load(ctx, s.db, acc, "account", "", &r)
	return r.Self.SelfID, err
}
func (s *Store) BindSelf(ctx context.Context, acc engine.AccountID, self engine.Account) error {
	if self.SelfID <= 0 || acc == "" {
		return engine.ErrInvalid
	}
	if self.ID != "" && self.ID != acc {
		return engine.ErrAccountMismatch
	}
	self.ID = acc
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var r accountRecord
		_, _, err := load(ctx, tx, acc, "account", "", &r)
		if err != nil {
			return err
		}
		if r.Self.SelfID != 0 && r.Self.SelfID != self.SelfID {
			return engine.ErrAccountMismatch
		}
		r.Self = self
		_, err = put(ctx, tx, acc, "account", "", r)
		return err
	})
}
func (s *Store) SetAuthState(ctx context.Context, acc engine.AccountID, st engine.AuthState, reason string) error {
	if st != engine.AuthNone && st != engine.AuthAuthorized && st != engine.AuthLoggedOut && st != engine.AuthRevoked {
		return engine.ErrInvalid
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		var r accountRecord
		_, _, err := load(ctx, tx, acc, "account", "", &r)
		if err != nil {
			return err
		}
		r.Auth = st
		r.Reason = reason
		_, err = put(ctx, tx, acc, "account", "", r)
		return err
	})
}

// Dialogs returns persisted server counters, without marking anything as read.
func (s *Store) Dialogs(ctx context.Context, acc engine.AccountID) ([]engine.Dialog, error) {
	rs, err := records(ctx, s.db, acc, "dialog")
	if err != nil {
		return nil, err
	}
	out := make([]engine.Dialog, 0, len(rs))
	for _, r := range rs {
		var d engine.Dialog
		if err = json.Unmarshal(r.body, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].TopMessageID > out[j].TopMessageID
	})
	return out, nil
}
