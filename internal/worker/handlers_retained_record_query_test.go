package worker

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestRetainedRecordQueryEmptyMemoriesPreservesCatalogHTTP(t *testing.T) {
	service, db, refs := retainedDiscoveryFixture(t)
	require.NoError(t, db.Exec("DELETE FROM project_consolidation_memberships WHERE table_name = ?", "memories").Error)
	require.NoError(t, db.Exec("DELETE FROM memories").Error)
	var memories, memberships int64
	require.NoError(t, db.Model(&gormdb.Memory{}).Count(&memories).Error)
	require.Zero(t, memories, "the native memory table must be empty, not just its historical memberships")
	require.NoError(t, db.Model(&gormdb.ConsolidationMembership{}).Where("table_name = ?", "memories").Count(&memberships).Error)
	require.Zero(t, memberships)

	router := retainedDiscoveryRouter(service)
	response := retainedDiscoveryGET(t, router, "/api/projects", true, http.StatusOK)
	var catalog struct {
		Items []struct {
			ProjectID string `json:"project_id"`
			Name      string `json:"name"`
		} `json:"items"`
		Unresolved []struct {
			Reference string          `json:"reference"`
			Families  json.RawMessage `json:"families"`
		} `json:"unresolved"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &catalog))
	require.Len(t, catalog.Items, 1, "an independently existing canonical project must remain discoverable")
	require.NotEmpty(t, catalog.Items[0].ProjectID)
	require.Equal(t, "Historical notebook", catalog.Items[0].Name)
	groups := map[string]json.RawMessage{}
	for _, group := range catalog.Unresolved {
		groups[group.Reference] = group.Families
	}
	require.Len(t, catalog.Unresolved, len(refs))
	for _, reference := range refs {
		require.Contains(t, groups, reference, "removing memories must preserve the other catalog metadata groups")
	}
	require.JSONEq(t, `[{"kind":"native","table_name":"sdk_sessions","membership_count":1,"reader_support":"metadata_only"}]`, string(groups[refs["native"]]))
	require.JSONEq(t, `[{"kind":"native","table_name":"projects","membership_count":1,"reader_support":"metadata_only"}]`, string(groups[refs["registry"]]))

	// The adopted explicit mode preserves the independent full catalog and other families.
	response = retainedDiscoveryGET(t, router, "/api/projects?view=retained_records&attribution_scope=unresolved_history&kind=memory&q=retained&limit=1", true, http.StatusOK)
	var page struct {
		View             string          `json:"view"`
		AttributionScope string          `json:"attribution_scope"`
		Kind             string          `json:"kind"`
		Items            json.RawMessage `json:"items"`
		NextCursor       json.RawMessage `json:"next_cursor"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
	require.JSONEq(t, `[]`, string(page.Items), "an empty native-memory query must return no records, not canonical catalog projects")
	require.Equal(t, "retained_records", page.View)
	require.Equal(t, "unresolved_history", page.AttributionScope)
	require.Equal(t, "memory", page.Kind)
	require.JSONEq(t, `null`, string(page.NextCursor), "an exhausted empty traversal must explicitly return a null cursor")
	require.Equal(t, "private, no-store", response.Header().Get("Cache-Control"))
}
