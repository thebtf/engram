package gorm

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/tools/gen-data-model/datamodel"
)

func TestDataModelGeneratedTablesBlockMatchesMigrations(t *testing.T) {
	root := repositoryRoot(t)
	derivation, err := datamodel.DeriveFromMigrationsFile(filepath.Join(root, "internal", "db", "gorm", "migrations.go"))
	require.NoError(t, err)
	require.Greater(t, derivation.MigrationCount, 96, "RED guardrail defect: migration count should expose DATA_MODEL.md's stale 96 count")
	require.NotEqual(t, 25, derivation.LiveTableCount, "RED guardrail defect: live table count should expose DATA_MODEL.md's stale 25 count")

	doc, err := os.ReadFile(filepath.Join(root, "docs", "arch", "DATA_MODEL.md"))
	require.NoError(t, err)
	actual, ok := datamodel.ExtractGeneratedBlock(string(doc))
	require.True(t, ok, "DATA_MODEL.md missing generated tables block; expected %d migrations and %d live tables", derivation.MigrationCount, derivation.LiveTableCount)
	require.Equal(t, derivation.Block, actual, "DATA_MODEL.md generated table block is stale")
}
