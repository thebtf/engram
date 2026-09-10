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

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestOperatorCollectionHTTPAdapter_FrozenSelectionUsesServerSnapshot(t *testing.T) {
	store := &operatorCollectionHTTPTestStore{}
	resolver := &operatorCollectionHTTPTestResolver{scope: operatorCollectionTestScope("rules")}
	freezer := &operatorCollectionHTTPTestFreezer{frozen: gormdb.CollectionFrozenSelection{
		Targets: []gormdb.CollectionSelectionTarget{
			{ID: "rule-1", ExpectedVersion: 4},
			{ID: "rule-2", ExpectedVersion: 5},
		},
		ExpiresAt: time.Now().UTC().Add(time.Minute),
	}}
	adapter := NewOperatorCollectionHTTPAdapter(store, resolver, freezer, nil)

	request := operatorCollectionHTTPTestRequest(t, http.MethodPost, `{
		"domain":"rules",
		"selection":{"kind":"frozen_filter","filter_fingerprint":"`+operatorCollectionTestDigest("f")+`","excluded_ids":["rule-2"]}
	}`)
	recorder := httptest.NewRecorder()
	adapter.HandleSnapshot(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	require.Len(t, store.saved, 1)
	require.Equal(t, gormdb.CollectionSelectionFrozenFilter, store.saved[0].selection.Kind)
	require.Equal(t, []string{"rule-2"}, store.saved[0].selection.ExcludedIDs)
	require.Equal(t, []gormdb.CollectionSelectionTarget{{ID: "rule-1", ExpectedVersion: 4}, {ID: "rule-2", ExpectedVersion: 5}}, store.saved[0].selection.Targets)
	require.Len(t, freezer.calls, 1)
	require.Equal(t, operatorCollectionTestScope("rules"), freezer.calls[0].scope)

	var response struct {
		Selection struct {
			Kind  string `json:"kind"`
			Token string `json:"selection_token"`
		} `json:"selection"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Equal(t, string(gormdb.CollectionSelectionFrozenFilter), response.Selection.Kind)
	require.Empty(t, response.Selection.Token, "the adapter never lets a client provide or use a token as action authority")
}

func TestOperatorCollectionHTTPAdapter_DeniesStaleCursorAndRejectsTokenReplay(t *testing.T) {
	store := &operatorCollectionHTTPTestStore{}
	pager := &operatorCollectionHTTPTestPager{err: gormdb.ErrCollectionSelectionDenied}
	adapter := NewOperatorCollectionHTTPAdapter(store, &operatorCollectionHTTPTestResolver{scope: operatorCollectionTestScope("rules")}, nil, pager)

	staleCursor := operatorCollectionHTTPTestRequest(t, http.MethodPost, `{"domain":"rules","filter_fingerprint":"`+operatorCollectionTestDigest("f")+`","cursor":"opaque-stale-cursor","limit":50}`)
	staleCursorRecorder := httptest.NewRecorder()
	adapter.HandlePage(staleCursorRecorder, staleCursor)
	require.Equal(t, http.StatusForbidden, staleCursorRecorder.Code)
	require.Len(t, pager.calls, 1, "a domain pager must reject stale cursors within the current browser scope")

	tokenReplay := operatorCollectionHTTPTestRequest(t, http.MethodPost, `{"domain":"rules","selection":{"kind":"frozen_filter","selection_token":"4e0add48-42c9-4f4f-b5c7-f89ebf2a6ee6","filter_fingerprint":"`+operatorCollectionTestDigest("f")+`"}}`)
	tokenReplayRecorder := httptest.NewRecorder()
	adapter.HandleSnapshot(tokenReplayRecorder, tokenReplay)
	require.Equal(t, http.StatusBadRequest, tokenReplayRecorder.Code)
	require.Empty(t, store.saved, "no selection token can select a later action or alter server-owned frozen intent")
}

func TestOperatorCollectionHTTPAdapter_RejectsInvalidFrozenIntentBeforeFreezing(t *testing.T) {
	store := &operatorCollectionHTTPTestStore{}
	freezer := &operatorCollectionHTTPTestFreezer{}
	adapter := NewOperatorCollectionHTTPAdapter(store, &operatorCollectionHTTPTestResolver{scope: operatorCollectionTestScope("rules")}, freezer, nil)

	request := operatorCollectionHTTPTestRequest(t, http.MethodPost, `{"domain":"rules","selection":{"kind":"frozen_filter","filter_fingerprint":"not-a-fingerprint","excluded_ids":["rule-1"]}}`)
	recorder := httptest.NewRecorder()
	adapter.HandleSnapshot(recorder, request)

	require.Equal(t, http.StatusBadRequest, recorder.Code)
	require.Empty(t, freezer.calls)
	require.Empty(t, store.saved)
}

func TestOperatorCollectionHTTPAdapter_RejectsBoundsAndForeignSession(t *testing.T) {
	store := &operatorCollectionHTTPTestStore{}
	pager := &operatorCollectionHTTPTestPager{}
	adapter := NewOperatorCollectionHTTPAdapter(store, &operatorCollectionHTTPTestResolver{scope: operatorCollectionTestScope("rules")}, nil, pager)

	overBound := operatorCollectionHTTPTestRequest(t, http.MethodPost, `{"domain":"rules","filter_fingerprint":"`+operatorCollectionTestDigest("f")+`","limit":201}`)
	overBoundRecorder := httptest.NewRecorder()
	adapter.HandlePage(overBoundRecorder, overBound)
	require.Equal(t, http.StatusBadRequest, overBoundRecorder.Code)
	require.Empty(t, pager.calls, "the handler rejects bounds before a domain pager can observe them")
	longCursor := operatorCollectionHTTPTestRequest(t, http.MethodPost, `{"domain":"rules","filter_fingerprint":"`+operatorCollectionTestDigest("f")+`","cursor":"`+strings.Repeat("c", 513)+`"}`)
	longCursorRecorder := httptest.NewRecorder()
	adapter.HandlePage(longCursorRecorder, longCursor)
	require.Equal(t, http.StatusBadRequest, longCursorRecorder.Code)
	require.Empty(t, pager.calls)

	foreign := httptest.NewRequest(http.MethodPost, "/api/collections/selection", strings.NewReader(`{"domain":"rules","selection":{"kind":"none"}}`))
	foreign.Header.Set("Content-Type", "application/json")
	foreign.Header.Set("X-Engram-Request-ID", "selection-request-foreign")
	foreign.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "browser-selection-session"})
	foreign = foreign.WithContext(auth.WithIdentity(foreign.Context(), auth.Client("read-write", "keycard-foreign")))
	foreignRecorder := httptest.NewRecorder()
	adapter.HandleSnapshot(foreignRecorder, foreign)
	require.Equal(t, http.StatusForbidden, foreignRecorder.Code)
	require.Empty(t, store.saved, "a non-browser token cannot create a browser collection selection")
	foreignBrowser := operatorCollectionHTTPTestRequestForUser(t, http.MethodPost, `{"domain":"rules","selection":{"kind":"none"}}`, 42)
	foreignBrowserRecorder := httptest.NewRecorder()
	adapter.HandleSnapshot(foreignBrowserRecorder, foreignBrowser)
	require.Equal(t, http.StatusForbidden, foreignBrowserRecorder.Code)
	require.Empty(t, store.saved, "a browser selection is bound to its exact authenticated owner as well as its session and domain")
}

type operatorCollectionHTTPTestStore struct {
	saved []operatorCollectionSaved
}

type operatorCollectionSaved struct {
	scope     gormdb.CollectionSelectionScope
	selection gormdb.CollectionSelection
}

func (store *operatorCollectionHTTPTestStore) Save(_ context.Context, scope gormdb.CollectionSelectionScope, selection gormdb.CollectionSelection) (gormdb.CollectionSelection, error) {
	store.saved = append(store.saved, operatorCollectionSaved{scope: scope, selection: selection})
	return selection, nil
}

func (store *operatorCollectionHTTPTestStore) Current(_ context.Context, _ gormdb.CollectionSelectionScope) (gormdb.CollectionSelection, error) {
	return gormdb.CollectionSelection{Kind: gormdb.CollectionSelectionNone}, nil
}

type operatorCollectionHTTPTestResolver struct {
	scope gormdb.CollectionSelectionScope
	err   error
}

func (resolver *operatorCollectionHTTPTestResolver) ResolveOperatorCollectionScope(_ context.Context, identity auth.Identity, sessionID, domain string) (gormdb.CollectionSelectionScope, error) {
	if _, ok := identity.SessionBrowserSubject(); !ok || sessionID != resolver.scope.SessionID || domain != resolver.scope.Domain {
		return gormdb.CollectionSelectionScope{}, errors.New("browser collection scope denied")
	}
	return resolver.scope, resolver.err
}

type operatorCollectionFreezeCall struct {
	scope             gormdb.CollectionSelectionScope
	filterFingerprint string
}

type operatorCollectionHTTPTestFreezer struct {
	frozen gormdb.CollectionFrozenSelection
	calls  []operatorCollectionFreezeCall
}

func (freezer *operatorCollectionHTTPTestFreezer) FreezeCollectionSelection(_ context.Context, scope gormdb.CollectionSelectionScope, filterFingerprint string) (gormdb.CollectionFrozenSelection, error) {
	freezer.calls = append(freezer.calls, operatorCollectionFreezeCall{scope: scope, filterFingerprint: filterFingerprint})
	return freezer.frozen, nil
}

type operatorCollectionHTTPTestPager struct {
	calls []gormdb.CollectionPageRequest
	err   error
}

func (pager *operatorCollectionHTTPTestPager) PageCollection(_ context.Context, _ gormdb.CollectionSelectionScope, request gormdb.CollectionPageRequest) (gormdb.CollectionPage, error) {
	pager.calls = append(pager.calls, request)
	return gormdb.CollectionPage{}, pager.err
}

func operatorCollectionHTTPTestRequest(t *testing.T, method, body string) *http.Request {
	return operatorCollectionHTTPTestRequestForUser(t, method, body, 41)
}

func operatorCollectionHTTPTestRequestForUser(t *testing.T, method, body string, userID int64) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, "/api/collections/selection", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Engram-Request-ID", "selection-request-41")
	request.AddCookie(&http.Cookie{Name: authSessionCookieName, Value: "browser-selection-session"})
	return request.WithContext(auth.WithIdentity(request.Context(), auth.SessionForBrowserUser("operator", userID)))
}

func operatorCollectionTestScope(domain string) gormdb.CollectionSelectionScope {
	return gormdb.CollectionSelectionScope{
		SubjectUserID:      41,
		SessionID:          "browser-selection-session",
		Domain:             domain,
		ContextFingerprint: operatorCollectionTestDigest("c"),
		AuthorizationEpoch: 7,
		CollectionVersion:  11,
	}
}

func operatorCollectionTestDigest(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
