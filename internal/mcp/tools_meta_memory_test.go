package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

type deferredMetaMemoryIndex struct{ calls int }

func (index *deferredMetaMemoryIndex) QueryMetaIndex(context.Context, gormdb.MetaIndexQuery) ([]gormdb.MetaIndexHit, error) {
	index.calls++
	return nil, nil
}

func TestKnowAbout_RetiredFlagsCannotActivateDeferredDiscovery(t *testing.T) {
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S2_METAMEM", "true")
	index := &deferredMetaMemoryIndex{}
	server := NewServer(ServerOptions{Version: "test"})
	server.SetMetaMemoryIndex(index)
	for _, tool := range server.ListTools() {
		require.NotEqual(t, "know_about", tool.Name)
	}
	_, err := server.handleKnowAbout(context.Background(), json.RawMessage(`{"topic":"retained","project":"project"}`))
	require.ErrorContains(t, err, "startup activation is retired")
	require.Zero(t, index.calls, "deferred discovery must not read retained owner records")
}
