package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestIngestDocument_StoresChunksWithoutBulkOpSnapshot(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping ingest snapshot contract")
	}
	store, err := gormdb.NewStore(gormdb.Config{DSN: dsn, MaxConns: 2})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	project := fmt.Sprintf("ingest-no-snapshot-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		require.NoError(t, store.DB.Unscoped().Exec("DELETE FROM memories WHERE project = ?", project).Error)
	})

	var before int64
	require.NoError(t, store.DB.Table("bulk_op_snapshots").Count(&before).Error)
	server := NewServer(ServerOptions{Version: "mb1-ingest-contract"})
	server.SetMemoryStore(gormdb.NewMemoryStore(store))
	out, err := server.handleIngest(context.Background(), json.RawMessage(fmt.Sprintf(`{
		"action":"ingest",
		"content":"first paragraph\n\nsecond paragraph",
		"source_title":"MB1 live ingest",
		"project":%q,
		"chunk_strategy":"paragraphs"
	}`, project)))
	require.NoError(t, err)

	var response map[string]any
	require.NoError(t, json.Unmarshal([]byte(out), &response))
	require.Equal(t, float64(2), response["stored"])
	var memories, after int64
	require.NoError(t, store.DB.Model(&gormdb.Memory{}).Where("project = ?", project).Count(&memories).Error)
	require.EqualValues(t, 2, memories)
	require.NoError(t, store.DB.Table("bulk_op_snapshots").Count(&after).Error)
	require.Equal(t, before, after)
}
