package store

import "context"

// migrateV7 retains policy-independent content/attachment identity. Historical
// rows remain unknown: public filenames and old hashes cannot establish IDs.
func (s *Store) migrateV7(ctx context.Context) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('messages') WHERE name='content_identity')`).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN content_identity TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=7`); err != nil {
		return err
	}
	if err := s.fail("migration_v7_before_commit"); err != nil {
		return err
	}
	return tx.Commit()
}
