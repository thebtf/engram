package worker

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

func TestLegacyDirectEnforcement_ContextInjectRoute(t *testing.T) {
	t.Setenv("ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT", "true")
	resolverCalled := false
	service := &Service{legacyDirectProjectResolver: func(context.Context, string) (string, error) {
		resolverCalled = true
		return "11111111-1111-4111-8111-111111111111", nil
	}}
	project := "legacy-direct-context"
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/context/inject", bytes.NewBufferString(`{"project":"`+project+`","identity_only":true}`))
	service.handleContextInject(recorder, request)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.True(t, resolverCalled, "legacy direct authorization must run before store readiness or identity mutation")
}

func TestLegacyDirectEnforcement_SessionStartRoute(t *testing.T) {
	t.Setenv("ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT", "true")
	backend := &stubSessionStartContextServer{resp: &pb.GetSessionStartContextResponse{}}
	service := &Service{grpcInternalServer: backend}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/context/session-start?project=legacy-direct-session", nil)
	service.handleSessionStartContextStatic(recorder, request)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Nil(t, backend.req, "guard must run before the compatibility backend")
}

func TestLegacyDirectEnforcement_AmbientRoute(t *testing.T) {
	t.Setenv("ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT", "true")
	t.Setenv("ENGRAM_V7_PLUG_ENABLED", "true")
	t.Setenv("ENGRAM_V7_S3_AMBIENT", "true")
	service := newAmbientHandlerService(t)
	recorder := postAmbientCandidates(t, service, ambientCandidatesRequest{
		SessionID:  "legacy-direct-ambient",
		Project:    "legacy-direct-ambient-project",
		PromptText: "must be denied before ambient proposal",
	})
	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func TestLegacyDirectEnforcementAllowsOnlyMatchingUnexpiredIdentity(t *testing.T) {
	project := "legacy-direct-project"
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	matching := auth.ClientWithPrincipalExpiry("read-write", "legacy-keycard", auth.LegacyDirectPrincipal(project), auth.PrincipalKindAgent, &future)

	t.Setenv("ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT", "")
	defaultRecorder := httptest.NewRecorder()
	require.False(t, (&Service{}).rejectLegacyDirectDelivery(defaultRecorder, context.Background(), project))

	matchingContext := auth.WithIdentity(context.Background(), matching)
	require.True(t, legacyDirectIdentityMatches(matchingContext, project, now))

	wrongProject := auth.ClientWithPrincipalExpiry("read-write", "other-keycard", auth.LegacyDirectPrincipal("other-project"), auth.PrincipalKindAgent, &future)
	require.False(t, legacyDirectIdentityMatches(auth.WithIdentity(context.Background(), wrongProject), project, now))

	past := now.Add(-time.Second)
	expired := auth.ClientWithPrincipalExpiry("read-write", "expired-keycard", auth.LegacyDirectPrincipal(project), auth.PrincipalKindAgent, &past)
	require.False(t, legacyDirectIdentityMatches(auth.WithIdentity(context.Background(), expired), project, now))
}

func TestHAPHTTPRouteAllowlistIsCredentialClassSpecific(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour)
	legacy := auth.ClientWithPrincipalExpiry("read-write", "legacy", auth.LegacyDirectPrincipal("project"), auth.PrincipalKindAgent, &future)
	project := auth.ClientWithPrincipal("read-write", "project", auth.ProjectServicePrincipal("project"), auth.PrincipalKindService)
	registration := auth.ClientWithPrincipal("read-write", "registration", auth.RegistrationServicePrincipal("workstation"), auth.PrincipalKindService)
	ordinary := auth.ClientWithPrincipal("read-write", "ordinary", "agent/codex", auth.PrincipalKindAgent)

	require.True(t, hapHTTPRouteAllowed(legacy, http.MethodPost, "/api/context/inject"))
	require.True(t, hapHTTPRouteAllowed(legacy, http.MethodGet, "/api/context/session-start"))
	require.True(t, hapHTTPRouteAllowed(legacy, http.MethodPost, "/api/hooks/ambient-candidates"))
	require.False(t, hapHTTPRouteAllowed(legacy, http.MethodGet, "/api/memories"))
	require.False(t, hapHTTPRouteAllowed(project, http.MethodPost, "/api/context/session-start"))
	require.False(t, hapHTTPRouteAllowed(registration, http.MethodPost, "/api/context/inject"))
	require.True(t, hapHTTPRouteAllowed(ordinary, http.MethodGet, "/api/memories"))
}

func TestLegacyDirectEnforcementResolvesLegacyAliasBeforePrincipalMatch(t *testing.T) {
	t.Setenv("ENGRAM_HAP_01B_LEGACY_DIRECT_ENFORCEMENT", "true")
	db, cleanup := setupProjectTestDB(t)
	defer cleanup()
	canonicalProject := "9bc5b121-5be5-486f-a4ea-cdb6a503b655"
	legacyAlias := "legacy-direct-alias"
	require.NoError(t, gormdb.UpsertProject(context.Background(), db, canonicalProject, legacyAlias, "", "", "legacy direct alias"))
	defer db.Exec("DELETE FROM projects WHERE id = ?", canonicalProject)

	future := time.Now().UTC().Add(time.Hour)
	identity := auth.ClientWithPrincipalExpiry("read-write", "legacy-keycard", auth.LegacyDirectPrincipal(canonicalProject), auth.PrincipalKindAgent, &future)
	service := &Service{store: &gormdb.Store{DB: db}}
	recorder := httptest.NewRecorder()
	require.False(t, service.rejectLegacyDirectDelivery(recorder, auth.WithIdentity(context.Background(), identity), legacyAlias))
}
