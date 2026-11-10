package mcp

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	dbgorm "github.com/thebtf/engram/internal/db/gorm"
)

// t007TestEnv carries the live MCP Server plus the raw *gorm.DB handle used
// by integration fixtures that need to insert via raw SQL (the MemoryStore
// public surface does not expose the underlying DB).
type t007TestEnv struct {
	srv   *Server
	store *dbgorm.Store
}

// newMemoryServerForT007 builds an MCP Server bound to a live PostgreSQL +
// runs migrations. Matches the worker.newMemoryTestService pattern but lives
// at the MCP layer because T007/T008 functional surface is MCP (T004 path
// drift Aligned note).
//
// DSN-gated per existing convention; tests skip when DATABASE_DSN is unset.
// Cleanup deletes rows with the per-test project before closing the store.
func newMemoryServerForT007(t *testing.T, project string) t007TestEnv {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping integration test")
	}

	store, err := dbgorm.NewStore(dbgorm.Config{DSN: dsn, MaxConns: 2})
	if err != nil {
		t.Fatalf("open DATABASE_DSN test store: %v", err)
	}

	memoryStore := dbgorm.NewMemoryStore(store)

	srv := NewServer(ServerOptions{Version: "test"})
	srv.memoryStore = memoryStore

	t.Cleanup(func() {
		require.NoError(t, store.DB.WithContext(context.Background()).
			Exec(`DELETE FROM memories WHERE project = ?`, project).Error)
		require.NoError(t, store.DB.Where("id = ?", project).Delete(&dbgorm.Project{}).Error)
		require.NoError(t, store.Close())
	})

	return t007TestEnv{srv: srv, store: store}
}
