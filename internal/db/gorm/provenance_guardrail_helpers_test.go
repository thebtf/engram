package gorm

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/db/gorm/migrationmeta"
)

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "resolve test helper path")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func migrationSchema(t *testing.T) *migrationmeta.Schema {
	t.Helper()
	schema, err := migrationmeta.ParseFile(filepath.Join(repositoryRoot(t), "internal", "db", "gorm", "migrations.go"))
	require.NoError(t, err)
	require.NotEmpty(t, schema.Migrations, "migrations.go parser must discover gormigrate migrations")
	require.NotEmpty(t, schema.LiveTables(), "migrations.go parser must discover live tables")
	return schema
}
