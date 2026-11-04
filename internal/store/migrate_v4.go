package store

import "context"

// migrateV4 repairs only the index classification when retained JSON contains
// Telegram subtype evidence. Older converters stored no document attributes;
// MIME types, filenames, dimensions and durations cannot recover those missing
// semantics. Unknown rows remain documents until an authoritative remote read.
func (s *Store) migrateV4(ctx context.Context) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// JSON is sanitized before extraction so malformed legacy payloads are a
	// conservative no-op. json_set preserves every unrelated/unknown field.
	// Attribute precedence matches convertMessage: sticker > animated > video
	// (round_message) or audio (voice). Flags alone require their parent attribute.
	_, err = tx.ExecContext(ctx, `WITH candidates AS MATERIALIZED (
 SELECT m.row_id, CASE WHEN json_valid(m.media_json) THEN m.media_json ELSE '{}' END AS payload
 FROM messages m
 WHERE m.media_type='document' AND m.media_ttl=0
 AND (m.expires_at IS NULL OR m.expires_at>?)
 AND NOT EXISTS(SELECT 1 FROM message_tombstones t WHERE t.account=m.account AND t.peer_type=m.peer_type AND t.peer_id=m.peer_id AND t.msg_id=m.msg_id)
 AND (m.peer_type='channel' OR NOT EXISTS(SELECT 1 FROM pending_deletes d WHERE d.account=m.account AND d.msg_id=m.msg_id))
), classified AS MATERIALIZED (
 SELECT row_id, CASE
 WHEN json_extract(payload,'$.type') IN ('video','video_note','audio','voice','sticker','animation') THEN json_extract(payload,'$.type')
 WHEN coalesce(json_extract(payload,'$.type'),'document')<>'document' THEN NULL
 WHEN json_type(payload,'$.document_attributes.sticker')='true' THEN 'sticker'
 WHEN json_type(payload,'$.document_attributes.animated')='true' THEN 'animation'
 WHEN json_type(payload,'$.document_attributes.video')='true' THEN
  CASE WHEN json_type(payload,'$.document_attributes.round_message')='true' THEN 'video_note' ELSE 'video' END
 WHEN json_type(payload,'$.document_attributes.audio')='true' THEN
  CASE WHEN json_type(payload,'$.document_attributes.voice')='true' THEN 'voice' ELSE 'audio' END
 ELSE NULL END AS subtype
 FROM candidates WHERE coalesce(json_extract(payload,'$.ttl_seconds'),0)=0
)
UPDATE messages SET media_type=c.subtype,media_json=json_set(media_json,'$.type',c.subtype)
FROM classified c WHERE messages.row_id=c.row_id AND c.subtype IS NOT NULL`, s.now().Unix())
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version=4`); err != nil {
		return err
	}
	if err = s.fail("migration_v4_before_commit"); err != nil {
		return err
	}
	return tx.Commit()
}
