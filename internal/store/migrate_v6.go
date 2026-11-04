package store

import "context"

// migrateV6 retains a reaction revision even when all reactions are cleared.
// Existing v5 state starts at revision zero; no historical events are invented.
func (s *Store) migrateV6(ctx context.Context) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Older migration fixtures can lower user_version while retaining columns.
	// Preserve their already assigned revisions rather than resetting them.
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('reactions') WHERE name='version')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE reactions ADD COLUMN version INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=6`); err != nil {
		return err
	}
	if err := s.fail("migration_v6_before_commit"); err != nil {
		return err
	}
	return tx.Commit()
}
