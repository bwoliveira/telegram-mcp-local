package store

import (
	"context"
	"database/sql"
	"strings"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

// Keep statements bounded and reuse each shape on the transaction's connection.
// This avoids both one SQL parse per member and SQLite's parameter limit without
// reducing the number of members materialized by a snapshot.
const participantSQLBatch = 100

func participantValues(n, width int) string {
	row := "(" + strings.TrimSuffix(strings.Repeat("?,", width), ",") + ")"
	return strings.TrimSuffix(strings.Repeat(row+",", n), ",")
}

func participantBatchExec(ctx context.Context, tx *sql.Tx, prefix, suffix string, width int, args []any) error {
	statements := make(map[int]*sql.Stmt)
	defer func() {
		for _, stmt := range statements {
			stmt.Close()
		}
	}()
	for len(args) > 0 {
		n := min(participantSQLBatch, len(args)/width)
		stmt := statements[n]
		if stmt == nil {
			var err error
			stmt, err = tx.PrepareContext(ctx, prefix+participantValues(n, width)+suffix)
			if err != nil {
				return err
			}
			statements[n] = stmt
		}
		if _, err := stmt.ExecContext(ctx, args[:n*width]...); err != nil {
			return err
		}
		args = args[n*width:]
	}
	return nil
}

type participantPeerMetadata struct {
	title, first, last, username, phone string
	usernameKnown                       bool
}

// Participant snapshots carry only first-name and username observations, never
// peer access hashes, kind, contact status or security flags. Read just the
// metadata needed for that sparse merge in bounded set queries. In particular,
// do not turn an unknown/empty observation into a metadata revocation.
func participantPeerMetadataFor(ctx context.Context, tx *sql.Tx, a engine.AccountID, members []engine.Participant) (map[engine.PeerRef]participantPeerMetadata, error) {
	old := make(map[engine.PeerRef]participantPeerMetadata)
	statements := make(map[int]*sql.Stmt)
	defer func() {
		for _, stmt := range statements {
			stmt.Close()
		}
	}()
	for len(members) > 0 {
		n := min(participantSQLBatch, len(members))
		stmt := statements[n]
		if stmt == nil {
			var err error
			stmt, err = tx.PrepareContext(ctx, `WITH wanted(peer_type,peer_id,username) AS (VALUES `+participantValues(n, 3)+`)
SELECT p.peer_type,p.peer_id,p.title,p.first_name,p.last_name,p.username,p.phone,
 (wanted.username='' OR EXISTS(SELECT 1 FROM peer_usernames u WHERE u.account=p.account AND u.peer_type=p.peer_type AND u.peer_id=p.peer_id AND u.username=wanted.username COLLATE BINARY))
FROM wanted JOIN peers p ON p.account=? AND p.peer_type=wanted.peer_type AND p.peer_id=wanted.peer_id`)
			if err != nil {
				return nil, err
			}
			statements[n] = stmt
		}
		args := make([]any, 0, n*3+1)
		for _, v := range members[:n] {
			if !validPeer(v.Peer) {
				return nil, engine.ErrInvalid
			}
			args = append(args, v.Peer.Type, v.Peer.ID, v.Username)
		}
		args = append(args, a)
		rows, err := stmt.QueryContext(ctx, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var ref engine.PeerRef
			var m participantPeerMetadata
			if err = rows.Scan(&ref.Type, &ref.ID, &m.title, &m.first, &m.last, &m.username, &m.phone, &m.usernameKnown); err != nil {
				rows.Close()
				return nil, err
			}
			old[ref] = m
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		members = members[n:]
	}
	return old, nil
}

func insertParticipantMembers(ctx context.Context, tx *sql.Tx, a engine.AccountID, p engine.ParticipantsSnapshot, now int64) error {
	old, err := participantPeerMetadataFor(ctx, tx, a, p.Participants)
	if err != nil {
		return err
	}
	peerArgs := make([]any, 0, len(p.Participants)*8)
	aliasArgs := make([]any, 0)
	memberArgs := make([]any, 0, len(p.Participants)*12)
	for _, v := range p.Participants {
		m, exists := old[v.Peer]
		changed := !exists || v.Name != "" && v.Name != m.first || v.Username != "" && (v.Username != m.username || !m.usernameKnown)
		if v.Name != "" {
			m.first = v.Name
		}
		if v.Username != "" {
			m.username = v.Username
		}
		if changed {
			search := normalize(strings.Join([]string{m.title, m.first, m.last, m.username, m.phone}, " "))
			peerArgs = append(peerArgs, a, v.Peer.Type, v.Peer.ID, defaultKind(v.Peer), m.first, m.username, search, now)
		}
		if v.Username != "" {
			aliasArgs = append(aliasArgs, a, v.Peer.Type, v.Peer.ID, strings.TrimPrefix(v.Username, "@"))
		}
		memberArgs = append(memberArgs, a, p.Group.Type, p.Group.ID, v.Peer.Type, v.Peer.ID, v.Role, nullableTime(v.JoinedAt), v.AdminRights, v.BannedRights, v.BannedUntil, v.CanEdit, now)
	}
	// On conflict touch only the two observed fields and their derived search key.
	// The metadata was merged above on this same transaction/connection.
	if err = participantBatchExec(ctx, tx, `INSERT INTO peers(account,peer_type,peer_id,kind,first_name,username,search_name,updated_at) VALUES `,
		` ON CONFLICT(account,peer_type,peer_id) DO UPDATE SET first_name=excluded.first_name,username=excluded.username,search_name=excluded.search_name,updated_at=excluded.updated_at`, 8, peerArgs); err != nil {
		return err
	}
	if err = participantBatchExec(ctx, tx, `INSERT OR IGNORE INTO peer_usernames(account,peer_type,peer_id,username) VALUES `, "", 4, aliasArgs); err != nil {
		return err
	}
	return participantBatchExec(ctx, tx, `INSERT INTO participants(account,group_type,group_id,user_type,user_id,role,joined_at,admin_rights,banned_rights,banned_until,can_edit,updated_at) VALUES `, "", 12, memberArgs)
}
