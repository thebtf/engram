package datamodel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/thebtf/engram/internal/db/gorm/migrationmeta"
)

func TestDeriveFromMigrationsFileIncludesRegisteredConstructorInOtherFile(t *testing.T) {
	dir := t.TempDir()
	registration := filepath.Join(dir, "migrations.go")
	for path, source := range map[string]string{
		registration: `package gorm
func runMigrations() {
	gormigrate.New(db, options, []*gormigrate.Migration{
		{ID: "001_first", Migrate: func(tx *DB) error {
			return tx.Exec("CREATE TABLE first_table (id INT)").Error
		}},
		secondMigration(),
	})
}`,
		filepath.Join(dir, "migration_second.go"): `package gorm
func secondMigration() *gormigrate.Migration {
	return &gormigrate.Migration{ID: "002_second", Migrate: func(tx *DB) error {
		return tx.Exec("CREATE TABLE second_table (id INT)").Error
	}}
}
func unregisteredMigration() *gormigrate.Migration {
	return &gormigrate.Migration{ID: "003_unregistered", Migrate: func(tx *DB) error {
		return tx.Exec("CREATE TABLE decoy_table (id INT)").Error
	}}
}`,
	} {
		if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := DeriveFromMigrationsFile(registration)
	if err != nil {
		t.Fatal(err)
	}
	if got.MigrationCount != 2 || got.LiveTableCount != 2 ||
		!strings.Contains(got.Block, "| `second_table` | `002_second` |") ||
		strings.Contains(got.Block, "decoy_table") {
		t.Fatalf("registered separate-file migration missing or unregistered migration included: %+v", got)
	}
	actualPath := filepath.Join("..", "..", "..", "internal", "db", "gorm", "migrations.go")
	actual, err := migrationmeta.ParseRegisteredFile(actualPath)
	if err != nil {
		t.Fatal(err)
	}
	maxID, foundCatalog := 0, false
	for _, migration := range actual.Migrations {
		if migration.ID == "182_workspace_catalog_display_metadata" {
			foundCatalog = true
		}
		if migration.NumericID > maxID {
			maxID = migration.NumericID
		}
	}
	if !foundCatalog {
		t.Fatal("registered migration 182 from the separate workspace catalog file was omitted")
	}
	t.Logf("registered migrations: %d; highest numeric ID: %d; workspace catalog 182: present", len(actual.Migrations), maxID)
}
