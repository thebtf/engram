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
	issues           []gormdb.BrowserReadGrantIssue
	ownerIssues      []gormdb.BrowserReadGrantOwnerIssue
	ownerChoices     []gormdb.BrowserReadGrantOwnerChoice
	ownerChoiceCalls []struct {
		issuerUserID    int64
		issuerPrincipal string
	}
	ownerLabels []struct {
		issuerUserID    int64
		issuerPrincipal string
		choiceRef       string
		label           string
	}
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
	canRead      bool
	current      gormdb.BrowserReadGrant
	currentOK    bool
	currentCalls []int64
}

func (store *recordingCodeGrantStore) Issue(_ context.Context, in gormdb.BrowserReadGrantIssue) (gormdb.BrowserReadGrant, error) {
	store.issues = append(store.issues, in)
	return gormdb.BrowserReadGrant{GrantRef: uuid.NewString()}, nil
}

func (store *recordingCodeGrantStore) IssueOwnerChoice(_ context.Context, in gormdb.BrowserReadGrantOwnerIssue) (gormdb.BrowserReadGrant, error) {
	store.ownerIssues = append(store.ownerIssues, in)
	return gormdb.BrowserReadGrant{GrantRef: uuid.NewString()}, nil
}

func (store *recordingCodeGrantStore) ListOwnerChoices(_ context.Context, issuerUserID int64, issuerPrincipal string) ([]gormdb.BrowserReadGrantOwnerChoice, error) {
	store.ownerChoiceCalls = append(store.ownerChoiceCalls, struct {
		issuerUserID    int64
		issuerPrincipal string
	}{issuerUserID: issuerUserID, issuerPrincipal: issuerPrincipal})
	return append([]gormdb.BrowserReadGrantOwnerChoice(nil), store.ownerChoices...), nil
}

func (store *recordingCodeGrantStore) SetOwnerChoiceLabel(_ context.Context, issuerUserID int64, issuerPrincipal, choiceRef, label string) (gormdb.BrowserReadGrantOwnerChoice, error) {
	store.ownerLabels = append(store.ownerLabels, struct {
		issuerUserID    int64
		issuerPrincipal string
		choiceRef       string
		label           string
	}{issuerUserID: issuerUserID, issuerPrincipal: issuerPrincipal, choiceRef: choiceRef, label: label})
	return gormdb.BrowserReadGrantOwnerChoice{ChoiceRef: choiceRef, RepositoryLabel: "Engram", WorkingCopyLabel: label}, nil
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

func (store *recordingCodeGrantStore) Active(_ context.Context, subjectUserID int64, sourceID, checkoutID string) (gormdb.BrowserReadGrant, bool, error) {
	store.canReadCalls = append(store.canReadCalls, struct {
		subjectUserID int64
		sourceID      string
		checkoutID    string
	}{subjectUserID: subjectUserID, sourceID: sourceID, checkoutID: checkoutID})
	return store.current, store.currentOK, nil
}

func (store *recordingCodeGrantStore) Current(_ context.Context, subjectUserID int64) (gormdb.BrowserReadGrant, bool, error) {
	store.currentCalls = append(store.currentCalls, subjectUserID)
	return store.current, store.currentOK, nil
}

func TestCodeGrantApplication_CarriesOnlyCanonicalBrowserSubject(t *testing.T) {
	choiceRef := uuid.NewString()
	store := &recordingCodeGrantStore{canRead: true, ownerChoices: []gormdb.BrowserReadGrantOwnerChoice{{
		ChoiceRef:        choiceRef,
		RepositoryLabel:  "Engram",
		WorkingCopyLabel: "Studio workstation · release candidate",
	}}}
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

	choices, err := app.ListOwnerChoices(context.Background(), issuer)
	require.NoError(t, err)
	require.Equal(t, store.ownerChoices, choices)
	_, err = app.IssueOnboarding(context.Background(), issuer, IssueOnboardingCodeGrantInput{Target: target, ChoiceRef: choiceRef})
	require.NoError(t, err)
	require.Equal(t, []gormdb.BrowserReadGrantOwnerIssue{{
		IssuerUserID:    41,
		IssuerPrincipal: "browser-user/41",
		TargetUserID:    99,
		ChoiceRef:       choiceRef,
	}}, store.ownerIssues)
	renamed, err := app.SetWorkingCopyLabel(context.Background(), issuer, choiceRef, "Laptop workstation · release candidate")
	require.NoError(t, err)
	require.Equal(t, "Laptop workstation · release candidate", renamed.WorkingCopyLabel)
	require.Equal(t, []struct {
		issuerUserID    int64
		issuerPrincipal string
		choiceRef       string
		label           string
	}{{issuerUserID: 41, issuerPrincipal: "browser-user/41", choiceRef: choiceRef, label: "Laptop workstation · release candidate"}}, store.ownerLabels)

	canRead, err := app.CanRead(context.Background(), issuer, sourceID, checkoutID)
	require.NoError(t, err)
	require.True(t, canRead)
	require.Len(t, store.canReadCalls, 1)
	require.Equal(t, int64(41), store.canReadCalls[0].subjectUserID)

	store.current = gormdb.BrowserReadGrant{SourceID: sourceID, CheckoutID: checkoutID, SubjectUserID: 41}
	store.currentOK = true
	current, currentOK, err := app.Current(context.Background(), issuer)
	require.NoError(t, err)
	require.True(t, currentOK)
	require.Equal(t, store.current, current)
	require.Equal(t, []int64{41}, store.currentCalls)
	active, activeOK, err := app.Active(context.Background(), issuer, sourceID, checkoutID)
	require.NoError(t, err)
	require.True(t, activeOK)
	require.Equal(t, store.current, active)
	require.Len(t, store.canReadCalls, 2)
	require.Equal(t, int64(41), store.canReadCalls[1].subjectUserID)
	require.Equal(t, sourceID, store.canReadCalls[1].sourceID)
	require.Equal(t, checkoutID, store.canReadCalls[1].checkoutID)
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

			_, err = app.ListOwnerChoices(context.Background(), caller)
			require.ErrorIs(t, err, errCodeGrantCallerDenied)

			_, err = app.IssueOnboarding(context.Background(), caller, IssueOnboardingCodeGrantInput{Target: target, ChoiceRef: checkoutID})
			require.ErrorIs(t, err, errCodeGrantCallerDenied)

			_, err = app.SetWorkingCopyLabel(context.Background(), caller, checkoutID, "Studio workstation")
			require.ErrorIs(t, err, errCodeGrantCallerDenied)

			canRead, readErr := app.CanRead(context.Background(), caller, sourceID, checkoutID)
			require.NoError(t, readErr)
			require.False(t, canRead)
		})
	}
	require.Empty(t, store.issues)
	require.Empty(t, store.ownerIssues)
	require.Empty(t, store.ownerChoiceCalls)
	require.Empty(t, store.ownerLabels)
	require.Empty(t, store.canReadCalls)
	require.Empty(t, store.revokes)
}
