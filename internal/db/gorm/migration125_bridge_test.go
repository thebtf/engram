package gorm

import (
	"testing"

	gormlib "gorm.io/gorm"
)

// Migration125Fixture exposes only a test-owned, identity-verified container to
// the external MCP integration test. It is not part of the production API.
type Migration125Fixture struct {
	db *gormlib.DB
}

func OpenMigration125Fixture(t *testing.T) *Migration125Fixture {
	t.Helper()
	return &Migration125Fixture{db: openDockerMigrationDBTo(t, "121_knowledge_edges_cascade")}
}

func (f *Migration125Fixture) DB() *gormlib.DB { return f.db }

func (f *Migration125Fixture) MigrateRemaining() error { return runMigrations(f.db) }
