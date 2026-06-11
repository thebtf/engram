package mcp

import (
	"testing"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestServerSetAuditStoreAssignsField(t *testing.T) {
	srv := NewServer(ServerOptions{Version: "wiring-audit-test"})

	as := &gormdb.AuditStore{}
	srv.SetAuditStore(as)

	require.Same(t, as, srv.auditStore)
}
