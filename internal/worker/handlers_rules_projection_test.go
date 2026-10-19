package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func TestRulesSelectionProjectionRegisteredRoutes(t *testing.T) {
	store := openRulesCollectionBridgeStore(t)
	rules := dbgorm.NewBehavioralRulesStore(store)
	selections := dbgorm.NewCollectionSelectionStore(store.DB)
	adapter, err := composeOperatorCollectionHTTPAdapter(store.DB, false)
	require.NoError(t, err)
	service := newOperatorCollectionRouteTestService(adapter)
	service.behavioralRulesStore = rules
	ctx := context.Background()
	projectA, projectB := uuid.NewString(), uuid.NewString()
	sessionID := "rules-projection-" + uuid.NewString()
	identity := auth.SessionForBrowserUser("operator", 41)
	var ownedIDs []int64
	t.Cleanup(func() {
		require.NoError(t, store.DB.Exec("DELETE FROM collection_selections WHERE session_id = ?", sessionID).Error)
		if len(ownedIDs) > 0 {
			require.NoError(t, store.DB.Exec("DELETE FROM behavioral_rules WHERE id IN ?", ownedIDs).Error)
		}
	})
	seed := func(project *string) *models.BehavioralRule {
		t.Helper()
		row, createErr := rules.Create(ctx, &models.BehavioralRule{Project: project, Content: "owned Rules projection fixture " + uuid.NewString(), Priority: 1 << 30})
		require.NoError(t, createErr)
		ownedIDs = append(ownedIDs, row.ID)
		return row
	}
	ruleA, ruleB, global := seed(&projectA), seed(&projectB), seed(nil)
	call := func(path string, body map[string]any, caller auth.Identity, browserSession string) *httptest.ResponseRecorder {
		t.Helper()
		payload, marshalErr := json.Marshal(body)
		require.NoError(t, marshalErr)
		request := operatorCollectionRouteTestRequest(t, path, string(payload), caller)
		request.Header.Set("Cookie", (&http.Cookie{Name: authSessionCookieName, Value: browserSession}).String())
		recorder := httptest.NewRecorder()
		service.router.ServeHTTP(recorder, request)
		return recorder
	}
	filter := func(scope string) map[string]string { return map[string]string{"scope": scope} }
	snapshot := func(projection string, intent map[string]any) operatorCollectionSnapshotDTO {
		t.Helper()
		response := call("/api/collections/selection", map[string]any{"domain": "rules", "filter": filter(projection), "selection": intent}, identity, sessionID)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var saved operatorCollectionSnapshotResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &saved))
		return saved.Selection
	}
	current := func(projection, domain string, caller auth.Identity, browserSession string) operatorCollectionSnapshotDTO {
		t.Helper()
		response := call("/api/collections/selection/current", map[string]any{"domain": domain, "filter": filter(projection)}, caller, browserSession)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		var saved operatorCollectionSnapshotResponse
		require.NoError(t, json.Unmarshal(response.Body.Bytes(), &saved))
		return saved.Selection
	}
	operation := func(projection string, saved operatorCollectionSnapshotDTO, caller auth.Identity, browserSession string) *httptest.ResponseRecorder {
		t.Helper()
		selection := map[string]any{"kind": saved.Kind, "selection_version": saved.SelectionVersion}
		if saved.SelectionToken != "" {
			selection["selection_token"] = saved.SelectionToken
		}
		return call("/api/rules", map[string]any{"request_id": "selection-route-request", "action": "disable", "filter": filter(projection), "selection": selection}, caller, browserSession)
	}
	explicit := func(ids ...int64) map[string]any {
		t.Helper()
		targets := make([]dbgorm.CollectionSelectionTarget, 0, len(ids))
		for _, id := range ids {
			row, getErr := rules.Get(ctx, id)
			require.NoError(t, getErr)
			targets = append(targets, dbgorm.CollectionSelectionTarget{ID: strconv.FormatInt(id, 10), ExpectedVersion: uint64(row.Version)})
		}
		return map[string]any{"kind": "explicit", "targets": targets}
	}

	for _, kind := range []string{"explicit", "page", "frozen_filter"} {
		t.Run(kind, func(t *testing.T) {
			intent := explicit(ruleA.ID)
			if kind == "page" {
				cursor := ""
				for {
					request := map[string]any{"domain": "rules", "filter": filter(projectA), "limit": 1}
					if cursor != "" {
						request["cursor"] = cursor
					}
					response := call("/api/collections/selection/page", request, identity, sessionID)
					require.Equal(t, http.StatusOK, response.Code, response.Body.String())
					var page operatorCollectionPageResponse
					require.NoError(t, json.Unmarshal(response.Body.Bytes(), &page))
					if len(page.Targets) == 1 && page.Targets[0].ID == strconv.FormatInt(ruleA.ID, 10) {
						intent = map[string]any{"kind": "page", "cursor": page.Cursor}
						break
					}
					require.NotEmpty(t, page.NextCursor, "the owned fixture row must be reachable without selecting foreign global rows")
					cursor = page.NextCursor
				}
				wrongPage := call("/api/collections/selection", map[string]any{"domain": "rules", "filter": filter(projectB), "selection": intent}, identity, sessionID)
				require.Equal(t, http.StatusPreconditionFailed, wrongPage.Code, wrongPage.Body.String())
			} else if kind == "frozen_filter" {
				// Exclude every global rule so this test never mutates another fixture's data.
				var globalIDs []int64
				require.NoError(t, store.DB.Model(&dbgorm.BehavioralRule{}).Where("project IS NULL AND deleted_at IS NULL").Pluck("id", &globalIDs).Error)
				excluded := make([]string, 0, len(globalIDs))
				for _, id := range globalIDs {
					excluded = append(excluded, strconv.FormatInt(id, 10))
				}
				intent = map[string]any{"kind": "frozen_filter", "filter": filter(projectA), "excluded_ids": excluded}
				wrongFilter := call("/api/collections/selection", map[string]any{"domain": "rules", "filter": filter(projectB), "selection": intent}, identity, sessionID)
				require.Equal(t, http.StatusBadRequest, wrongFilter.Code, wrongFilter.Body.String())
			}
			saved := snapshot(projectA, intent)
			same := current(projectA, "rules", identity, sessionID)
			require.Equal(t, saved.Kind, same.Kind)
			require.Equal(t, saved.SelectionVersion, same.SelectionVersion)
			require.False(t, same.ReconfirmationRequired)
			before, getErr := rules.Get(ctx, ruleA.ID)
			require.NoError(t, getErr)

			wrongAction := operation(projectB, saved, identity, sessionID)
			require.Equal(t, http.StatusPreconditionFailed, wrongAction.Code, wrongAction.Body.String())
			wrong := current(projectB, "rules", identity, sessionID)
			require.True(t, wrong.ReconfirmationRequired)
			require.Equal(t, dbgorm.CollectionSelectionReconfirmContextChanged, wrong.ReconfirmationReason)
			require.Greater(t, wrong.SelectionVersion, saved.SelectionVersion)
			replayed := operation(projectB, wrong, identity, sessionID)
			require.Equal(t, http.StatusPreconditionFailed, replayed.Code, replayed.Body.String())
			after, getErr := rules.Get(ctx, ruleA.ID)
			require.NoError(t, getErr)
			require.Equal(t, before, after, "wrong projection must not change any target bytes/version")
			require.NotContains(t, wrongAction.Body.String(), before.Content)

			saved = snapshot(projectA, intent)
			foreignOwner := current(projectA, "rules", auth.SessionForBrowserUser("operator", 42), sessionID)
			require.Equal(t, dbgorm.CollectionSelectionNone, foreignOwner.Kind)
			foreignSession := current(projectA, "rules", identity, sessionID+"-other")
			require.Equal(t, dbgorm.CollectionSelectionNone, foreignSession.Kind)
			forbidden := operation(projectA, saved, auth.SessionForBrowserUser("operator", 42), sessionID)
			require.Equal(t, http.StatusForbidden, forbidden.Code, forbidden.Body.String())
			staleVersion := saved
			staleVersion.SelectionVersion--
			conflict := operation(projectA, staleVersion, identity, sessionID)
			require.Equal(t, http.StatusConflict, conflict.Code, conflict.Body.String())
			completed := operation(projectA, saved, identity, sessionID)
			require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
			var completedBody behavioralRuleSelectionOperationResponse
			require.NoError(t, json.Unmarshal(completed.Body.Bytes(), &completedBody))
			require.Equal(t, "completed", completedBody.OperationState)
			require.NotNil(t, completedBody.Readback)
			require.True(t, completedBody.Readback.Authoritative)
		})
	}

	t.Run("intentional all and global projections", func(t *testing.T) {
		for _, projection := range []string{"all", "global"} {
			ids := []int64{ruleA.ID, ruleB.ID}
			if projection == "global" {
				ids = []int64{global.ID}
			}
			saved := snapshot(projection, explicit(ids...))
			restored := current(projection, "rules", identity, sessionID)
			require.False(t, restored.ReconfirmationRequired)
			require.Equal(t, saved.Targets, restored.Targets)
			completed := operation(projection, saved, identity, sessionID)
			require.Equal(t, http.StatusOK, completed.Code, completed.Body.String())
		}
	})

	t.Run("unbound snapshots and malformed projection fail closed without deleting intent", func(t *testing.T) {
		base, resolveErr := (operatorCollectionScopeAuthority{}).ResolveOperatorCollectionScope(ctx, identity, sessionID, "rules")
		require.NoError(t, resolveErr)
		row, getErr := rules.Get(ctx, ruleA.ID)
		require.NoError(t, getErr)
		old, saveErr := selections.Save(ctx, base, dbgorm.CollectionSelection{Kind: dbgorm.CollectionSelectionExplicit, Targets: []dbgorm.CollectionSelectionTarget{{ID: strconv.FormatInt(row.ID, 10), ExpectedVersion: uint64(row.Version)}}})
		require.NoError(t, saveErr)
		stale := current(projectA, "rules", identity, sessionID)
		require.True(t, stale.ReconfirmationRequired)
		require.Equal(t, old.Targets, stale.Targets, "old intent must be retained, not guessed or deleted")
		denied := operation(projectA, stale, identity, sessionID)
		require.Equal(t, http.StatusPreconditionFailed, denied.Code, denied.Body.String())
		missingOperationProjection := call("/api/rules", map[string]any{"request_id": "selection-route-request", "action": "delete", "selection": map[string]any{"kind": stale.Kind, "selection_version": stale.SelectionVersion}}, identity, sessionID)
		require.Equal(t, http.StatusBadRequest, missingOperationProjection.Code, missingOperationProjection.Body.String())
		for _, body := range []map[string]any{
			{"domain": "rules"},
			{"domain": "rules", "filter": filter("project-label-not-UUID")},
			{"domain": "rules", "filter": map[string]string{"scope": projectA, "filter_fingerprint": "sha256:" + strings.Repeat("a", 64)}},
		} {
			invalid := call("/api/collections/selection/current", body, identity, sessionID)
			require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())
		}
		foreignDomain := call("/api/collections/selection/current", map[string]any{"domain": "issues"}, identity, sessionID)
		require.Equal(t, http.StatusForbidden, foreignDomain.Code, foreignDomain.Body.String())
		missingBrowser := call("/api/collections/selection/current", map[string]any{"domain": "rules", "filter": filter(projectA)}, auth.Client("read-write", "not-a-browser"), sessionID)
		require.Equal(t, http.StatusForbidden, missingBrowser.Code, missingBrowser.Body.String())
		after, getErr := rules.Get(ctx, row.ID)
		require.NoError(t, getErr)
		require.Equal(t, row, after)
	})
}
