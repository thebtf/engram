package gorm

import (
	"encoding/json"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

func TestUCIContextStoreContextCatalogAndOwnerAuthorizer(t *testing.T) {
	t.Run("loads the exact tuple and returns independent references", func(t *testing.T) {
		fixture := newUCIIndexBindingFixture(t, true)
		ref := uciContextAuthorityRef(fixture)

		record, err := fixture.store.LoadContext(fixture.ctx, ref)
		require.NoError(t, err)
		require.Equal(t, fixture.realm, record.AuthRealm)
		require.Equal(t, ref.SourceID, record.Ref.SourceID)
		require.Equal(t, ref.CheckoutID, record.Ref.CheckoutID)
		require.Equal(t, ref.ViewID, record.Ref.ViewID)
		require.Equal(t, ref.AnalysisProfileID, record.Ref.AnalysisProfileID)
		require.Equal(t, ref.Generation, record.Ref.Generation)
		require.NotNil(t, record.Ref.SpaceID)
		require.NotSame(t, ref.SpaceID, record.Ref.SpaceID)

		spaceID := *record.Ref.SpaceID
		*ref.SpaceID = uuid.NewString()
		require.Equal(t, spaceID, *record.Ref.SpaceID, "catalog records must not retain caller-owned space pointers")
		*record.Ref.SpaceID = uuid.NewString()

		reloaded, err := fixture.store.LoadContext(fixture.ctx, uciContextAuthorityRef(fixture))
		require.NoError(t, err)
		require.Equal(t, spaceID, *reloaded.Ref.SpaceID, "mutating a returned record must not alter catalog state")
		require.NotSame(t, record.Ref.SpaceID, reloaded.Ref.SpaceID)

		require.NoError(t, NewUCIContextAuthorizer(fixture.store).AuthorizeContext(fixture.ctx, uciContextAuthorityAccess(fixture)))
	})

	t.Run("rejects every changed catalog tuple component", func(t *testing.T) {
		fixture := newUCIIndexBindingFixture(t, true)
		otherProfile, err := fixture.store.CreateProfile(fixture.ctx, newUCIContextMigrationProfileInput(uuid.NewString()))
		require.NoError(t, err)
		ref := uciContextAuthorityRef(fixture)

		for _, test := range []struct {
			name   string
			mutate func(*uci.ContextRef)
		}{
			{name: "source", mutate: func(candidate *uci.ContextRef) { candidate.SourceID = uuid.NewString() }},
			{name: "checkout", mutate: func(candidate *uci.ContextRef) { candidate.CheckoutID = uuid.NewString() }},
			{name: "view", mutate: func(candidate *uci.ContextRef) { candidate.ViewID = uuid.NewString() }},
			{name: "profile", mutate: func(candidate *uci.ContextRef) { candidate.AnalysisProfileID = otherProfile.ProfileID }},
			{name: "generation", mutate: func(candidate *uci.ContextRef) { candidate.Generation++ }},
		} {
			t.Run(test.name, func(t *testing.T) {
				candidate := ref
				test.mutate(&candidate)

				_, err := fixture.store.LoadContext(fixture.ctx, candidate)
				require.ErrorIs(t, err, errUCIContextCatalogNotFound)
			})
		}
	})

	t.Run("requires an active matching space membership", func(t *testing.T) {
		t.Run("unlinked space", func(t *testing.T) {
			fixture := newUCIIndexBindingFixture(t, true)
			unlinked, err := fixture.store.CreateSpace(fixture.ctx, CreateSpaceInput{
				AuthRealm:   fixture.realm,
				DisplayName: "unlinked-space-" + uuid.NewString(),
			})
			require.NoError(t, err)
			ref := uciContextAuthorityRef(fixture)
			ref.SpaceID = &unlinked.SpaceID

			_, err = fixture.store.LoadContext(fixture.ctx, ref)
			require.ErrorIs(t, err, errUCIContextCatalogNotFound)
		})

		t.Run("space linked to another source", func(t *testing.T) {
			fixture := newUCIIndexBindingFixture(t, true)
			otherSource, err := fixture.store.CreateSource(fixture.ctx, CreateSourceInput{
				AuthRealm:   fixture.realm,
				Kind:        UCISourceDirectory,
				DisplayName: "other-source-" + uuid.NewString(),
			})
			require.NoError(t, err)
			otherSpace, err := fixture.store.CreateSpace(fixture.ctx, CreateSpaceInput{
				AuthRealm:   fixture.realm,
				DisplayName: "other-space-" + uuid.NewString(),
			})
			require.NoError(t, err)
			require.NoError(t, fixture.store.LinkSpaceSource(fixture.ctx, LinkSpaceSourceInput{
				SpaceID:  otherSpace.SpaceID,
				SourceID: otherSource.SourceID,
			}))
			ref := uciContextAuthorityRef(fixture)
			ref.SpaceID = &otherSpace.SpaceID

			_, err = fixture.store.LoadContext(fixture.ctx, ref)
			require.ErrorIs(t, err, errUCIContextCatalogNotFound)
		})

		t.Run("space in another realm", func(t *testing.T) {
			fixture := newUCIIndexBindingFixture(t, true)
			otherRealmSpace, err := fixture.store.CreateSpace(fixture.ctx, CreateSpaceInput{
				AuthRealm:   "other-realm-" + uuid.NewString(),
				DisplayName: "other-realm-space-" + uuid.NewString(),
			})
			require.NoError(t, err)
			ref := uciContextAuthorityRef(fixture)
			ref.SpaceID = &otherRealmSpace.SpaceID

			_, err = fixture.store.LoadContext(fixture.ctx, ref)
			require.ErrorIs(t, err, errUCIContextCatalogNotFound)
		})

		t.Run("retired space", func(t *testing.T) {
			fixture := newUCIIndexBindingFixture(t, true)
			require.NoError(t, fixture.db.Model(&UCISpace{}).Where("space_id = ?", fixture.space.SpaceID).Update("state", UCISpaceRetired).Error)

			_, err := fixture.store.LoadContext(fixture.ctx, uciContextAuthorityRef(fixture))
			require.ErrorIs(t, err, errUCIContextCatalogNotFound)
		})
	})

	t.Run("rejects inactive catalog records and retired views", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			mutate func(t *testing.T, fixture uciIndexBindingFixture)
		}{
			{
				name: "offline source",
				mutate: func(t *testing.T, fixture uciIndexBindingFixture) {
					require.NoError(t, fixture.db.Model(&UCISource{}).Where("source_id = ?", fixture.source.SourceID).Update("state", UCISourceOffline).Error)
				},
			},
			{
				name: "offline checkout",
				mutate: func(t *testing.T, fixture uciIndexBindingFixture) {
					require.NoError(t, fixture.db.Model(&UCICheckout{}).Where("checkout_id = ?", fixture.checkout.CheckoutID).Update("state", UCICheckoutOffline).Error)
				},
			},
			{
				name: "retired view",
				mutate: func(t *testing.T, fixture uciIndexBindingFixture) {
					require.NoError(t, fixture.db.Model(&UCICheckout{}).Where("checkout_id = ?", fixture.checkout.CheckoutID).Update("current_view_id", nil).Error)
					require.NoError(t, fixture.db.Model(&UCIView{}).Where("view_id = ?", fixture.view.ViewID).Update("state", UCIViewRetired).Error)
				},
			},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture := newUCIIndexBindingFixture(t, true)
				test.mutate(t, fixture)

				_, err := fixture.store.LoadContext(fixture.ctx, uciContextAuthorityRef(fixture))
				require.ErrorIs(t, err, errUCIContextCatalogNotFound)
			})
		}
	})

	t.Run("rejects staging views", func(t *testing.T) {
		fixture := newUCIIndexBindingFixture(t, true)
		staging, err := fixture.store.CreateView(fixture.ctx, newUCIContextMigrationViewInput(
			fixture.checkout,
			fixture.profile,
			fixture.view.Generation+1,
			uuid.NewString(),
		))
		require.NoError(t, err)
		ref := uciContextAuthorityRef(fixture)
		ref.ViewID = staging.ViewID
		ref.Generation = staging.Generation

		_, err = fixture.store.LoadContext(fixture.ctx, ref)
		require.ErrorIs(t, err, errUCIContextCatalogNotFound)
	})

	t.Run("allows a superseded historical tuple", func(t *testing.T) {
		fixture := newUCIIndexBindingFixture(t, true)
		historical := uciContextAuthorityRef(fixture)
		_ = uciContextAuthorityPublishView(t, fixture, fixture.checkout, fixture.view.Generation+1)
		require.NoError(t, fixture.db.Model(&UCIView{}).Where("view_id = ?", fixture.view.ViewID).Update("state", UCIViewSuperseded).Error)

		record, err := fixture.store.LoadContext(fixture.ctx, historical)
		require.NoError(t, err)
		require.Equal(t, historical, record.Ref)
		metadata, err := fixture.store.LoadContextMetadata(fixture.ctx, historical)
		require.NoError(t, err)
		require.NotEmpty(t, metadata.View)
		require.NoError(t, NewUCIContextAuthorizer(fixture.store).AuthorizeContext(fixture.ctx, uciContextAuthorityAccess(fixture)))
	})

	t.Run("rejects malformed checkout workstation ownership", func(t *testing.T) {
		fixture := newUCIIndexBindingFixture(t, true)
		_, err := fixture.store.RegisterCheckout(fixture.ctx, RegisterCheckoutInput{
			SourceID:       fixture.source.SourceID,
			WorkstationID:  " invalid-workstation ",
			Kind:           UCICheckoutWorkingTree,
			OwnerPrincipal: fixture.checkout.OwnerPrincipal,
			LocatorRef:     "private-invalid-workstation-" + uuid.NewString(),
		})
		require.ErrorContains(t, err, "workstation_id")
	})

	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, fixture uciIndexBindingFixture, access *uci.ContextAccess)
	}{
		{
			name: "other principal",
			mutate: func(_ *testing.T, _ uciIndexBindingFixture, access *uci.ContextAccess) {
				access.Principal = "other-principal"
			},
		},
		{
			name: "other realm",
			mutate: func(_ *testing.T, _ uciIndexBindingFixture, access *uci.ContextAccess) {
				access.AuthRealm = "other-realm"
			},
		},
		{
			name: "other source",
			mutate: func(_ *testing.T, _ uciIndexBindingFixture, access *uci.ContextAccess) {
				access.SourceID = uuid.NewString()
			},
		},
		{
			name: "other checkout",
			mutate: func(_ *testing.T, _ uciIndexBindingFixture, access *uci.ContextAccess) {
				access.CheckoutID = uuid.NewString()
			},
		},
		{
			name: "offline source",
			mutate: func(t *testing.T, fixture uciIndexBindingFixture, _ *uci.ContextAccess) {
				require.NoError(t, fixture.db.Model(&UCISource{}).Where("source_id = ?", fixture.source.SourceID).Update("state", UCISourceOffline).Error)
			},
		},
		{
			name: "offline checkout",
			mutate: func(t *testing.T, fixture uciIndexBindingFixture, _ *uci.ContextAccess) {
				require.NoError(t, fixture.db.Model(&UCICheckout{}).Where("checkout_id = ?", fixture.checkout.CheckoutID).Update("state", UCICheckoutOffline).Error)
			},
		},
		{
			name: "empty principal",
			mutate: func(_ *testing.T, _ uciIndexBindingFixture, access *uci.ContextAccess) {
				access.Principal = ""
			},
		},
		{
			name: "master realm",
			mutate: func(_ *testing.T, _ uciIndexBindingFixture, access *uci.ContextAccess) {
				access.AuthRealm = "master"
			},
		},
		{
			name: "authentication disabled realm",
			mutate: func(_ *testing.T, _ uciIndexBindingFixture, access *uci.ContextAccess) {
				access.AuthRealm = "auth-disabled"
			},
		},
	} {
		t.Run("denies "+test.name, func(t *testing.T) {
			fixture := newUCIIndexBindingFixture(t, true)
			access := uciContextAuthorityAccess(fixture)
			test.mutate(t, fixture, &access)

			err := NewUCIContextAuthorizer(fixture.store).AuthorizeContext(fixture.ctx, access)
			require.ErrorIs(t, err, errUCIContextAuthorizationDenied)
		})
	}
}

func TestUCIContextStoreListAuthorizedContextsIsCurrentScopedAndDeterministic(t *testing.T) {
	fixture := newUCIIndexBindingFixture(t, true)
	register := func(sourceID, owner, label string) *UCICheckout {
		t.Helper()
		checkout, err := fixture.store.RegisterCheckout(fixture.ctx, RegisterCheckoutInput{
			SourceID:       sourceID,
			WorkstationID:  label + "-workstation-" + uuid.NewString(),
			Kind:           UCICheckoutWorkingTree,
			OwnerPrincipal: owner,
			LocatorRef:     "private-" + label + "-locator-" + uuid.NewString(),
		})
		require.NoError(t, err)
		return checkout
	}

	owned := register(fixture.source.SourceID, fixture.checkout.OwnerPrincipal, "owned")
	ownedHistorical := uciContextAuthorityPublishView(t, fixture, owned, 1)
	ownedView := uciContextAuthorityPublishView(t, fixture, owned, 2)
	foreign := register(fixture.source.SourceID, "foreign-principal-"+uuid.NewString(), "foreign")
	_ = uciContextAuthorityPublishView(t, fixture, foreign, 1)
	unindexed := register(fixture.source.SourceID, fixture.checkout.OwnerPrincipal, "unindexed")
	offlineCheckout := register(fixture.source.SourceID, fixture.checkout.OwnerPrincipal, "offline")
	_ = uciContextAuthorityPublishView(t, fixture, offlineCheckout, 1)
	require.NoError(t, fixture.db.Model(&UCICheckout{}).Where("checkout_id = ?", offlineCheckout.CheckoutID).Update("state", UCICheckoutOffline).Error)

	inactiveSource, err := fixture.store.CreateSource(fixture.ctx, CreateSourceInput{
		AuthRealm:   fixture.realm,
		Kind:        UCISourceDirectory,
		DisplayName: "inactive-source-" + uuid.NewString(),
	})
	require.NoError(t, err)
	inactiveSourceCheckout := register(inactiveSource.SourceID, fixture.checkout.OwnerPrincipal, "inactive-source")
	_ = uciContextAuthorityPublishView(t, fixture, inactiveSourceCheckout, 1)
	require.NoError(t, fixture.db.Model(&UCISource{}).Where("source_id = ?", inactiveSource.SourceID).Update("state", UCISourceOffline).Error)

	expected := []uci.ContextRef{
		{
			SourceID:          fixture.source.SourceID,
			CheckoutID:        fixture.checkout.CheckoutID,
			ViewID:            fixture.view.ViewID,
			AnalysisProfileID: fixture.profile.ProfileID,
			Generation:        fixture.view.Generation,
		},
		{
			SourceID:          fixture.source.SourceID,
			CheckoutID:        owned.CheckoutID,
			ViewID:            ownedView.ViewID,
			AnalysisProfileID: fixture.profile.ProfileID,
			Generation:        ownedView.Generation,
		},
	}
	sort.Slice(expected, func(left, right int) bool {
		if expected[left].SourceID != expected[right].SourceID {
			return expected[left].SourceID < expected[right].SourceID
		}
		if expected[left].CheckoutID != expected[right].CheckoutID {
			return expected[left].CheckoutID < expected[right].CheckoutID
		}
		return expected[left].ViewID < expected[right].ViewID
	})

	refs, err := fixture.store.ListAuthorizedContexts(fixture.ctx, fixture.realm, fixture.checkout.OwnerPrincipal, uciContextListMax)
	require.NoError(t, err)
	require.Equal(t, expected, refs)
	repeated, err := fixture.store.ListAuthorizedContexts(fixture.ctx, fixture.realm, fixture.checkout.OwnerPrincipal, uciContextListMax)
	require.NoError(t, err)
	require.Equal(t, refs, repeated, "listing must be deterministic")
	for _, ref := range refs {
		require.Nil(t, ref.SpaceID, "listing must not fabricate a space selection")
		require.NotEqual(t, unindexed.CheckoutID, ref.CheckoutID, "registered no-View checkouts are not listable contexts")
		require.NotEqual(t, foreign.CheckoutID, ref.CheckoutID, "another owner must not appear in the list")
		require.NotEqual(t, offlineCheckout.CheckoutID, ref.CheckoutID, "offline checkouts must not appear in the list")
		require.NotEqual(t, ownedHistorical.ViewID, ref.ViewID, "historical published views must not appear in the list")
		require.NotEqual(t, inactiveSourceCheckout.CheckoutID, ref.CheckoutID, "inactive sources must not appear in the list")
	}

	limited, err := fixture.store.ListAuthorizedContexts(fixture.ctx, fixture.realm, fixture.checkout.OwnerPrincipal, 1)
	require.NoError(t, err)
	require.Equal(t, expected[:1], limited)
	for _, limit := range []int{-1, 0, uciContextListMax + 1} {
		_, err := fixture.store.ListAuthorizedContexts(fixture.ctx, fixture.realm, fixture.checkout.OwnerPrincipal, limit)
		require.ErrorContains(t, err, "limit must be between 1 and")
	}

	otherRealm, err := fixture.store.ListAuthorizedContexts(fixture.ctx, "other-realm", fixture.checkout.OwnerPrincipal, uciContextListMax)
	require.NoError(t, err)
	require.Empty(t, otherRealm)

	encoded, err := json.Marshal(refs)
	require.NoError(t, err)
	for _, forbidden := range []string{
		fixture.checkout.LocatorRef,
		fixture.checkout.WorkstationID,
		fixture.checkout.OwnerPrincipal,
		owned.LocatorRef,
		owned.WorkstationID,
		foreign.LocatorRef,
		foreign.WorkstationID,
	} {
		require.NotContains(t, string(encoded), forbidden)
	}
}

func TestUCIContextStoreContextMetadataOmitsPrivateCheckoutFields(t *testing.T) {
	fixture := newUCIIndexBindingFixture(t, true)
	metadata, err := fixture.store.LoadContextMetadata(fixture.ctx, uciContextAuthorityRef(fixture))
	require.NoError(t, err)
	require.Equal(t, fixture.source.DisplayName, metadata.Source)
	require.Equal(t, string(fixture.checkout.Kind), metadata.Checkout)
	require.NotNil(t, fixture.view.RefLabel)
	require.Equal(t, *fixture.view.RefLabel, metadata.View)

	encoded, err := json.Marshal(metadata)
	require.NoError(t, err)
	for _, forbidden := range []string{
		fixture.source.AuthRealm,
		fixture.source.SourceID,
		fixture.checkout.CheckoutID,
		fixture.checkout.IncarnationID,
		fixture.checkout.LocatorRef,
		fixture.checkout.WorkstationID,
		fixture.checkout.OwnerPrincipal,
		fixture.view.ViewID,
		fixture.profile.ProfileID,
	} {
		require.NotContains(t, string(encoded), forbidden)
	}
}

func TestUCIContextStoreContextMetadataLabelsDetachedAndUnbornViews(t *testing.T) {
	for _, test := range []struct {
		name      string
		headOID   *string
		refLabel  *string
		wantLabel string
	}{
		{
			name:      "explicit ref label wins",
			refLabel:  func() *string { value := "release/2026-09"; return &value }(),
			wantLabel: "release/2026-09",
		},
		{
			name:      "detached",
			headOID:   func() *string { value := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"; return &value }(),
			wantLabel: "detached",
		},
		{
			name:      "unborn",
			wantLabel: "unborn",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newUCIIndexBindingFixture(t, true)
			require.NoError(t, fixture.db.Model(&UCIView{}).Where("view_id = ?", fixture.view.ViewID).Updates(map[string]any{
				"head_oid":  test.headOID,
				"ref_label": test.refLabel,
			}).Error)

			metadata, err := fixture.store.LoadContextMetadata(fixture.ctx, uciContextAuthorityRef(fixture))
			require.NoError(t, err)
			require.Equal(t, test.wantLabel, metadata.View)
		})
	}
}

func uciContextAuthorityAccess(fixture uciIndexBindingFixture) uci.ContextAccess {
	return uci.ContextAccess{
		AuthRealm:  fixture.realm,
		Principal:  fixture.checkout.OwnerPrincipal,
		SourceID:   fixture.source.SourceID,
		CheckoutID: fixture.checkout.CheckoutID,
	}
}

func uciContextAuthorityRef(fixture uciIndexBindingFixture) uci.ContextRef {
	spaceID := fixture.space.SpaceID
	return uci.ContextRef{
		SpaceID:           &spaceID,
		SourceID:          fixture.source.SourceID,
		CheckoutID:        fixture.checkout.CheckoutID,
		ViewID:            fixture.view.ViewID,
		AnalysisProfileID: fixture.profile.ProfileID,
		Generation:        fixture.view.Generation,
	}
}

func uciContextAuthorityPublishView(t *testing.T, fixture uciIndexBindingFixture, checkout *UCICheckout, generation int64) *UCIView {
	t.Helper()
	view, err := fixture.store.CreateView(fixture.ctx, newUCIContextMigrationViewInput(checkout, fixture.profile, generation, uuid.NewString()))
	require.NoError(t, err)
	publishedAt := time.Now().UTC()
	require.NoError(t, fixture.db.Model(&UCIView{}).Where("view_id = ?", view.ViewID).Updates(map[string]any{
		"state":        UCIViewPublished,
		"published_at": publishedAt,
	}).Error)
	require.NoError(t, fixture.db.Model(&UCICheckout{}).Where("checkout_id = ?", checkout.CheckoutID).Update("current_view_id", view.ViewID).Error)
	view.State = UCIViewPublished
	view.PublishedAt = &publishedAt
	checkout.CurrentViewID = &view.ViewID
	return view
}
