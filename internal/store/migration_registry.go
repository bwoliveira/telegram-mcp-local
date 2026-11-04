package store

import (
	"context"
	"fmt"
)

const currentSchemaVersion = 9

type schemaMigration struct {
	version int
	apply   func(*Store, context.Context) error
}

// This registry is the sequence used by Open, not a separate test-only list.
// Keep published versions stable: v7 is content identity, v8 is panel indexes.
func storeMigrations() []schemaMigration {
	return []schemaMigration{
		{1, (*Store).migrateV1},
		{2, (*Store).migrateV2},
		{3, (*Store).migrateV3},
		{4, (*Store).migrateV4},
		{5, (*Store).migrateV5},
		{6, (*Store).migrateV6},
		{7, (*Store).migrateV7},
		{8, (*Store).migrateV8},
		{9, (*Store).migrateV9},
	}
}

func validateMigrationSequence(migrations []schemaMigration) error {
	if len(migrations) != currentSchemaVersion {
		return fmt.Errorf("store: migration registry has %d entries; want %d", len(migrations), currentSchemaVersion)
	}
	for i, migration := range migrations {
		if migration.version != i+1 || migration.apply == nil {
			return fmt.Errorf("store: invalid migration registry entry %d (version %d); want version %d and a migration function", i, migration.version, i+1)
		}
	}
	return nil
}
