package gorm

import (
	"testing"

	gormlib "gorm.io/gorm"
)

// OpenEmbeddingStatsFixture is available only to external tests and always
// returns the identity-verified, fully migrated Docker fixture.
func OpenEmbeddingStatsFixture(t *testing.T) *gormlib.DB {
	t.Helper()
	return openDockerMigrationDB(t)
}
