package store

import "context"

// Permission bitsets are transport-independent integers. Existing snapshots
// default to no known rights; migration must not infer privileges from roles.
func (s *Store) migrateV5(ctx context.Context) error {
	tx, err := s.writer.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Like v3, tolerate fixture databases that lower user_version while
	// retaining newer columns, without overwriting their existing values.
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(participants)`)
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
	for _, name := range []string{"admin_rights", "banned_rights", "banned_until", "can_edit"} {
		if !columns[name] {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE participants ADD COLUMN `+name+` INTEGER NOT NULL DEFAULT 0`); err != nil {
				return err
			}
		}
	}
	// The unreleased v5 schema also separates visible edit markers from server
	// ordering and remembers content identity for hidden reaction refreshes.
	for _, column := range []struct{ name, definition string }{
		{"server_edit_date", "INTEGER NOT NULL DEFAULT 0"},
		{"content_hash", "TEXT NOT NULL DEFAULT ''"},
	} {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pragma_table_info('messages') WHERE name=?)`, column.name).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			if _, err := tx.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN `+column.name+` `+column.definition); err != nil {
				return err
			}
			if column.name == "server_edit_date" {
				if _, err := tx.ExecContext(ctx, `UPDATE messages SET server_edit_date=edit_date WHERE edit_date>0`); err != nil {
					return err
				}
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version=5`); err != nil {
		return err
	}
	if err := s.fail("migration_v5_before_commit"); err != nil {
		return err
	}
	return tx.Commit()
}
