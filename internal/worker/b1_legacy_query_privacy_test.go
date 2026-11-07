package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/principalmemory"
)

func TestB1LegacyPrincipalQueryPrivacyHTTP(t *testing.T) {
	db := memoryMutationSQLiteDB(t)
	f := newMemoryMutationSecurityFixture(t, db)
	f.service.principalMemoryQueryService = principalmemory.NewPrincipalMemoryQueryService(f.service.memoryStore, f.service.auditStore)
	const marker = "syntheticuniquemarigold"
	const privateID = 912345
	insert := `INSERT INTO memories (id, project, content, tags, status, context_kind, context_principal, context_principal_kind, privacy_scope, source_workstation_id, source_sessions, owner_principal, owner_principal_kind, agent_visibility, domain, created_at, updated_at, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?)`
	add := func(id int, visibility string) {
		t.Helper()
		require.NoError(t, db.Exec(insert, id, f.project, marker, "[]", "active", "legacy", "", "", "project", "synthetic-workstation-a", "{}", "agent/synthetic-security-a", "agent", visibility, "", 1).Error)
	}
	add(privateID, "private")
	query := func(token, term string, widen bool) memoryMutationHTTPResult {
		t.Helper()
		values := url.Values{"principal": {"agent/synthetic-security-a"}, "principal_kind": {"agent"}, "q": {term}, "limit": {"1"}}
		if widen {
			values.Set("include_private", "true")
		}
		return f.serve(f.request(t, context.Background(), http.MethodGet, "/api/memories/principal?"+values.Encode(), token, nil))
	}
	decode := func(response memoryMutationHTTPResult) principalMemoryQueryResponse {
		t.Helper()
		require.Equal(t, http.StatusOK, response.status, response.body)
		var result principalMemoryQueryResponse
		require.NoError(t, json.Unmarshal([]byte(response.body), &result))
		var wire map[string]any
		require.NoError(t, json.Unmarshal([]byte(response.body), &wire))
		require.Contains(t, wire, "hidden_count")
		require.Nil(t, wire["hidden_count"])
		require.Nil(t, result.HiddenCount)
		return result
	}
	owner := decode(query(f.tokenA, marker, false))
	require.Len(t, owner.Items, 1)
	assert.EqualValues(t, privateID, owner.Items[0].ID)
	assert.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, query("", marker, false).status)
	match := query(f.tokenB, marker, false)
	miss := query(f.tokenB, "syntheticuniquenotpresent", false)
	require.Empty(t, decode(match).Items)
	require.Empty(t, decode(miss).Items)
	assert.NotContains(t, match.body, marker)
	assert.NotContains(t, match.body, "synthetic-workstation-a")
	assert.JSONEq(t, miss.body, match.body, "inaccessible matches must not affect response metadata")
	assert.Equal(t, http.StatusForbidden, query(f.tokenB, marker, true).status)
	const sharedID = 912346
	add(sharedID, "shared")
	for i := range 52 {
		add(sharedID+1+i, "private")
	}
	page := decode(query(f.tokenB, marker, false))
	require.Len(t, page.Items, 1)
	assert.EqualValues(t, sharedID, page.Items[0].ID, "private rows must be filtered before LIMIT")
	master := f.tokenAuth.Token()
	f.tokenAuth.SetValidator(auth.NewValidator(master, nil))
	admin := decode(query(master, marker, true))
	require.Len(t, admin.Items, 1)
	assert.Equal(t, "written", admin.AuditStatus)
	assert.True(t, admin.Audit.Durable)
	var audits int64
	require.NoError(t, db.Table("audit_log").Where("memory_id = ? AND action = ?", admin.Items[0].ID, "principal_memory_private_read").Count(&audits).Error)
	assert.EqualValues(t, 1, audits)
	f.service.principalMemoryQueryService = principalmemory.NewPrincipalMemoryQueryService(f.service.memoryStore, nil)
	failed := query(master, marker, true)
	assert.Equal(t, http.StatusInternalServerError, failed.status)
	assert.NotContains(t, failed.body, marker)
}
