package gorm

import (
	"context"
	"testing"
	"time"

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

func TestNoAuthCodeCatalogKeepsCurrentAfterManySupersededViews(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	require.NoError(t, workspaceCatalogMigration182().Migrate(fixture.db))
	ctx := context.Background()
	contexts := NewUCIContextStore(fixture.db)
	source, err := contexts.CreateSource(ctx, CreateSourceInput{AuthRealm: uci.NoAuthCodeRealm, Kind: UCISourceGit, DisplayName: "local repository"})
	require.NoError(t, err)
	checkout, err := contexts.RegisterCheckout(ctx, RegisterCheckoutInput{SourceID: source.SourceID, WorkstationID: "install-a", Kind: UCICheckoutWorkingTree, OwnerPrincipal: uci.NoAuthCodePrincipal, LocatorRef: "file:///local/repository"})
	require.NoError(t, err)
	var previous *UCIView
	var pinned *UCIView
	for generation := 1; generation <= 130; generation++ {
		view := newUCIProjectionView(t, contexts, checkout, fixture.profile, int64(generation), "retained-view-"+uuid.NewString())
		require.NoError(t, fixture.db.Model(&UCIView{}).Where("view_id = ?", view.ViewID).Updates(map[string]any{"state": UCIViewPublished, "published_at": time.Now().UTC()}).Error)
		if generation == 1 {
			pinned = view
		}
		require.NoError(t, fixture.db.Model(&UCICheckout{}).Where("checkout_id = ?", checkout.CheckoutID).Update("current_view_id", view.ViewID).Error)
		if previous != nil {
			require.NoError(t, fixture.db.Model(&UCIView{}).Where("view_id = ?", previous.ViewID).Update("state", UCIViewSuperseded).Error)
		}
		previous = view
		if generation == 130 {
			entries, err := NewBrowserCodeContextStore(fixture.db).ListNoAuthCatalog(ctx)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			require.Equal(t, view.ViewID, entries[0].Context.ViewID)
			old := uci.ContextRef{SourceID: source.SourceID, CheckoutID: checkout.CheckoutID, ViewID: pinned.ViewID, AnalysisProfileID: fixture.profile.ProfileID, Generation: pinned.Generation}
			loaded, err := contexts.LoadContext(ctx, old)
			require.NoError(t, err)
			require.Equal(t, old, loaded.Ref)
		}
	}
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
	require.Contains(t, entries[0].CheckoutLabel, "Worktree · repository · Device ")
	require.NotContains(t, entries[0].CheckoutLabel, "file://")
}

func TestNoAuthCodeSameLocatorDifferentInstallationsKeepDistinctCheckouts(t *testing.T) {
	db, store := openUCIContextMigrationStore(t)
	ctx := context.Background()
	firstInput := RegisterLocalGitInput{AuthRealm: uci.NoAuthCodeRealm, Principal: uci.NoAuthCodePrincipal, WorkstationID: "noauth-install-a", SourceLabel: "local checkout", Locator: "file:///same/absolute/worktree"}
	first, err := store.RegisterLocalGit(ctx, firstInput)
	require.NoError(t, err)
	secondInput := firstInput
	secondInput.WorkstationID = "noauth-install-b"
	second, err := store.RegisterLocalGit(ctx, secondInput)
	require.NoError(t, err)
	require.NotEqual(t, first.CheckoutID, second.CheckoutID)
	require.NotEqual(t, first.IncarnationID, second.IncarnationID)
	require.NotEqual(t, first.SourceID, second.SourceID)
	replayed, err := NewUCIContextStore(db).RegisterLocalGit(ctx, firstInput)
	require.NoError(t, err)
	require.Equal(t, first, replayed)
	require.NoError(t, workspaceCatalogMigration182().Migrate(db))
	entries, err := NewBrowserCodeContextStore(db).ListNoAuthCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	require.Contains(t, entries[0].CheckoutLabel, "Worktree · worktree · Device ")
	require.Contains(t, entries[1].CheckoutLabel, "Worktree · worktree · Device ")
	require.NotEqual(t, entries[0].CheckoutLabel, entries[1].CheckoutLabel)
	require.NotContains(t, entries[0].CheckoutLabel, "file://")
	require.NotContains(t, entries[1].CheckoutLabel, "file://")
}
