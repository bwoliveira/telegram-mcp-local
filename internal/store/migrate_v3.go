package store

import "context"

// Legacy state_lost rows deliberately receive no anchor. The checkpoint beside
// an old row may already be getState's adopted head, not the lost baseline.
// Never backfill provenance from that head, timestamps, or a generic trusted bit.
func (s *Store) migrateV3(ctx context.Context) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Column discovery also supports fixture migrations that lower user_version
	// while retaining a newer schema. It never invents evidence for old rows.
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(gaps)`)
	if err != nil {
		return err
	}
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var def any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &def, &pk); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range []string{"recovery_anchor", "recovery_cursor"} {
		if !columns[name] {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE gaps ADD COLUMN `+name+` TEXT`); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=3`); err != nil {
		return err
	}
	if err := s.fail("migration_v3_before_commit"); err != nil {
		return err
	}
	return tx.Commit()
}
