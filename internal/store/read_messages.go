package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
	"github.com/bwoliveira/telegram-mcp-local/internal/state"
)

// localVisibleMessage is shared by all unpaginated message/cache seams. Both
// TTL representations are authoritative, even before the janitor repairs or
// removes an inconsistent row. Media TTL is not a message expiry.
const localVisibleMessage = `m.account=?
 AND (m.expires_at IS NULL OR m.expires_at>?)
 AND (m.ttl_period<=0 OR m.date+m.ttl_period>?)
 AND NOT EXISTS (SELECT 1 FROM message_tombstones d WHERE d.account=m.account AND d.peer_type=m.peer_type AND d.peer_id=m.peer_id AND d.msg_id=m.msg_id)
 AND NOT EXISTS (SELECT 1 FROM pending_deletes d WHERE d.account=m.account AND d.msg_id=m.msg_id AND m.peer_type<>'channel')`

// ReadMessages returns every visible local message, without the legacy page
// cap. The scanner never restores Media.Remote (unlike the engine's Message).
func (s *Store) ReadMessages(ctx context.Context, account engine.AccountID) ([]engine.Message, error) {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return nil, err
	}
	now := s.now().Unix()
	return s.readLocalMessages(ctx, localVisibleMessage, []any{account, now, now})
}

func (s *Store) readLocalMessages(ctx context.Context, where string, args []any) ([]engine.Message, error) {
	rows, err := s.reader.QueryContext(ctx, `SELECT `+messageColumns+` FROM messages m WHERE `+where+` ORDER BY m.date,m.peer_type,m.peer_id,m.msg_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []engine.Message{}
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SearchLocal performs actual FTS5 matching over text and transcripts. It
// returns the same safe message projection/visibility as ReadMessages, with
// no fallback to substring search and no silent 100/1000-result ceiling.
func (s *Store) SearchLocal(ctx context.Context, account engine.AccountID, query string) ([]engine.Message, error) {
	if err := s.checkReadAccount(ctx, account); err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return nil, engine.ErrInvalid
	}
	expression := "{text transcript} : (" + query + ")"
	// Validate the original expression, not its column-restricted wrapper:
	// unmatched parentheses must not close our wrapper and escape into sender.
	// Preserve context/storage failures while hiding SQLite's echo of malformed
	// user expressions in public diagnostics.
	var probe int
	err := s.reader.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ? LIMIT 1)`, query).Scan(&probe)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if strings.Contains(err.Error(), "fts5") || strings.Contains(err.Error(), "syntax error") || strings.Contains(err.Error(), "unterminated") || strings.Contains(err.Error(), "no such column") || strings.Contains(err.Error(), "expected integer") {
			return nil, fmt.Errorf("store: invalid full-text expression: %w", engine.ErrInvalid)
		}
		return nil, err
	}
	now := s.now().Unix()
	return s.readLocalMessages(ctx, localVisibleMessage+` AND m.row_id IN (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)`, []any{account, now, now, expression})
}

// ReadTranscript reads only the durable cache for a complete typed message
// key. Joining the authoritative visible message prevents orphaned/stale FTS
// or transcript data from reviving deleted or expired content. Access loss
// does not remove previously cached history. No downloader/transcriber runs.
func (s *Store) ReadTranscript(ctx context.Context, key engine.MessageKey) (state.Transcript, bool, error) {
	var out state.Transcript
	if err := s.checkReadAccount(ctx, key.Account); err != nil {
		return out, false, err
	}
	if err := validKey(s.account, key); err != nil {
		return out, false, err
	}
	now := s.now().Unix()
	var created int64
	err := s.reader.QueryRowContext(ctx, `SELECT t.text,coalesce(t.raw_text,''),t.language,t.model,t.source,t.created_at FROM messages m JOIN transcripts t ON t.row_id=m.row_id WHERE `+localVisibleMessage+` AND m.peer_type=? AND m.peer_id=? AND m.msg_id=? AND m.media_ttl=0`, key.Account, now, now, key.Peer.Type, key.Peer.ID, key.ID).Scan(&out.Text, &out.Raw, &out.Language, &out.Model, &out.Source, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return state.Transcript{}, false, nil
	}
	if err != nil {
		return state.Transcript{}, false, err
	}
	out.ChatJID = fmt.Sprintf("%s:%d", key.Peer.Type, key.Peer.ID)
	out.MessageID = strconv.Itoa(key.ID)
	out.CreatedAt = time.Unix(created, 0).UTC()
	return out, true, nil
}
