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
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
)

func TestB2LegacyPrincipalPrivacyHTTP(t *testing.T) {
	db := memoryMutationSQLiteDB(t)
	f := newMemoryMutationSecurityFixture(t, db)
	f.service.principalMemoryQueryService = principalmemory.NewPrincipalMemoryQueryService(f.service.memoryStore, f.service.auditStore)
	const marker = "syntheticuniquemarigold"
	const id = 912345
	require.NoError(t, db.Exec(`INSERT INTO memories (id, project, content, tags, status, context_kind, context_principal, context_principal_kind, privacy_scope, source_workstation_id, source_sessions, owner_principal, owner_principal_kind, agent_visibility, domain, created_at, updated_at, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?)`, id, f.project, marker, "[]", "active", "legacy", "", "", "project", "synthetic-workstation-a", "{}", "agent/synthetic-security-a", "agent", "private", "", 1).Error)
	var row gormdb.Memory
	require.NoError(t, db.First(&row, id).Error)
	require.Equal(t, "legacy", row.ContextKind)
	require.Equal(t, "project", row.PrivacyScope)
	require.Equal(t, "private", row.AgentVisibility)
	require.Equal(t, "agent/synthetic-security-a", row.OwnerPrincipal)
	require.Equal(t, "agent", row.OwnerPrincipalKind)

	query := func(token, term string) memoryMutationHTTPResult {
		t.Helper()
		values := url.Values{"principal": {"agent/synthetic-security-a"}, "principal_kind": {"agent"}, "q": {term}}
		return f.serve(f.request(t, context.Background(), http.MethodGet, "/api/memories/principal?"+values.Encode(), token, nil))
	}
	owner := query(f.tokenA, marker)
	require.Equal(t, http.StatusOK, owner.status, owner.body)
	var visible principalMemoryQueryResponse
	require.NoError(t, json.Unmarshal([]byte(owner.body), &visible))
	require.Len(t, visible.Items, 1)
	require.EqualValues(t, id, visible.Items[0].ID)
	unauthenticated := query("", marker)
	require.Contains(t, []int{http.StatusUnauthorized, http.StatusForbidden}, unauthenticated.status)
	matching := query(f.tokenB, marker)
	nonmatching := query(f.tokenB, "syntheticuniquenotpresent")
	require.Equal(t, http.StatusOK, matching.status, matching.body)
	require.Equal(t, http.StatusOK, nonmatching.status, nonmatching.body)
	var matched, missed principalMemoryQueryResponse
	require.NoError(t, json.Unmarshal([]byte(matching.body), &matched))
	require.NoError(t, json.Unmarshal([]byte(nonmatching.body), &missed))
	assert.Empty(t, matched.Items)
	assert.Empty(t, missed.Items)
	assert.NotContains(t, matching.body, marker)
	assert.NotContains(t, matching.body, "synthetic-workstation-a")
	assert.Equal(t, missed.HiddenCount, matched.HiddenCount, "hidden_count must not disclose inaccessible matches")
	assert.JSONEq(t, nonmatching.body, matching.body, "all response metadata must be indistinguishable")

	const sharedID = 912346
	for i := range 53 {
		visibility := "private"
		if i == 0 {
			visibility = "shared"
		}
		require.NoError(t, db.Exec(`INSERT INTO memories (id, project, content, tags, status, context_kind, context_principal, context_principal_kind, privacy_scope, source_workstation_id, source_sessions, owner_principal, owner_principal_kind, agent_visibility, domain, created_at, updated_at, version) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?)`, sharedID+i, f.project, marker, "[]", "active", "legacy", "", "", "project", "synthetic-workstation-a", "{}", "agent/synthetic-security-a", "agent", visibility, "", 1).Error)
	}
	values := url.Values{"principal": {"agent/synthetic-security-a"}, "principal_kind": {"agent"}, "q": {marker}, "limit": {"1"}}
	page := f.serve(f.request(t, context.Background(), http.MethodGet, "/api/memories/principal?"+values.Encode(), f.tokenB, nil))
	require.Equal(t, http.StatusOK, page.status, page.body)
	var shared principalMemoryQueryResponse
	require.NoError(t, json.Unmarshal([]byte(page.body), &shared))
	require.Len(t, shared.Items, 1, "newer private rows must not consume the retrieval budget")
	assert.EqualValues(t, sharedID, shared.Items[0].ID)
	assert.Nil(t, shared.HiddenCount)

	master := f.tokenAuth.Token()
	f.tokenAuth.SetValidator(auth.NewValidator(master, nil))
	values.Set("include_private", "true")
	admin := f.serve(f.request(t, context.Background(), http.MethodGet, "/api/memories/principal?"+values.Encode(), master, nil))
	require.Equal(t, http.StatusOK, admin.status, admin.body)
	var widened principalMemoryQueryResponse
	require.NoError(t, json.Unmarshal([]byte(admin.body), &widened))
	require.Len(t, widened.Items, 1)
	assert.Equal(t, "written", widened.AuditStatus)
	assert.True(t, widened.Audit.Durable)
	var audits int64
	require.NoError(t, db.Table("audit_log").Where("memory_id = ? AND action = ?", widened.Items[0].ID, "principal_memory_private_read").Count(&audits).Error)
	assert.EqualValues(t, 1, audits)
	f.service.principalMemoryQueryService = principalmemory.NewPrincipalMemoryQueryService(f.service.memoryStore, nil)
	failed := f.serve(f.request(t, context.Background(), http.MethodGet, "/api/memories/principal?"+values.Encode(), master, nil))
	assert.Equal(t, http.StatusInternalServerError, failed.status)
	assert.NotContains(t, failed.body, marker)
}
