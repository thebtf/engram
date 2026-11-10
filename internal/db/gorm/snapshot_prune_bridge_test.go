package gorm

import (
	"testing"

	gormlib "gorm.io/gorm"
)

// OpenSnapshotPruneFixture exposes only an identity-verified disposable test database.
func OpenSnapshotPruneFixture(t *testing.T) *gormlib.DB {
	t.Helper()
	return openDockerMigrationDB(t)
}
