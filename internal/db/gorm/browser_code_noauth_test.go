package gorm

import (
	"context"
	"errors"
	"fmt"
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

func TestNoAuthCodeCatalogShowsOfflineMetadataWithoutSelection(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	ctx := context.Background()
	contexts := NewUCIContextStore(fixture.db)
	source, err := contexts.CreateSource(ctx, CreateSourceInput{AuthRealm: uci.NoAuthCodeRealm, Kind: UCISourceGit, DisplayName: "offline repository"})
	require.NoError(t, err)
	checkout, err := contexts.RegisterCheckout(ctx, RegisterCheckoutInput{SourceID: source.SourceID, WorkstationID: "offline-device", Kind: UCICheckoutWorkingTree, OwnerPrincipal: uci.NoAuthCodePrincipal, LocatorRef: "file:///private/offline-copy"})
	require.NoError(t, err)
	view := publishBrowserCodeContextView(t, fixture.db, checkout, fixture.profile)
	require.NoError(t, fixture.db.Model(&UCICheckout{}).Where("checkout_id = ?", checkout.CheckoutID).Update("state", UCICheckoutOffline).Error)

	entries, err := NewBrowserCodeContextStore(fixture.db).ListNoAuthCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, source.SourceID, entries[0].SourceID)
	require.Equal(t, source.DisplayName, entries[0].SourceLabel)
	require.Equal(t, checkout.CheckoutID, entries[0].CheckoutID)
	require.Contains(t, entries[0].CheckoutLabel, "Offline")
	require.NotContains(t, entries[0].CheckoutLabel, "file://")
	require.Nil(t, entries[0].Context, "offline metadata cannot supply a pin choice even with a published View")
	require.False(t, entries[0].IndexIntentAvailable)
	_, err = NewBrowserCodeContextStore(fixture.db).AuthorizeNoAuthIndexIntent(ctx, source.SourceID, checkout.CheckoutID, fixture.profile.ProfileID, false)
	require.ErrorIs(t, err, ErrBrowserCodeContextDenied)
	ref := uci.ContextRef{SourceID: source.SourceID, CheckoutID: checkout.CheckoutID, ViewID: view.ViewID, AnalysisProfileID: fixture.profile.ProfileID, Generation: view.Generation}
	require.ErrorIs(t, browserCodePublishedContextExists(ctx, fixture.db, ref, uci.NoAuthCodeRealm), ErrBrowserCodeContextDenied)
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

func TestNoAuthCodeCatalogRejectsMoreThan128CurrentCheckouts(t *testing.T) {
	fixture := openUCIProjectionMigrationFixture(t)
	require.NoError(t, workspaceCatalogMigration182().Migrate(fixture.db))
	ctx := context.Background()
	contexts := NewUCIContextStore(fixture.db)
	source, err := contexts.CreateSource(ctx, CreateSourceInput{AuthRealm: uci.NoAuthCodeRealm, Kind: UCISourceGit, DisplayName: "local repository"})
	require.NoError(t, err)
	code := NewBrowserCodeContextStore(fixture.db)
	for i := range browserCodeCatalogMaxEntries + 1 {
		_, err := contexts.RegisterCheckout(ctx, RegisterCheckoutInput{
			SourceID: source.SourceID, WorkstationID: "install-" + uuid.NewString(),
			Kind: UCICheckoutWorkingTree, OwnerPrincipal: uci.NoAuthCodePrincipal,
			LocatorRef: "file:///noauth/" + uuid.NewString(),
		})
		require.NoError(t, err)
		if i == browserCodeCatalogMaxEntries-1 {
			entries, err := code.ListNoAuthCatalog(ctx)
			require.NoError(t, err)
			require.Len(t, entries, browserCodeCatalogMaxEntries)
		}
	}
	entries, err := code.ListNoAuthCatalog(ctx)
	require.ErrorContains(t, err, "local code context catalog exceeds 128 entries")
	require.Nil(t, entries, "a partial catalog must never appear complete")
	require.NoError(t, fixture.db.Model(&UCICheckout{}).Where("source_id = ?", source.SourceID).Update("state", UCICheckoutOffline).Error)
	entries, err = code.ListNoAuthCatalog(ctx)
	require.NoError(t, err, "129 historical offline rows cannot hide the catalog")
	require.Len(t, entries, browserCodeCatalogMaxEntries)
	for _, entry := range entries {
		require.Nil(t, entry.Context)
		require.False(t, entry.IndexIntentAvailable)
		require.Contains(t, entry.CheckoutLabel, "Offline")
	}
}

func TestNoAuthCodeRegisterLocalGitRefuses129thWithoutHidingCatalog(t *testing.T) {
	db, store := openUCIContextMigrationStore(t)
	ctx := context.Background()
	input := RegisterLocalGitInput{AuthRealm: uci.NoAuthCodeRealm, Principal: uci.NoAuthCodePrincipal, WorkstationID: "device", SourceLabel: "local-0", Locator: "file:///worktrees/0"}
	first, err := store.RegisterLocalGit(ctx, input)
	require.NoError(t, err)
	for i := 1; i < browserCodeCatalogMaxEntries; i++ {
		input.SourceID, input.SourceLabel = "", fmt.Sprintf("local-%d", i)
		if i%2 == 0 {
			input.SourceID, input.SourceLabel = first.SourceID, ""
		}
		input.Locator = fmt.Sprintf("file:///worktrees/%d", i)
		_, err := store.RegisterLocalGit(ctx, input)
		require.NoError(t, err, "checkout %d", i)
	}
	code := NewBrowserCodeContextStore(db)
	entries, err := code.ListNoAuthCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, entries, browserCodeCatalogMaxEntries)
	input.SourceID, input.SourceLabel, input.Locator = "", "new-source-at-capacity", "file:///worktrees/excess"
	_, err = store.RegisterLocalGit(ctx, input)
	require.ErrorIs(t, err, uci.ErrNoAuthCodeCatalogFull)
	input.SourceID, input.SourceLabel = first.SourceID, ""
	_, err = store.RegisterLocalGit(ctx, input)
	require.ErrorIs(t, err, uci.ErrNoAuthCodeCatalogFull)
	input.SourceID, input.SourceLabel, input.Locator = "", "local-0", "file:///worktrees/0"
	replayed, err := store.RegisterLocalGit(ctx, input)
	require.NoError(t, err)
	require.Equal(t, first, replayed)
	input.SourceID, input.SourceLabel = first.SourceID, ""
	replayed, err = store.RegisterLocalGit(ctx, input)
	require.NoError(t, err)
	require.Equal(t, first, replayed)
	entries, err = code.ListNoAuthCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, entries, browserCodeCatalogMaxEntries)
	var count int64
	require.NoError(t, db.Model(&UCISource{}).Where("display_name = ?", "new-source-at-capacity").Count(&count).Error)
	require.Zero(t, count, "refused new source must be rolled back")
	input.AuthRealm, input.Principal, input.SourceID, input.SourceLabel, input.Locator = "client", "browser-user/41", "", "authenticated", "file:///worktrees/authenticated"
	_, err = store.RegisterLocalGit(ctx, input)
	require.NoError(t, err, "authenticated registration is independent")
	entries, err = code.ListNoAuthCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, entries, browserCodeCatalogMaxEntries)
	require.NoError(t, db.Model(&UCICheckout{}).Where("checkout_id = ?", first.CheckoutID).Update("state", UCICheckoutOffline).Error)
	entries, err = code.ListNoAuthCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, entries, browserCodeCatalogMaxEntries)
	require.Equal(t, first.CheckoutID, entries[len(entries)-1].CheckoutID)
	require.Nil(t, entries[len(entries)-1].Context)
	input.AuthRealm, input.Principal, input.SourceID, input.SourceLabel, input.Locator = uci.NoAuthCodeRealm, uci.NoAuthCodePrincipal, "", "new-source-at-capacity", "file:///worktrees/excess"
	replacement, err := store.RegisterLocalGit(ctx, input)
	require.NoError(t, err, "offline checkouts no longer consume catalog capacity")
	entries, err = code.ListNoAuthCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, entries, browserCodeCatalogMaxEntries+1, "128 active choices and one offline row have separate bounds")
	require.NotEqual(t, first.SourceID, replacement.SourceID)
}

func TestNoAuthCodeRegisterLocalGitSerializesDistinctBoundaryRegistrations(t *testing.T) {
	db, store := openUCIContextMigrationStore(t)
	ctx := context.Background()
	input := RegisterLocalGitInput{AuthRealm: uci.NoAuthCodeRealm, Principal: uci.NoAuthCodePrincipal, WorkstationID: "device", SourceLabel: "seed", Locator: "file:///worktrees/seed"}
	first, err := store.RegisterLocalGit(ctx, input)
	require.NoError(t, err)
	for i := 1; i < browserCodeCatalogMaxEntries-1; i++ {
		input.SourceID, input.SourceLabel, input.Locator = first.SourceID, "", fmt.Sprintf("file:///worktrees/%d", i)
		_, err := store.RegisterLocalGit(ctx, input)
		require.NoError(t, err)
	}
	inputs := []RegisterLocalGitInput{
		{AuthRealm: uci.NoAuthCodeRealm, Principal: uci.NoAuthCodePrincipal, WorkstationID: "other-device", SourceID: first.SourceID, Locator: "file:///worktrees/concurrent-existing-source"},
		{AuthRealm: uci.NoAuthCodeRealm, Principal: uci.NoAuthCodePrincipal, WorkstationID: "third-device", SourceLabel: "concurrent-new-source", Locator: "file:///worktrees/concurrent-new-source"},
	}
	start := make(chan struct{})
	results := make(chan error, len(inputs))
	for _, registration := range inputs {
		go func() {
			<-start
			_, err := NewUCIContextStore(db).RegisterLocalGit(ctx, registration)
			results <- err
		}()
	}
	close(start)
	var accepted, refused int
	for range inputs {
		switch err := <-results; {
		case err == nil:
			accepted++
		case errors.Is(err, uci.ErrNoAuthCodeCatalogFull):
			refused++
		default:
			t.Fatalf("unexpected registration error: %v", err)
		}
	}
	require.Equal(t, 1, accepted)
	require.Equal(t, 1, refused)
	entries, err := NewBrowserCodeContextStore(db).ListNoAuthCatalog(ctx)
	require.NoError(t, err)
	require.Len(t, entries, browserCodeCatalogMaxEntries)
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
	require.Contains(t, entries[0].CheckoutLabel, "Worktree · local › repository · Device ")
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
	require.Contains(t, entries[0].CheckoutLabel, "Worktree · absolute › worktree · Device ")
	require.Contains(t, entries[1].CheckoutLabel, "Worktree · absolute › worktree · Device ")
	require.NotEqual(t, entries[0].CheckoutLabel, entries[1].CheckoutLabel)
	require.NotContains(t, entries[0].CheckoutLabel, "file://")
	require.NotContains(t, entries[1].CheckoutLabel, "file://")
}
