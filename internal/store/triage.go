package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// TriageMark records a local decision about a peer; it never changes Telegram.
type TriageMark struct {
	Peer        engine.PeerRef
	HandledAt   *time.Time
	SnoozedAt   *time.Time
	SnoozeUntil *time.Time
	Note        string
}

// Hides matches upstream triage: a newer incoming message always resurfaces a
// peer, while a snooze expires at (not after) its deadline.
func (m TriageMark) Hides(lastIncoming, now time.Time) bool {
	if m.HandledAt != nil && !lastIncoming.After(*m.HandledAt) {
		return true
	}
	return m.SnoozeUntil != nil && now.Before(*m.SnoozeUntil) && m.SnoozedAt != nil && !lastIncoming.After(*m.SnoozedAt)
}

func (s *Store) checkTriageAccount(ctx context.Context, account engine.AccountID) error {
	if ctx == nil {
		return engine.ErrInvalid
	}
	return s.checkReadAccount(ctx, account)
}

// prepareTriagePeerLocked checks existence on the serialized writer so an Apply
// or peer purge in this Store cannot interleave between validation and mutation.
func (s *Store) prepareTriagePeerLocked(ctx context.Context, account engine.AccountID, peer engine.PeerRef) error {
	var exists bool
	if err := s.writer.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM peers WHERE account=? AND peer_type=? AND peer_id=?)`, account, peer.Type, peer.ID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return engine.ErrPeerNotFound
	}
	return s.ensureTriageLocked(ctx)
}

// ensureTriageLocked is additive local state, not a core schema migration.
// The caller holds writeMu, including when a read first creates the table.
func (s *Store) ensureTriageLocked(ctx context.Context) error {
	_, err := s.writer.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS triage (
		account TEXT NOT NULL,
		peer_type TEXT NOT NULL,
		peer_id INTEGER NOT NULL,
		handled_at INTEGER,
		snoozed_at INTEGER,
		snooze_until INTEGER,
		note TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (account, peer_type, peer_id),
		FOREIGN KEY (account, peer_type, peer_id) REFERENCES peers ON DELETE CASCADE
	) WITHOUT ROWID`)
	return err
}

// SnoozeTriage replaces a handled decision with a local snooze. An exactly empty
// note retains the previous note; expired snoozes remain available as local state.
func (s *Store) SnoozeTriage(ctx context.Context, account engine.AccountID, peer engine.PeerRef, note string, at, until time.Time) error {
	if err := s.checkTriageAccount(ctx, account); err != nil {
		return err
	}
	if !validPeer(peer) {
		return engine.ErrInvalid
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.prepareTriagePeerLocked(ctx, account, peer); err != nil {
		return err
	}
	_, err := s.writer.ExecContext(ctx, `INSERT INTO triage(account,peer_type,peer_id,snoozed_at,snooze_until,note) VALUES(?,?,?,?,?,?)
		ON CONFLICT(account,peer_type,peer_id) DO UPDATE SET handled_at=NULL,snoozed_at=excluded.snoozed_at,snooze_until=excluded.snooze_until,
		note=CASE WHEN excluded.note != '' THEN excluded.note ELSE triage.note END`, account, peer.Type, peer.ID, at.UnixNano(), until.UnixNano(), note)
	return err
}

// ClearTriageMark forgets only the local triage decision for this peer.
func (s *Store) ClearTriageMark(ctx context.Context, account engine.AccountID, peer engine.PeerRef) error {
	if err := s.checkTriageAccount(ctx, account); err != nil {
		return err
	}
	if !validPeer(peer) {
		return engine.ErrInvalid
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.prepareTriagePeerLocked(ctx, account, peer); err != nil {
		return err
	}
	_, err := s.writer.ExecContext(ctx, `DELETE FROM triage WHERE account=? AND peer_type=? AND peer_id=?`, account, peer.Type, peer.ID)
	return err
}

func triageTime(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	at := time.Unix(0, value.Int64).UTC()
	return &at
}

// ReadTriageMarks returns owned local snapshots keyed by the full peer namespace.
func (s *Store) ReadTriageMarks(ctx context.Context, account engine.AccountID) (map[engine.PeerRef]TriageMark, error) {
	if err := s.checkTriageAccount(ctx, account); err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	err := s.ensureTriageLocked(ctx)
	s.writeMu.Unlock()
	if err != nil {
		return nil, err
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT peer_type,peer_id,handled_at,snoozed_at,snooze_until,note FROM triage WHERE account=?`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	marks := make(map[engine.PeerRef]TriageMark)
	for rows.Next() {
		var mark TriageMark
		var handled, snoozed, until sql.NullInt64
		if err := rows.Scan(&mark.Peer.Type, &mark.Peer.ID, &handled, &snoozed, &until, &mark.Note); err != nil {
			return nil, err
		}
		mark.HandledAt, mark.SnoozedAt, mark.SnoozeUntil = triageTime(handled), triageTime(snoozed), triageTime(until)
		marks[mark.Peer] = mark
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return marks, nil
}

// MarkTriageHandled records a local handled decision and lifts any snooze.
// An exactly empty note retains the previous note.
func (s *Store) MarkTriageHandled(ctx context.Context, account engine.AccountID, peer engine.PeerRef, note string, at time.Time) error {
	if err := s.checkTriageAccount(ctx, account); err != nil {
		return err
	}
	if !validPeer(peer) {
		return engine.ErrInvalid
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.prepareTriagePeerLocked(ctx, account, peer); err != nil {
		return err
	}
	_, err := s.writer.ExecContext(ctx, `INSERT INTO triage(account,peer_type,peer_id,handled_at,note) VALUES(?,?,?,?,?)
		ON CONFLICT(account,peer_type,peer_id) DO UPDATE SET handled_at=excluded.handled_at,snoozed_at=NULL,snooze_until=NULL,
		note=CASE WHEN excluded.note != '' THEN excluded.note ELSE triage.note END`, account, peer.Type, peer.ID, at.UnixNano(), note)
	return err
}
