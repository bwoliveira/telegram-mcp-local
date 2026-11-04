package store

import (
	"context"
	"testing"
)

func TestMigrationRegistryIsUniqueAndContiguous(t *testing.T) {
	if currentSchemaVersion != 9 {
		t.Fatalf("combined schema version=%d; want 9", currentSchemaVersion)
	}
	if err := validateMigrationSequence(storeMigrations()); err != nil {
		t.Fatal(err)
	}
}

func TestMigrationRegistryRejectsInvalidSequences(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func([]schemaMigration) []schemaMigration
	}{
		{"empty", func(m []schemaMigration) []schemaMigration { return nil }},
		{"duplicate v7", func(m []schemaMigration) []schemaMigration { m[7].version = 7; return m }},
		{"missing middle", func(m []schemaMigration) []schemaMigration { return append(m[:3], m[4:]...) }},
		{"missing latest", func(m []schemaMigration) []schemaMigration { return m[:len(m)-1] }},
		{"gap same length", func(m []schemaMigration) []schemaMigration { m[3].version = 9; return m }},
		{"out of order", func(m []schemaMigration) []schemaMigration { m[3], m[4] = m[4], m[3]; return m }},
		{"zero version", func(m []schemaMigration) []schemaMigration { m[0].version = 0; return m }},
		{"nil migration", func(m []schemaMigration) []schemaMigration { m[6].apply = nil; return m }},
		{"unexpected extra", func(m []schemaMigration) []schemaMigration {
			return append(m, schemaMigration{version: currentSchemaVersion + 1, apply: func(*Store, context.Context) error { return nil }})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateMigrationSequence(test.edit(storeMigrations())); err == nil {
				t.Fatal("invalid migration sequence accepted")
			}
		})
	}
}
