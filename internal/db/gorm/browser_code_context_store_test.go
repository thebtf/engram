package gorm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
	gormlib "gorm.io/gorm"
)

type browserCodeContextFixture struct {
	db        *gormlib.DB
	store     *BrowserCodeContextStore
	user      *User
	source    *UCISource
	checkout  *UCICheckout
	profile   *UCIAnalysisProfile
	view      *UCIView
	grant     BrowserReadGrant
	caller    BrowserTabBindingCaller
	binding   BrowserTabBinding
	materials browserTabBindingMaterials
}

func TestBrowserCodeContextStore_CatalogDistinguishesPublishedAndInitialTargets(t *testing.T) {
	fixture := newBrowserCodeContextFixture(t)
	unpublished, _ := fixture.addUnpublishedCheckout(t)
	expiredCheckout, expiredGrant := fixture.addUnpublishedCheckout(t)
	expiredAt := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, fixture.db.Model(&BrowserReadGrant{}).Where("grant_ref = ?", expiredGrant.GrantRef).Updates(map[string]any{
		"issued_at":  expiredAt.Add(-time.Minute),
		"expires_at": expiredAt,
	}).Error)

	entries, err := fixture.store.ListCatalog(context.Background(), fixture.user.ID)
	require.NoError(t, err)
	require.Len(t, entries, 2)

	byCheckout := make(map[string]BrowserCodeContextCatalogEntry, len(entries))
	for _, entry := range entries {
		byCheckout[entry.CheckoutID] = entry
	}

	published := byCheckout[fixture.checkout.CheckoutID]
	require.Equal(t, fixture.source.SourceID, published.SourceID)
	require.Equal(t, fixture.reference(), *published.Context)
	require.False(t, published.IndexIntentAvailable)
	require.NotEmpty(t, published.SourceLabel)
	require.Empty(t, published.CheckoutLabel, "existing unnamed checkouts must not receive a synthetic UUID label")
	require.NotEmpty(t, published.ViewLabel)
	require.NotNil(t, published.SnapshotPublishedAt)

	initial := byCheckout[unpublished.CheckoutID]
	require.Equal(t, fixture.source.SourceID, initial.SourceID)
	require.Nil(t, initial.Context)
	require.True(t, initial.IndexIntentAvailable)

	var expired BrowserReadGrant
	require.NoError(t, fixture.db.Where("grant_ref = ?", expiredGrant.GrantRef).First(&expired).Error)
	require.Equal(t, BrowserReadGrantExpired, expired.State)
	require.NotContains(t, byCheckout, expiredCheckout.CheckoutID)
}

func TestBrowserCodeContextCatalogRowUsesSafeOptionalWorkingCopyMetadata(t *testing.T) {
	row := browserCodeContextCatalogRow{
		SourceID:      uuid.NewString(),
		SourceLabel:   "Engram",
		CheckoutID:    uuid.NewString(),
		CheckoutKind:  string(UCICheckoutWorkingTree),
		CheckoutLabel: "",
	}

	entry, err := row.catalogEntry()
	require.NoError(t, err)
	require.Empty(t, entry.CheckoutLabel, "existing unnamed checkouts stay unnamed")

	for _, label := range []string{uuid.NewString(), "file:///private/worktree"} {
		row.CheckoutLabel = label
		_, err = row.catalogEntry()
		require.ErrorIs(t, err, ErrBrowserCodeContextDenied, "locator or UUID metadata must never become working-copy task language")
	}

	row.CheckoutLabel = "Studio workstation · release candidate"
	entry, err = row.catalogEntry()
	require.NoError(t, err)
	require.Equal(t, row.CheckoutLabel, entry.CheckoutLabel)
}

func TestBrowserCodeContextStore_CatalogDistinguishesSameBranchWorktreesByOwnerLabels(t *testing.T) {
	fixture := newBrowserCodeContextFixture(t)
	ctx := context.Background()
	principal := browserReadGrantPrincipal(fixture.user.ID)
	grants := NewBrowserReadGrantStore(fixture.db)
	secondCheckout, _ := fixture.addUnpublishedCheckout(t)
	secondView := publishBrowserCodeContextView(t, fixture.db, secondCheckout, fixture.profile)
	branch := "refs/heads/release"
	publishedAt := time.Now().UTC()
	require.NoError(t, fixture.db.Model(&UCIView{}).Where("view_id = ?", fixture.view.ViewID).Updates(map[string]any{
		"ref_label":    branch,
		"published_at": publishedAt,
	}).Error)
	require.NoError(t, fixture.db.Model(&UCIView{}).Where("view_id = ?", secondView.ViewID).Update("ref_label", branch).Error)

	_, err := grants.SetOwnerChoiceLabel(ctx, fixture.user.ID, principal, fixture.checkout.CheckoutID, "Studio workstation · release")
	require.NoError(t, err)
	_, err = grants.SetOwnerChoiceLabel(ctx, fixture.user.ID, principal, secondCheckout.CheckoutID, "Laptop workstation · release")
	require.NoError(t, err)

	entries, err := fixture.store.ListCatalog(ctx, fixture.user.ID)
	require.NoError(t, err)
	byCheckout := make(map[string]BrowserCodeContextCatalogEntry, len(entries))
	for _, entry := range entries {
		byCheckout[entry.CheckoutID] = entry
	}
	first := byCheckout[fixture.checkout.CheckoutID]
	second := byCheckout[secondCheckout.CheckoutID]
	require.Equal(t, branch, first.ViewLabel)
	require.Equal(t, branch, second.ViewLabel)
	require.Equal(t, "Studio workstation · release", first.CheckoutLabel)
	require.Equal(t, "Laptop workstation · release", second.CheckoutLabel)
	require.NotEqual(t, first.CheckoutLabel, second.CheckoutLabel)
	require.NotContains(t, first.CheckoutLabel, fixture.checkout.CheckoutID)
	require.NotContains(t, second.CheckoutLabel, secondCheckout.CheckoutID)
	require.NotContains(t, first.CheckoutLabel, fixture.checkout.LocatorRef)
	require.NotContains(t, second.CheckoutLabel, secondCheckout.LocatorRef)
	require.NotNil(t, first.SnapshotRevision)
	require.NotNil(t, first.SnapshotPublishedAt)
	require.NotNil(t, second.SnapshotRevision)
	require.NotNil(t, second.SnapshotPublishedAt)
}

func TestBrowserCodeContextStore_PinRequiresExactPublishedContextAndAudits(t *testing.T) {
	fixture := newBrowserCodeContextFixture(t)
	ctx := context.Background()

	wrong := fixture.reference()
	wrong.Generation++
	err := fixture.store.Pin(ctx, fixture.pin(wrong))
	require.ErrorIs(t, err, ErrBrowserCodeContextDenied)
	fixture.requireUnpinned(t)
	assertBrowserCodeContextAuditCount(t, fixture.db, "code_context_pinned", 0)

	require.NoError(t, fixture.store.Pin(ctx, fixture.pin(fixture.reference())))
	var stored BrowserTabBinding
	require.NoError(t, fixture.db.Where("tab_binding_id = ?", fixture.binding.TabBindingID).First(&stored).Error)
	require.Equal(t, fixture.reference().SourceID, *stored.PinnedSourceID)
	require.Equal(t, fixture.reference().CheckoutID, *stored.PinnedCheckoutID)
	require.Equal(t, fixture.reference().ViewID, *stored.PinnedViewID)
	require.Equal(t, fixture.reference().AnalysisProfileID, *stored.PinnedAnalysisProfileID)
	require.Equal(t, fixture.reference().Generation, *stored.PinnedGeneration)

	var audit AuditLogEntry
	require.NoError(t, fixture.db.Where("action = ?", "code_context_pinned").First(&audit).Error)
	require.Equal(t, browserReadGrantPrincipal(fixture.user.ID), audit.Actor)
	require.Contains(t, audit.Reason, "grant_ref="+fixture.grant.GrantRef)
	require.Contains(t, audit.Reason, "view_id="+fixture.view.ViewID)
}

func TestBrowserCodeContextStore_PinAuditFailureRollsBackPin(t *testing.T) {
	fixture := newBrowserCodeContextFixture(t)
	require.NoError(t, fixture.db.Migrator().DropTable(&AuditLogEntry{}))
	require.NoError(t, fixture.db.Exec(`CREATE TABLE audit_log (id BIGINT PRIMARY KEY)`).Error)

	err := fixture.store.Pin(context.Background(), fixture.pin(fixture.reference()))
	require.Error(t, err)
	fixture.requireUnpinned(t)
}

func TestBrowserCodeContextStore_InitialIntentReauthorizationAndContinuations(t *testing.T) {
	fixture := newBrowserCodeContextFixture(t)
	ctx := context.Background()
	unpublished, _ := fixture.addUnpublishedCheckout(t)
	target := fixture.indexTarget(unpublished)

	initial, err := fixture.store.AuthorizeInitialIndexIntent(ctx, target)
	require.NoError(t, err)
	require.Equal(t, uci.IndexScope{
		SourceID:      fixture.source.SourceID,
		CheckoutID:    unpublished.CheckoutID,
		IncarnationID: unpublished.IncarnationID,
	}, initial.Scope)
	require.Equal(t, fixture.profile.ProfileID, initial.ProfileID)
	require.Equal(t, fixture.source.AuthRealm, initial.AuthRealm)

	publishBrowserCodeContextView(t, fixture.db, unpublished, fixture.profile)
	_, err = fixture.store.AuthorizeInitialIndexIntent(ctx, target)
	require.ErrorIs(t, err, ErrBrowserCodeContextDenied, "initial index intent must not be admitted after a view exists")

	reauthorized, err := fixture.store.ReauthorizeIndexIntent(ctx, target)
	require.NoError(t, err)
	require.Equal(t, initial, reauthorized)

	continuation := fixture.continuationBinding()
	none, err := fixture.store.CreateContinuation(ctx, continuation, "")
	require.NoError(t, err)
	require.Empty(t, none)

	cursor, err := fixture.store.CreateContinuation(ctx, continuation, "service-page-2")
	require.NoError(t, err)
	require.NotEmpty(t, cursor)
	loaded, err := fixture.store.LoadContinuation(ctx, cursor, continuation)
	require.NoError(t, err)
	require.Equal(t, "service-page-2", loaded)

	foreign := continuation
	foreign.SubjectUserID++
	_, err = fixture.store.LoadContinuation(ctx, cursor, foreign)
	require.ErrorIs(t, err, ErrBrowserCodeContinuationDenied)
	foreign = continuation
	foreign.QueryDigest = browserCodeContextDigest("different-query")
	_, err = fixture.store.LoadContinuation(ctx, cursor, foreign)
	require.ErrorIs(t, err, ErrBrowserCodeContinuationDenied)

	successor, err := fixture.store.AdvanceContinuation(ctx, cursor, continuation, "service-page-3")
	require.NoError(t, err)
	require.NotEmpty(t, successor)
	_, err = fixture.store.LoadContinuation(ctx, cursor, continuation)
	require.ErrorIs(t, err, ErrBrowserCodeContinuationDenied, "a consumed opaque cursor cannot be replayed")
	loaded, err = fixture.store.LoadContinuation(ctx, successor, continuation)
	require.NoError(t, err)
	require.Equal(t, "service-page-3", loaded)

	terminal, err := fixture.store.AdvanceContinuation(ctx, successor, continuation, "")
	require.NoError(t, err)
	require.Empty(t, terminal)
	_, err = fixture.store.LoadContinuation(ctx, successor, continuation)
	require.ErrorIs(t, err, ErrBrowserCodeContinuationDenied)

	expiredCursor, err := fixture.store.CreateContinuation(ctx, continuation, "expired-page")
	require.NoError(t, err)

	expiredAt := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, fixture.db.Model(&BrowserCodeSearchContinuation{}).Where("cursor_ref = ?", expiredCursor).Updates(map[string]any{
		"created_at": expiredAt.Add(-time.Minute),
		"expires_at": expiredAt,
	}).Error)
	_, err = fixture.store.LoadContinuation(ctx, expiredCursor, continuation)
	require.ErrorIs(t, err, ErrBrowserCodeContinuationDenied, "expired opaque cursors fail closed")
}

func TestBrowserCodeContextStore_AdvanceContinuationMaintainsTerminalLifecycleConstraint(t *testing.T) {
	fixture := newBrowserCodeContextFixture(t)
	ctx := context.Background()
	binding := fixture.continuationBinding()
	cursor, err := fixture.store.CreateContinuation(ctx, binding, "terminal-page")
	require.NoError(t, err)

	createdAt, err := browserTabBindingDatabaseClock(ctx, fixture.db)
	require.NoError(t, err)
	createdAt = createdAt.Add(time.Minute)
	require.NoError(t, fixture.db.Model(&BrowserCodeSearchContinuation{}).Where("cursor_ref = ?", cursor).Updates(map[string]any{
		"created_at": createdAt,
		"expires_at": createdAt.Add(fixture.store.continuationDuration()),
		"updated_at": createdAt,
	}).Error)

	clockBeforeAdvance, err := browserTabBindingDatabaseClock(ctx, fixture.db)
	require.NoError(t, err)

	terminal, err := fixture.store.AdvanceContinuation(ctx, cursor, binding, "")
	require.NoError(t, err, "terminal lifecycle update must not predate created_at=%s; database clock before advance=%s", createdAt.Format(time.RFC3339Nano), clockBeforeAdvance.Format(time.RFC3339Nano))
	require.Empty(t, terminal)

	var stored BrowserCodeSearchContinuation
	require.NoError(t, fixture.db.Where("cursor_ref = ?", cursor).First(&stored).Error)
	require.NotNil(t, stored.ConsumedAt)
	require.True(t, stored.ExpiresAt.After(stored.CreatedAt))
	require.False(t, stored.ConsumedAt.Before(stored.CreatedAt))
	require.False(t, stored.UpdatedAt.Before(stored.CreatedAt))
	require.True(t, stored.ConsumedAt.Equal(stored.CreatedAt))
	require.True(t, stored.UpdatedAt.Equal(stored.CreatedAt))
}

func TestBrowserCodeContextStore_AdvanceContinuationConsumesCursorOnceConcurrently(t *testing.T) {
	fixture := newBrowserCodeContextFixture(t)
	ctx := context.Background()
	binding := fixture.continuationBinding()
	cursor, err := fixture.store.CreateContinuation(ctx, binding, "concurrent-page")
	require.NoError(t, err)

	type advanceResult struct {
		successor string
		err       error
	}
	start := make(chan struct{})
	results := make(chan advanceResult, 2)
	for range 2 {
		go func() {
			<-start
			successor, err := fixture.store.AdvanceContinuation(context.Background(), cursor, binding, "concurrent-successor")
			results <- advanceResult{successor: successor, err: err}
		}()
	}
	close(start)

	var successor string
	for range 2 {
		result := <-results
		if result.err == nil {
			require.Empty(t, successor)
			successor = result.successor
			continue
		}
		require.ErrorIs(t, result.err, ErrBrowserCodeContinuationDenied)
	}
	require.NotEmpty(t, successor)

	_, err = fixture.store.LoadContinuation(ctx, cursor, binding)
	require.ErrorIs(t, err, ErrBrowserCodeContinuationDenied)
	loaded, err := fixture.store.LoadContinuation(ctx, successor, binding)
	require.NoError(t, err)
	require.Equal(t, "concurrent-successor", loaded)
}

func TestBrowserCodeContextStoreExpiredGrantDeniesPinAndReauthorization(t *testing.T) {
	fixture := newBrowserCodeContextFixture(t)
	ctx := context.Background()
	target := fixture.indexTarget(fixture.checkout)

	bound, err := fixture.store.ReauthorizeIndexIntent(ctx, target)
	require.NoError(t, err)
	require.Equal(t, fixture.checkout.IncarnationID, bound.Scope.IncarnationID)

	expiredAt := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, fixture.db.Model(&BrowserReadGrant{}).Where("grant_ref = ?", fixture.grant.GrantRef).Updates(map[string]any{
		"issued_at":  expiredAt.Add(-time.Minute),
		"expires_at": expiredAt,
	}).Error)

	err = fixture.store.Pin(ctx, fixture.pin(fixture.reference()))
	require.ErrorIs(t, err, ErrBrowserCodeContextDenied)
	fixture.requireUnpinned(t)
	assertBrowserCodeContextAuditCount(t, fixture.db, "code_context_pinned", 0)
	_, err = fixture.store.ReauthorizeIndexIntent(ctx, target)
	require.ErrorIs(t, err, ErrBrowserCodeContextDenied)
	entries, err := fixture.store.ListCatalog(ctx, fixture.user.ID)
	require.NoError(t, err)
	require.Empty(t, entries)

	var expired BrowserReadGrant
	require.NoError(t, fixture.db.Where("grant_ref = ?", fixture.grant.GrantRef).First(&expired).Error)
	require.Equal(t, BrowserReadGrantExpired, expired.State)
}

func newBrowserCodeContextFixture(t *testing.T) browserCodeContextFixture {
	t.Helper()

	projection := openUCIProjectionMigrationFixture(t)
	require.NoError(t, workspaceCatalogMigration182().Migrate(projection.db))
	token := uuid.NewString()
	now := time.Now().UTC()
	user := &User{
		Email:        fmt.Sprintf("browser-code-context-%s@example.test", token),
		PasswordHash: "browser-code-context-fixture-password-hash",
		Role:         DashboardRoleOperator,
		CreatedAt:    now,
	}
	require.NoError(t, projection.db.Create(user).Error)
	principal := browserReadGrantPrincipal(user.ID)
	require.NoError(t, projection.db.Model(&UCICheckout{}).Where("checkout_id = ?", projection.checkout.CheckoutID).Update("owner_principal", principal).Error)

	grant, err := NewBrowserReadGrantStore(projection.db).Issue(context.Background(), BrowserReadGrantIssue{
		IssuerUserID:    user.ID,
		IssuerPrincipal: principal,
		TargetUserID:    user.ID,
		SourceID:        projection.source.SourceID,
		CheckoutID:      projection.checkout.CheckoutID,
	})
	require.NoError(t, err)
	require.NoError(t, projection.db.Where("grant_ref = ?", grant.GrantRef).First(&grant).Error)

	materials := newBrowserTabBindingMaterials("browser-code-context")
	caller := BrowserTabBindingCaller{
		SubjectUserID: user.ID,
		SessionID:     "browser-code-context-session-" + token,
	}
	binding, err := NewBrowserTabBindingStore(projection.db).Create(context.Background(), browserTabBindingTestCreate(caller, materials))
	require.NoError(t, err)

	return browserCodeContextFixture{
		db:        projection.db,
		store:     NewBrowserCodeContextStore(projection.db),
		user:      user,
		source:    projection.source,
		checkout:  projection.checkout,
		profile:   projection.profile,
		view:      projection.view,
		grant:     grant,
		caller:    caller,
		binding:   binding,
		materials: materials,
	}
}

func (fixture browserCodeContextFixture) addUnpublishedCheckout(t *testing.T) (*UCICheckout, BrowserReadGrant) {
	t.Helper()

	checkout, err := NewUCIContextStore(fixture.db).RegisterCheckout(context.Background(), RegisterCheckoutInput{
		SourceID:       fixture.source.SourceID,
		WorkstationID:  "browser-code-context-workstation-" + uuid.NewString(),
		Kind:           UCICheckoutWorkingTree,
		OwnerPrincipal: browserReadGrantPrincipal(fixture.user.ID),
		LocatorRef:     "file:///browser-code-context/" + uuid.NewString(),
	})
	require.NoError(t, err)
	grant, err := NewBrowserReadGrantStore(fixture.db).Issue(context.Background(), BrowserReadGrantIssue{
		IssuerUserID:    fixture.user.ID,
		IssuerPrincipal: browserReadGrantPrincipal(fixture.user.ID),
		TargetUserID:    fixture.user.ID,
		SourceID:        fixture.source.SourceID,
		CheckoutID:      checkout.CheckoutID,
	})
	require.NoError(t, err)
	return checkout, grant
}

func (fixture browserCodeContextFixture) reference() uci.ContextRef {
	return uci.ContextRef{
		SourceID:          fixture.source.SourceID,
		CheckoutID:        fixture.checkout.CheckoutID,
		ViewID:            fixture.view.ViewID,
		AnalysisProfileID: fixture.profile.ProfileID,
		Generation:        fixture.view.Generation,
	}
}

func (fixture browserCodeContextFixture) pin(ref uci.ContextRef) BrowserCodeContextPin {
	return BrowserCodeContextPin{
		Caller:              fixture.caller,
		TabBindingID:        fixture.binding.TabBindingID,
		DocumentProofDigest: browserTabBindingTestDigest(fixture.materials.documentProof),
		Context:             ref,
	}
}

func (fixture browserCodeContextFixture) indexTarget(checkout *UCICheckout) BrowserCodeIndexIntentTarget {
	return BrowserCodeIndexIntentTarget{
		Caller:              fixture.caller,
		TabBindingID:        fixture.binding.TabBindingID,
		DocumentProofDigest: browserTabBindingTestDigest(fixture.materials.documentProof),
		SourceID:            fixture.source.SourceID,
		CheckoutID:          checkout.CheckoutID,
		ProfileID:           fixture.profile.ProfileID,
	}
}

func (fixture browserCodeContextFixture) continuationBinding() BrowserCodeContinuationBinding {
	return BrowserCodeContinuationBinding{
		SubjectUserID: fixture.user.ID,
		AuthRealm:     fixture.source.AuthRealm,
		GrantRef:      fixture.grant.GrantRef,
		GrantIssuedAt: fixture.grant.IssuedAt,
		TabBindingID:  fixture.binding.TabBindingID,
		Context:       fixture.reference(),
		QueryDigest:   browserCodeContextDigest("query"),
		FilterDigest:  browserCodeContextDigest("filter"),
		PageSize:      25,
	}
}

func (fixture browserCodeContextFixture) requireUnpinned(t *testing.T) {
	t.Helper()

	var stored BrowserTabBinding
	require.NoError(t, fixture.db.Where("tab_binding_id = ?", fixture.binding.TabBindingID).First(&stored).Error)
	require.Nil(t, stored.PinnedSourceID)
	require.Nil(t, stored.PinnedCheckoutID)
	require.Nil(t, stored.PinnedViewID)
	require.Nil(t, stored.PinnedAnalysisProfileID)
	require.Nil(t, stored.PinnedGeneration)
}

func publishBrowserCodeContextView(t *testing.T, db *gormlib.DB, checkout *UCICheckout, profile *UCIAnalysisProfile) *UCIView {
	t.Helper()

	view := newUCIProjectionView(t, NewUCIContextStore(db), checkout, profile, 1, "browser-code-context-"+uuid.NewString())
	require.NoError(t, db.Model(&UCIView{}).Where("view_id = ?", view.ViewID).Updates(map[string]any{
		"state":        UCIViewPublished,
		"published_at": time.Now().UTC(),
	}).Error)
	require.NoError(t, db.Model(&UCICheckout{}).Where("checkout_id = ?", checkout.CheckoutID).Update("current_view_id", view.ViewID).Error)
	return view
}

func browserCodeContextDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("sha256:%x", digest)
}

func assertBrowserCodeContextAuditCount(t *testing.T, db *gormlib.DB, action string, want int64) {
	t.Helper()

	var count int64
	require.NoError(t, db.Model(&AuditLogEntry{}).Where("action = ?", action).Count(&count).Error)
	require.Equal(t, want, count)
}
