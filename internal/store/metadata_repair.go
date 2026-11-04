package store

import (
	"context"
	"database/sql"

	"github.com/bwoliveira/telegram-mcp-local/internal/engine"
)

func richMediaType(t engine.MediaType) bool {
	switch t {
	case engine.MediaPoll, engine.MediaGeo, engine.MediaLiveGeo, engine.MediaVenue, engine.MediaContact, engine.MediaDice, engine.MediaStory, engine.MediaWebPage:
		return true
	default:
		return false
	}
}

func documentSubtype(t engine.MediaType) bool {
	switch t {
	case engine.MediaVideo, engine.MediaVideoNote, engine.MediaAudio, engine.MediaVoice, engine.MediaSticker, engine.MediaAnimation:
		return true
	default:
		return false
	}
}

// repairDocumentSubtype only reclassifies a generic document. The caller has
// already checked revision ordering and tombstones. Never replace attachment
// content, credentials, lifecycle policy, timestamps or event versions.
func repairDocumentSubtype(ctx context.Context, tx *sql.Tx, a engine.AccountID, m engine.Message, now int64) error {
	if m.Media == nil || m.Media.TTLSeconds != 0 || !documentSubtype(m.Media.Type) {
		return nil
	}
	k := m.Key
	_, err := tx.ExecContext(ctx, `UPDATE messages SET media_type=?,media_json=json_set(media_json,'$.type',?)
 WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=? AND media_type='document'
 AND media_ttl=0 AND (expires_at IS NULL OR expires_at>?)
 AND CASE WHEN json_valid(media_json) THEN coalesce(json_extract(media_json,'$.ttl_seconds'),0)=0 ELSE 0 END`,
		m.Media.Type, m.Media.Type, a, k.Peer.Type, k.Peer.ID, k.ID, now)
	return err
}

// enrichMessageMetadata repairs copies indexed by older converters. It only
// fills unknown metadata at the same content revision; it must never replace
// newer reaction snapshots, message content, or the original lifecycle policy.
func enrichMessageMetadata(ctx context.Context, tx *sql.Tx, a engine.AccountID, m engine.Message, now int64) error {
	k := m.Key
	if err := repairDocumentSubtype(ctx, tx, a, m, now); err != nil {
		return err
	}
	if m.Reactions == nil && (m.Media == nil || !richMediaType(m.Media.Type)) {
		return nil
	}
	old, err := scanMessage(tx.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM messages m WHERE m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=? AND (m.expires_at IS NULL OR m.expires_at>?)`, a, k.Peer.Type, k.Peer.ID, k.ID, now))
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if old.Media != nil && old.Media.TTLSeconds > 0 || m.Media != nil && m.Media.TTLSeconds > 0 {
		return nil
	}
	if old.Media != nil && m.Media != nil && richMediaType(m.Media.Type) && (old.Media.Type == engine.MediaOther || old.Media.Type == m.Media.Type) {
		before := encode(old.Media) + encode(old.Location) + encode(old.Poll)
		old.Media.Type = m.Media.Type
		if old.Media.Contact == nil {
			old.Media.Contact = m.Media.Contact
		}
		if old.Media.Dice == nil {
			old.Media.Dice = m.Media.Dice
		}
		if old.Media.Story == nil {
			old.Media.Story = m.Media.Story
		}
		if old.Media.WebPage == nil {
			old.Media.WebPage = m.Media.WebPage
		}
		if old.Location == nil {
			old.Location = m.Location
		}
		if old.Poll == nil {
			old.Poll = m.Poll
			if old.Text == "" && m.Poll != nil {
				old.Text = m.Poll.Question
			}
		}
		if before != encode(old.Media)+encode(old.Location)+encode(old.Poll) {
			if _, err := tx.ExecContext(ctx, `UPDATE messages SET media_type=?,media_json=?,location_json=?,poll_json=?,text=?,updated_at=? WHERE account=? AND peer_type=? AND peer_id=? AND msg_id=?`, old.Media.Type, encode(old.Media), encode(old.Location), encode(old.Poll), old.Text, now, a, k.Peer.Type, k.Peer.ID, k.ID); err != nil {
				return err
			}
		}
	}
	if m.Reactions != nil {
		var known bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM reactions r JOIN messages m ON m.row_id=r.row_id WHERE m.account=? AND m.peer_type=? AND m.peer_id=? AND m.msg_id=?)`, a, k.Peer.Type, k.Peer.ID, k.ID).Scan(&known); err != nil {
			return err
		}
		if !known {
			return applyMessageReactions(ctx, tx, a, m, now, &engine.Applied{})
		}
	}
	return nil
}
