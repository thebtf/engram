package gorm

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
	gormlib "gorm.io/gorm"
)

func TestUCIContextStoreLoadIndexBindingFromAuthorizedView(t *testing.T) {
	fixture := newUCIIndexBindingFixture(t, true)
	selector := fixture.viewSelector()

	binding, err := fixture.store.LoadIndexBinding(fixture.ctx, selector)
	require.NoError(t, err)
	require.NoError(t, binding.Validate())
	require.NotNil(t, binding.Context)
	require.Equal(t, fixture.source.SourceID, binding.Scope.SourceID)
	require.Equal(t, fixture.checkout.CheckoutID, binding.Scope.CheckoutID)
	require.Equal(t, fixture.checkout.IncarnationID, binding.Scope.IncarnationID)
	require.Equal(t, fixture.profile.ProfileID, binding.ProfileID)
	require.Equal(t, fixture.checkout.LocatorRef, binding.LocalRootID)
	require.Equal(t, fixture.checkout.WorkstationID, binding.WorkstationID)
	require.Equal(t, fixture.view.ViewID, binding.Context.ViewID)
	require.Equal(t, fixture.view.Generation, binding.Context.Generation)
	require.Equal(t, fixture.profile.ProfileID, binding.Context.AnalysisProfileID)
	require.Equal(t, fixture.space.SpaceID, *binding.Context.SpaceID)
	require.NotSame(t, selector.Context, binding.Context)
	require.NotSame(t, selector.Context.SpaceID, binding.Context.SpaceID)

	checkoutBinding, err := fixture.store.LoadIndexBinding(fixture.ctx, fixture.checkoutSelector(fixture.profile.ProfileID))
	require.NoError(t, err)
	require.NoError(t, checkoutBinding.Validate())
	require.NotNil(t, checkoutBinding.Context)
	require.Equal(t, fixture.view.ViewID, checkoutBinding.Context.ViewID)
	require.Equal(t, fixture.view.Generation, checkoutBinding.Context.Generation)
	require.Nil(t, checkoutBinding.Context.SpaceID, "checkout selection must not infer a Space from a path or label")

	boundSpaceID := *binding.Context.SpaceID
	*selector.Context.SpaceID = uuid.NewString()
	require.Equal(t, boundSpaceID, *binding.Context.SpaceID, "binding must not retain caller-owned context pointers")

	clone := binding.Clone()
	require.NotSame(t, binding.Context, clone.Context)
	require.NotSame(t, binding.Context.SpaceID, clone.Context.SpaceID)
	*clone.Context.SpaceID = uuid.NewString()
	require.Equal(t, boundSpaceID, *binding.Context.SpaceID, "binding clone must not mutate the returned binding")
}

func TestUCIContextStoreLoadIndexBindingForRegisteredCheckoutWithoutView(t *testing.T) {
	fixture := newUCIIndexBindingFixture(t, false)

	binding, err := fixture.store.LoadIndexBinding(fixture.ctx, fixture.checkoutSelector(fixture.profile.ProfileID))
	require.NoError(t, err)
	require.NoError(t, binding.Validate())
	require.Nil(t, binding.Context, "a registered unindexed checkout has no current View rather than a lookup error")
	require.Equal(t, fixture.source.SourceID, binding.Scope.SourceID)
	require.Equal(t, fixture.checkout.CheckoutID, binding.Scope.CheckoutID)
	require.Equal(t, fixture.checkout.IncarnationID, binding.Scope.IncarnationID)
	require.Equal(t, fixture.profile.ProfileID, binding.ProfileID)
	require.Equal(t, fixture.checkout.LocatorRef, binding.LocalRootID)
	require.Equal(t, fixture.checkout.WorkstationID, binding.WorkstationID)
}

func TestUCIContextStoreLoadIndexBindingRejectsMismatches(t *testing.T) {
	t.Run("wrong source", func(t *testing.T) {
		fixture := newUCIIndexBindingFixture(t, true)
		otherSource, err := fixture.store.CreateSource(fixture.ctx, CreateSourceInput{
			AuthRealm:   fixture.realm,
			Kind:        UCISourceDirectory,
			DisplayName: "other-source-" + uuid.NewString(),
		})
		require.NoError(t, err)
		selector := fixture.viewSelector()
		selector.Context.SourceID = otherSource.SourceID

		_, err = fixture.store.LoadIndexBinding(fixture.ctx, selector)
		require.Error(t, err)
	})

	t.Run("wrong profile", func(t *testing.T) {
		fixture := newUCIIndexBindingFixture(t, true)
		otherProfile, err := fixture.store.CreateProfile(fixture.ctx, newUCIContextMigrationProfileInput(uuid.NewString()))
		require.NoError(t, err)

		_, err = fixture.store.LoadIndexBinding(fixture.ctx, fixture.checkoutSelector(otherProfile.ProfileID))
		require.Error(t, err)
	})

	t.Run("inactive checkout", func(t *testing.T) {
		fixture := newUCIIndexBindingFixture(t, true)
		require.NoError(t, fixture.db.Model(&UCICheckout{}).
			Where("checkout_id = ?", fixture.checkout.CheckoutID).
			Update("state", UCICheckoutOffline).Error)

		_, err := fixture.store.LoadIndexBinding(fixture.ctx, fixture.checkoutSelector(fixture.profile.ProfileID))
		require.Error(t, err)
	})

	t.Run("cross realm space", func(t *testing.T) {
		fixture := newUCIIndexBindingFixture(t, true)
		otherSpace, err := fixture.store.CreateSpace(fixture.ctx, CreateSpaceInput{
			AuthRealm:   "other-realm-" + uuid.NewString(),
			DisplayName: "other-space-" + uuid.NewString(),
		})
		require.NoError(t, err)
		selector := fixture.viewSelector()
		selector.Context.SpaceID = &otherSpace.SpaceID

		_, err = fixture.store.LoadIndexBinding(fixture.ctx, selector)
		require.Error(t, err)
	})
}

type uciIndexBindingFixture struct {
	ctx      context.Context
	db       *gormlib.DB
	store    *UCIContextStore
	realm    string
	space    *UCISpace
	source   *UCISource
	checkout *UCICheckout
	profile  *UCIAnalysisProfile
	view     *UCIView
}

func newUCIIndexBindingFixture(t *testing.T, publishCurrentView bool) uciIndexBindingFixture {
	t.Helper()
	db, store := openUCIContextMigrationStore(t)
	ctx := context.Background()
	token := uuid.NewString()
	realm := "uci-index-binding-realm-" + token

	space, err := store.CreateSpace(ctx, CreateSpaceInput{AuthRealm: realm, DisplayName: "space-" + token})
	require.NoError(t, err)
	source, err := store.CreateSource(ctx, CreateSourceInput{AuthRealm: realm, Kind: UCISourceGit, DisplayName: "source-" + token})
	require.NoError(t, err)
	require.NoError(t, store.LinkSpaceSource(ctx, LinkSpaceSourceInput{SpaceID: space.SpaceID, SourceID: source.SourceID}))
	checkout, err := store.RegisterCheckout(ctx, RegisterCheckoutInput{
		SourceID:       source.SourceID,
		WorkstationID:  "workstation-" + token,
		Kind:           UCICheckoutWorkingTree,
		OwnerPrincipal: "principal-" + token,
		LocatorRef:     "opaque-local-root-" + token,
	})
	require.NoError(t, err)
	profile, err := store.CreateProfile(ctx, newUCIContextMigrationProfileInput(token))
	require.NoError(t, err)

	fixture := uciIndexBindingFixture{
		ctx:      ctx,
		db:       db,
		store:    store,
		realm:    realm,
		space:    space,
		source:   source,
		checkout: checkout,
		profile:  profile,
	}
	if !publishCurrentView {
		return fixture
	}

	view, err := store.CreateView(ctx, newUCIContextMigrationViewInput(checkout, profile, 1, token))
	require.NoError(t, err)
	publishedAt := time.Now().UTC()
	require.NoError(t, db.Model(&UCIView{}).Where("view_id = ?", view.ViewID).Updates(map[string]any{
		"state":        UCIViewPublished,
		"published_at": publishedAt,
	}).Error)
	require.NoError(t, db.Model(&UCICheckout{}).Where("checkout_id = ?", checkout.CheckoutID).Update("current_view_id", view.ViewID).Error)
	view.State = UCIViewPublished
	view.PublishedAt = &publishedAt
	checkout.CurrentViewID = &view.ViewID
	fixture.view = view
	return fixture
}

func (fixture uciIndexBindingFixture) viewSelector() ucidomain.IndexBindingSelector {
	spaceID := fixture.space.SpaceID
	return ucidomain.IndexBindingSelector{Context: &ucidomain.ContextRef{
		SpaceID:           &spaceID,
		SourceID:          fixture.source.SourceID,
		CheckoutID:        fixture.checkout.CheckoutID,
		ViewID:            fixture.view.ViewID,
		AnalysisProfileID: fixture.profile.ProfileID,
		Generation:        fixture.view.Generation,
	}}
}

func (fixture uciIndexBindingFixture) checkoutSelector(profileID string) ucidomain.IndexBindingSelector {
	scope := ucidomain.IndexScope{
		SourceID:      fixture.source.SourceID,
		CheckoutID:    fixture.checkout.CheckoutID,
		IncarnationID: fixture.checkout.IncarnationID,
	}
	return ucidomain.IndexBindingSelector{Scope: &scope, ProfileID: profileID}
}
