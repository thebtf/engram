package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/worker/sse"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

func TestComposeOperatorCollectionHTTPAdapterBuildsScopedDomainBridge(t *testing.T) {
	adapter, err := composeOperatorCollectionHTTPAdapter(&gormlib.DB{}, true)

	require.NoError(t, err)
	require.NotNil(t, adapter)
	require.IsType(t, &gormdb.CollectionSelectionStore{}, adapter.store)
	require.NotNil(t, adapter.resolver)
	providers, ok := adapter.normalizer.(operatorCollectionProviderRouter)
	require.True(t, ok)
	require.IsType(t, &gormdb.BehavioralRulesStore{}, providers[operatorCollectionSelectionDomain])
	require.IsType(t, &queueCandidateCollectionProvider{}, providers[queueCandidateSelectionDomain])
	_, freezerOK := adapter.freezer.(operatorCollectionProviderRouter)
	_, pagerOK := adapter.pager.(operatorCollectionProviderRouter)
	require.True(t, freezerOK)
	require.True(t, pagerOK)
}

func TestOperatorCollectionRoutesFreezeQueueDataWithoutRules(t *testing.T) {
	candidates := &fakeCandidateReviewStore{listRows: []*models.CrystallizationCandidate{
		{ID: 42, Status: models.CandidateStatusPending, UpdatedAt: time.Date(2026, time.September, 15, 10, 0, 0, 0, time.UTC)},
		{ID: 43, Status: models.CandidateStatusPending, UpdatedAt: time.Date(2026, time.September, 15, 9, 0, 0, 0, time.UTC)},
	}}
	store := &operatorCollectionRouteTestStore{}
	resolver := &operatorCollectionRouteTestResolver{scope: operatorCollectionRouteTestScope(queueCandidateSelectionDomain)}
	providers := operatorCollectionProviderRouter{
		queueCandidateSelectionDomain: newQueueCandidateCollectionProvider(candidates),
	}
	service := newOperatorCollectionRouteTestService(NewOperatorCollectionHTTPAdapter(store, resolver, providers, providers, providers))
	identity := auth.SessionForBrowserUser("operator", 41)
	call := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		service.router.ServeHTTP(recorder, operatorCollectionRouteTestRequest(t, path, body, identity))
		return recorder
	}

	page := call("/api/collections/selection/page", `{"domain":"queue","filter":{"scope":"all"},"limit":1}`)
	require.Equal(t, http.StatusOK, page.Code, page.Body.String())
	var pageBody operatorCollectionPageResponse
	require.NoError(t, json.Unmarshal(page.Body.Bytes(), &pageBody))
	require.Equal(t, []gormdb.CollectionSelectionTarget{{ID: "42", ExpectedVersion: uint64(candidates.listRows[0].UpdatedAt.UnixNano())}}, pageBody.Targets)
	require.Equal(t, "", candidates.listProject)
	require.Equal(t, models.CandidateStatusPending, candidates.listStatus)
	require.Equal(t, gormdb.CollectionSelectionMaxTargets+1, candidates.listLimit)

	pageSnapshot := call("/api/collections/selection", `{"domain":"queue","selection":{"kind":"page","cursor":"`+pageBody.Cursor+`","targets":[{"id":"rule-1","expected_version":99}]}}`)
	require.Equal(t, http.StatusOK, pageSnapshot.Code, pageSnapshot.Body.String())
	require.Len(t, store.saved, 1)
	require.Equal(t, gormdb.CollectionSelectionPage, store.saved[0].selection.Kind)
	require.Equal(t, pageBody.Targets, store.saved[0].selection.Targets)

	frozenSnapshot := call("/api/collections/selection", `{"domain":"queue","selection":{"kind":"frozen_filter","filter":{"scope":"all"},"excluded_ids":["43"]}}`)
	require.Equal(t, http.StatusOK, frozenSnapshot.Code, frozenSnapshot.Body.String())
	require.Len(t, store.saved, 2)
	require.Equal(t, gormdb.CollectionSelectionFrozenFilter, store.saved[1].selection.Kind)
	require.Equal(t, []gormdb.CollectionSelectionTarget{
		{ID: "42", ExpectedVersion: uint64(candidates.listRows[0].UpdatedAt.UnixNano())},
		{ID: "43", ExpectedVersion: uint64(candidates.listRows[1].UpdatedAt.UnixNano())},
	}, store.saved[1].selection.Targets)
	require.Equal(t, []string{"43"}, store.saved[1].selection.ExcludedIDs)

	invalid := call("/api/collections/selection/page", `{"domain":"queue","filter":{"scope":"all"},"cursor":"not-a-queue-cursor","limit":1}`)
	require.Equal(t, http.StatusBadRequest, invalid.Code, invalid.Body.String())

	candidates.listRows = append([]*models.CrystallizationCandidate{{ID: 44, Status: models.CandidateStatusPending, UpdatedAt: time.Date(2026, time.September, 15, 11, 0, 0, 0, time.UTC)}}, candidates.listRows...)
	stale := call("/api/collections/selection/page", `{"domain":"queue","filter":{"scope":"all"},"cursor":"`+pageBody.Cursor+`","limit":1}`)
	require.Equal(t, http.StatusPreconditionFailed, stale.Code, stale.Body.String())
}

func TestOperatorCollectionRoutesDelegateScopedSelectionWithoutAuthority(t *testing.T) {
	store := &operatorCollectionRouteTestStore{current: gormdb.CollectionSelection{Kind: gormdb.CollectionSelectionNone}}
	resolver := &operatorCollectionRouteTestResolver{scope: operatorCollectionRouteTestScope("rules")}
	normalizer := &operatorCollectionRouteTestNormalizer{}
	pager := &operatorCollectionRouteTestPager{page: gormdb.CollectionPage{Cursor: "route-page-cursor", Targets: []gormdb.CollectionSelectionTarget{{ID: "rule-3", ExpectedVersion: 6}}}}
	service := newOperatorCollectionRouteTestService(NewOperatorCollectionHTTPAdapter(store, resolver, normalizer, nil, pager))
	identity := auth.SessionForBrowserUser("operator", 41)

	call := func(path, body string, caller auth.Identity) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		service.router.ServeHTTP(recorder, operatorCollectionRouteTestRequest(t, path, body, caller))
		return recorder
	}

	snapshot := call("/api/collections/selection", `{"domain":"rules","selection":{"kind":"explicit","targets":[{"id":"rule-1","expected_version":4}]}}`, identity)
	require.Equal(t, http.StatusOK, snapshot.Code, snapshot.Body.String())
	require.Equal(t, []operatorCollectionRouteTestSaved{{
		scope:     operatorCollectionRouteTestScope("rules"),
		selection: gormdb.CollectionSelection{Kind: gormdb.CollectionSelectionExplicit, Targets: []gormdb.CollectionSelectionTarget{{ID: "rule-1", ExpectedVersion: 4}}},
	}}, store.saved)

	current := call("/api/collections/selection/current", `{"domain":"rules"}`, identity)
	require.Equal(t, http.StatusOK, current.Code, current.Body.String())
	require.Equal(t, 1, store.currentCalls)

	page := call("/api/collections/selection/page", `{"domain":"rules","filter":{"scope":"project"},"limit":2}`, identity)
	require.Equal(t, http.StatusOK, page.Code, page.Body.String())
	require.Equal(t, []gormdb.CollectionPageRequest{{
		Domain: "rules",
		Filter: normalizer.filter,
		Limit:  2,
	}}, pager.calls)

	forgedToken := call("/api/collections/selection", `{"domain":"rules","selection":{"kind":"explicit","selection_token":"4e0add48-42c9-4f4f-b5c7-f89ebf2a6ee6","targets":[{"id":"rule-2"}]}}`, identity)
	require.Equal(t, http.StatusBadRequest, forgedToken.Code)
	require.Len(t, store.saved, 1, "a selection token cannot select an action or alter the stored snapshot")

	forgedIdentity := call("/api/collections/selection", `{"domain":"rules","selection":{"kind":"none"}}`, auth.Client("read-write", "keycard-forged"))
	require.Equal(t, http.StatusForbidden, forgedIdentity.Code)
	require.Len(t, store.saved, 1)

	require.Len(t, resolver.calls, 3)
	for _, resolved := range resolver.calls {
		require.Equal(t, int64(41), resolved.subjectUserID)
		require.Equal(t, "browser-selection-session", resolved.sessionID)
		require.Equal(t, "rules", resolved.domain)
	}
}

type operatorCollectionRouteTestStore struct {
	saved        []operatorCollectionRouteTestSaved
	current      gormdb.CollectionSelection
	currentCalls int
}

type operatorCollectionRouteTestSaved struct {
	scope     gormdb.CollectionSelectionScope
	selection gormdb.CollectionSelection
}

func (store *operatorCollectionRouteTestStore) Save(_ context.Context, scope gormdb.CollectionSelectionScope, selection gormdb.CollectionSelection) (gormdb.CollectionSelection, error) {
	store.saved = append(store.saved, operatorCollectionRouteTestSaved{scope: scope, selection: selection})
	return selection, nil
}

func (store *operatorCollectionRouteTestStore) Current(_ context.Context, _ gormdb.CollectionSelectionScope) (gormdb.CollectionSelection, error) {
	store.currentCalls++
	return store.current, nil
}

type operatorCollectionRouteTestResolved struct {
	subjectUserID int64
	sessionID     string
	domain        string
}

type operatorCollectionRouteTestResolver struct {
	scope gormdb.CollectionSelectionScope
	calls []operatorCollectionRouteTestResolved
}

func (resolver *operatorCollectionRouteTestResolver) ResolveOperatorCollectionScope(_ context.Context, identity auth.Identity, sessionID, domain string) (gormdb.CollectionSelectionScope, error) {
	subject, ok := identity.SessionBrowserSubject()
	if !ok || subject.UserID != resolver.scope.SubjectUserID || sessionID != resolver.scope.SessionID || domain != resolver.scope.Domain {
		return gormdb.CollectionSelectionScope{}, errors.New("collection selection scope denied")
	}
	resolver.calls = append(resolver.calls, operatorCollectionRouteTestResolved{subjectUserID: subject.UserID, sessionID: sessionID, domain: domain})
	return resolver.scope, nil
}

type operatorCollectionRouteTestPager struct {
	page  gormdb.CollectionPage
	calls []gormdb.CollectionPageRequest
}

func (pager *operatorCollectionRouteTestPager) PageCollection(_ context.Context, _ gormdb.CollectionSelectionScope, request gormdb.CollectionPageRequest) (gormdb.CollectionPage, error) {
	pager.calls = append(pager.calls, request)
	return pager.page, nil
}

type operatorCollectionRouteTestNormalizer struct {
	filter gormdb.CollectionFilter
}

func (normalizer *operatorCollectionRouteTestNormalizer) NormalizeCollectionFilter(_ context.Context, _ gormdb.CollectionSelectionScope, scope string) (gormdb.CollectionFilter, error) {
	if normalizer.filter.Fingerprint == "" {
		normalizer.filter = gormdb.CollectionFilter{Fingerprint: operatorCollectionRouteTestDigest("f"), Value: scope}
	}
	return normalizer.filter, nil
}

func newOperatorCollectionRouteTestService(adapter *OperatorCollectionHTTPAdapter) *Service {
	service := &Service{
		router:                    chi.NewRouter(),
		mcpHealth:                 mcp.NewMCPHealth(),
		sseBroadcaster:            sse.NewBroadcaster(),
		operatorCollectionAdapter: adapter,
	}
	service.ready.Store(true)
	service.setupRoutes()
	return service
}

func operatorCollectionRouteTestRequest(t *testing.T, path, body string, identity auth.Identity) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Engram-Request-ID", "selection-route-request")
	request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "browser-selection-session"})
	return request.WithContext(auth.WithIdentity(request.Context(), identity))
}

func operatorCollectionRouteTestScope(domain string) gormdb.CollectionSelectionScope {
	return gormdb.CollectionSelectionScope{
		SubjectUserID:      41,
		SessionID:          "browser-selection-session",
		Domain:             domain,
		ContextFingerprint: operatorCollectionRouteTestDigest("c"),
		AuthorizationEpoch: 7,
		CollectionVersion:  11,
	}
}

func operatorCollectionRouteTestDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
