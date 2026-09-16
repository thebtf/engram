package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

func TestHAPPrincipalClassesAreDisjoint(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	project := "canonical-project"
	cases := []struct {
		name                 string
		identity             auth.Identity
		class                auth.HAPPrincipalClass
		registration, daemon bool
		legacy               bool
	}{
		{
			name:         "registration service",
			identity:     auth.ClientWithPrincipal("read-write", "registration", auth.RegistrationServicePrincipal("workstation-a"), auth.PrincipalKindService),
			class:        auth.HAPPrincipalRegistration,
			registration: true,
		},
		{
			name:     "project service",
			identity: auth.ClientWithPrincipal("read-write", "project", auth.ProjectServicePrincipal(project), auth.PrincipalKindService),
			class:    auth.HAPPrincipalProject,
			daemon:   true,
		},
		{
			name:     "expiring legacy direct",
			identity: auth.ClientWithPrincipalExpiry("read-write", "legacy", auth.LegacyDirectPrincipal(project), auth.PrincipalKindAgent, &future),
			class:    auth.HAPPrincipalLegacyDirect,
			legacy:   true,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			principal, ok := test.identity.HAPPrincipal()
			require.True(t, ok)
			require.Equal(t, test.class, principal.Class)
			require.Equal(t, test.registration, test.identity.IsHAPRegistrationService())
			require.Equal(t, test.daemon, test.identity.IsHAPProjectServiceFor(project))
			require.Equal(t, test.legacy, test.identity.IsHAPLegacyDirectFor(project, now))
		})
	}
}

func TestHAPPrincipalIssuanceRequiresClassAndExpiry(t *testing.T) {
	now := time.Now().UTC()
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	project := "canonical-project"
	for _, test := range []struct {
		name      string
		principal string
		kind      auth.PrincipalKind
		expiresAt *time.Time
		wantError bool
	}{
		{name: "ordinary principal remains compatible", principal: "agent/codex", kind: auth.PrincipalKindAgent},
		{name: "registration service", principal: auth.RegistrationServicePrincipal("workstation-a"), kind: auth.PrincipalKindService},
		{name: "project service", principal: auth.ProjectServicePrincipal(project), kind: auth.PrincipalKindService},
		{name: "legacy direct future expiry", principal: auth.LegacyDirectPrincipal(project), kind: auth.PrincipalKindAgent, expiresAt: &future},
		{name: "legacy direct missing expiry", principal: auth.LegacyDirectPrincipal(project), kind: auth.PrincipalKindAgent, wantError: true},
		{name: "legacy direct expired", principal: auth.LegacyDirectPrincipal(project), kind: auth.PrincipalKindAgent, expiresAt: &past, wantError: true},
		{name: "legacy direct wrong kind", principal: auth.LegacyDirectPrincipal(project), kind: auth.PrincipalKindService, expiresAt: &future, wantError: true},
		{name: "malformed reserved principal", principal: "agent/omp-legacy/", kind: auth.PrincipalKindAgent, expiresAt: &future, wantError: true},
		{name: "nested reserved subject", principal: auth.ProjectServicePrincipal("project/nested"), kind: auth.PrincipalKindService, wantError: true},
		{name: "control character subject", principal: auth.RegistrationServicePrincipal("workstation\nother"), kind: auth.PrincipalKindService, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := auth.ValidateHAPPrincipalForIssuance(test.principal, test.kind, test.expiresAt, now)
			if test.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidatorRejectsExpiredKeycardBeforeReturningIdentity(t *testing.T) {
	raw := "engram_deafbeef000000000000000000000007"
	expiredAt := time.Now().UTC().Add(-time.Second)
	expired := makePrincipalKeycard(t, "expired-keycard", raw, "read-write", auth.LegacyDirectPrincipal("canonical-project"), auth.PrincipalKindAgent)
	expired.ExpiresAt = &expiredAt
	validator := auth.NewValidator("master-secret", &stubStore{byPrefix: map[string][]gormdb.APIToken{
		"deafbeef": {expired},
	}})

	identity, err := validator.Validate(context.Background(), raw)
	require.ErrorIs(t, err, auth.ErrInvalidCredentials)
	require.Equal(t, auth.Identity{}, identity)

	futureAt := time.Now().UTC().Add(time.Hour)
	future := expired
	future.ExpiresAt = &futureAt
	validator = auth.NewValidator("master-secret", &stubStore{byPrefix: map[string][]gormdb.APIToken{
		"deafbeef": {future},
	}})
	identity, err = validator.Validate(context.Background(), raw)
	require.NoError(t, err)
	require.NotNil(t, identity.ExpiresAt)
	require.True(t, identity.ExpiresAt.Equal(futureAt))
}
