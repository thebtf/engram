package worker

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

type recordingCodeGrantStore struct {
	issues  []gormdb.BrowserReadGrantIssue
	revokes []struct {
		issuerUserID    int64
		issuerPrincipal string
		grantRef        string
	}
	canReadCalls []struct {
		subjectUserID int64
		sourceID      string
		checkoutID    string
	}
	canRead bool
}

func (store *recordingCodeGrantStore) Issue(_ context.Context, in gormdb.BrowserReadGrantIssue) (gormdb.BrowserReadGrant, error) {
	store.issues = append(store.issues, in)
	return gormdb.BrowserReadGrant{GrantRef: uuid.NewString()}, nil
}

func (store *recordingCodeGrantStore) Revoke(_ context.Context, issuerUserID int64, issuerPrincipal, grantRef string) (gormdb.BrowserReadGrant, error) {
	store.revokes = append(store.revokes, struct {
		issuerUserID    int64
		issuerPrincipal string
		grantRef        string
	}{issuerUserID: issuerUserID, issuerPrincipal: issuerPrincipal, grantRef: grantRef})
	return gormdb.BrowserReadGrant{GrantRef: grantRef, State: gormdb.BrowserReadGrantRevoked}, nil
}

func (store *recordingCodeGrantStore) CanRead(_ context.Context, subjectUserID int64, sourceID, checkoutID string) (bool, error) {
	store.canReadCalls = append(store.canReadCalls, struct {
		subjectUserID int64
		sourceID      string
		checkoutID    string
	}{subjectUserID: subjectUserID, sourceID: sourceID, checkoutID: checkoutID})
	return store.canRead, nil
}

func TestCodeGrantApplication_CarriesOnlyCanonicalBrowserSubject(t *testing.T) {
	store := &recordingCodeGrantStore{canRead: true}
	app := &CodeGrantApplication{grants: store}
	issuer := auth.SessionForBrowserUser("operator", 41)
	target := auth.BrowserSubjectForUser(99)
	sourceID := uuid.NewString()
	checkoutID := uuid.NewString()

	_, err := app.Issue(context.Background(), issuer, IssueCodeGrantInput{
		Target:     target,
		SourceID:   sourceID,
		CheckoutID: checkoutID,
	})
	require.NoError(t, err)
	require.Equal(t, []gormdb.BrowserReadGrantIssue{{
		IssuerUserID:    41,
		IssuerPrincipal: "browser-user/41",
		TargetUserID:    99,
		SourceID:        sourceID,
		CheckoutID:      checkoutID,
	}}, store.issues)

	canRead, err := app.CanRead(context.Background(), issuer, sourceID, checkoutID)
	require.NoError(t, err)
	require.True(t, canRead)
	require.Len(t, store.canReadCalls, 1)
	require.Equal(t, int64(41), store.canReadCalls[0].subjectUserID)

	_, err = app.Revoke(context.Background(), issuer, "grant-ref")
	require.NoError(t, err)
	require.Equal(t, []struct {
		issuerUserID    int64
		issuerPrincipal string
		grantRef        string
	}{{issuerUserID: 41, issuerPrincipal: "browser-user/41", grantRef: "grant-ref"}}, store.revokes)
}

func TestCodeGrantApplication_DeniesRoleAndNonPersistedIdentityFallbacks(t *testing.T) {
	store := &recordingCodeGrantStore{canRead: true}
	app := &CodeGrantApplication{grants: store}
	target := auth.BrowserSubjectForUser(99)
	sourceID := uuid.NewString()
	checkoutID := uuid.NewString()

	callers := map[string]auth.Identity{
		"master admin":                auth.Admin(),
		"disabled auth admin":         auth.AuthDisabled(),
		"keycard human principal":     auth.ClientWithPrincipal("read-write", uuid.NewString(), "browser-user/41", auth.PrincipalKindHuman),
		"hmac admin session":          auth.Session("admin"),
		"forged noncanonical subject": {Source: auth.SourceSession, BrowserSubject: auth.BrowserSubject{UserID: 41, Principal: "admin", Kind: auth.PrincipalKindHuman}},
	}
	for name, caller := range callers {
		t.Run(name, func(t *testing.T) {
			_, err := app.Issue(context.Background(), caller, IssueCodeGrantInput{Target: target, SourceID: sourceID, CheckoutID: checkoutID})
			require.ErrorIs(t, err, errCodeGrantCallerDenied)

			_, err = app.Revoke(context.Background(), caller, "grant-ref")
			require.ErrorIs(t, err, errCodeGrantCallerDenied)

			canRead, readErr := app.CanRead(context.Background(), caller, sourceID, checkoutID)
			require.NoError(t, readErr)
			require.False(t, canRead)
		})
	}
	require.Empty(t, store.issues)
	require.Empty(t, store.canReadCalls)
	require.Empty(t, store.revokes)
}
