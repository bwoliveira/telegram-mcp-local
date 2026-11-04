package store

import "context"

// The covering index keeps local status/activity scans off wide message rows.
// It contains only existing metadata, not text, media locators or transcripts.
const panelStatusIndexSQL = `CREATE INDEX IF NOT EXISTS messages_panel_status
 ON messages(account,peer_type,peer_id,msg_id,expires_at,ttl_period,date,out,first_seen_at)`

func (s *Store) migrateV8(ctx context.Context) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, panelStatusIndexSQL); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version=8`); err != nil {
		return err
	}
	if err = s.fail("migration_v8_before_commit"); err != nil {
		return err
	}
	return tx.Commit()
}
