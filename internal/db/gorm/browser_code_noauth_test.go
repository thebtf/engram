package gorm

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

func TestNoAuthCodeCatalogAndIndexScopeStaySeparateFromHumanAndMemory(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	require.NoError(t, workspaceCatalogMigration182().Migrate(fixture.db))
	ctx := context.Background()
	contexts := NewUCIContextStore(fixture.db)
	code := NewBrowserCodeContextStore(fixture.db)
	first, err := contexts.CreateSource(ctx, CreateSourceInput{AuthRealm: uci.NoAuthCodeRealm, Kind: UCISourceGit, DisplayName: "local repository"})
	require.NoError(t, err)
	second, err := contexts.CreateSource(ctx, CreateSourceInput{AuthRealm: uci.NoAuthCodeRealm, Kind: UCISourceGit, DisplayName: "second repository"})
	require.NoError(t, err)
	historical, err := contexts.CreateSource(ctx, CreateSourceInput{AuthRealm: "auth-disabled", Kind: UCISourceGit, DisplayName: "old private source"})
	require.NoError(t, err)
	human, err := contexts.CreateSource(ctx, CreateSourceInput{AuthRealm: "client", Kind: UCISourceGit, DisplayName: "human private source"})
	require.NoError(t, err)
	register := func(source *UCISource, owner string) *UCICheckout {
		checkout, err := contexts.RegisterCheckout(ctx, RegisterCheckoutInput{SourceID: source.SourceID, WorkstationID: uci.NoAuthCodeWorkstation, Kind: UCICheckoutWorkingTree, OwnerPrincipal: owner, LocatorRef: "file:///noauth/" + uuid.NewString()})
		require.NoError(t, err)
		return checkout
	}
	a := register(first, uci.NoAuthCodePrincipal)
	b := register(first, uci.NoAuthCodePrincipal)
	c := register(second, uci.NoAuthCodePrincipal)
	_ = register(historical, uci.NoAuthCodePrincipal)
	_ = register(human, "browser-user/99")
	viewA := publishBrowserCodeContextView(t, fixture.db, a, fixture.profile)
	viewB := publishBrowserCodeContextView(t, fixture.db, b, fixture.profile)
	profile := fixture.profile.ProfileID
	for _, checkout := range []*UCICheckout{a, b, c} {
		authorized, err := code.AuthorizeNoAuthIndexIntent(ctx, checkout.SourceID, checkout.CheckoutID, profile, checkout.CheckoutID == c.CheckoutID)
		require.NoError(t, err)
		require.Equal(t, checkout.IncarnationID, authorized.Scope.IncarnationID)
		require.Equal(t, uci.NoAuthCodeRealm, authorized.AuthRealm)
	}
	_, err = code.AuthorizeNoAuthIndexIntent(ctx, a.SourceID, a.CheckoutID, profile, true)
	require.ErrorIs(t, err, ErrBrowserCodeContextDenied)
	for _, source := range []*UCISource{historical, human} {
		var checkout UCICheckout
		require.NoError(t, fixture.db.Where("source_id = ?", source.SourceID).First(&checkout).Error)
		_, err := code.AuthorizeNoAuthIndexIntent(ctx, source.SourceID, checkout.CheckoutID, profile, true)
		require.ErrorIs(t, err, ErrBrowserCodeContextDenied)
	}
	_, err = code.AuthorizeNoAuthIndexIntent(ctx, first.SourceID, c.CheckoutID, profile, true)
	require.ErrorIs(t, err, ErrBrowserCodeContextDenied)
	entries, err := code.ListNoAuthCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 3)
	byCheckout := make(map[string]BrowserCodeContextCatalogEntry, len(entries))
	for _, entry := range entries {
		byCheckout[entry.CheckoutID] = entry
	}
	require.Equal(t, viewA.ViewID, byCheckout[a.CheckoutID].Context.ViewID)
	require.Equal(t, viewB.ViewID, byCheckout[b.CheckoutID].Context.ViewID)
	require.Nil(t, byCheckout[c.CheckoutID].Context)
	require.NotEqual(t, byCheckout[a.CheckoutID].Context.ViewID, byCheckout[b.CheckoutID].Context.ViewID)
	restarted := NewBrowserCodeContextStore(fixture.db)
	again, err := restarted.ListNoAuthCatalog(ctx)
	require.NoError(t, err)
	require.Equal(t, entries, again)
}

func TestNoAuthCodeRegistrationReplaysAfterStoreRestartWithoutClaimingHistoricalScope(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	ctx := context.Background()
	input := RegisterLocalGitInput{
		AuthRealm: uci.NoAuthCodeRealm, Principal: uci.NoAuthCodePrincipal,
		WorkstationID: uci.NoAuthCodeWorkstation, SourceLabel: "local checkout", Locator: "file:///local/repository",
	}
	first, err := NewUCIContextStore(fixture.db).RegisterLocalGit(ctx, input)
	require.NoError(t, err)
	again, err := NewUCIContextStore(fixture.db).RegisterLocalGit(ctx, input)
	require.NoError(t, err)
	require.Equal(t, first, again)
	input.AuthRealm = "auth-disabled"
	_, err = NewUCIContextStore(fixture.db).RegisterLocalGit(ctx, input)
	require.Error(t, err)
	entries, err := NewBrowserCodeContextStore(fixture.db).ListNoAuthCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, first.CheckoutID, entries[0].CheckoutID)
}
