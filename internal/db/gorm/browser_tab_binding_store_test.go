package gorm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type browserTabBindingFixture struct {
	db     *gormlib.DB
	store  *BrowserTabBindingStore
	caller BrowserTabBindingCaller
}

type browserTabBindingMaterials struct {
	documentProof string
	resumeNonce   string
	reloadToken   string
}

func TestBrowserTabBindingStore_CopyCollisionRotatesOnlyNewDocument(t *testing.T) {
	fixture := newBrowserTabBindingFixture(t)
	ctx := context.Background()
	originalMaterials := newBrowserTabBindingMaterials("original")
	original := createBrowserTabBinding(t, fixture, originalMaterials)
	pinned := browserTabBindingTestContext()
	require.NoError(t, fixture.store.Pin(ctx, browserTabBindingTestPin(fixture.caller, original, originalMaterials.documentProof, pinned)))

	var before BrowserTabBinding
	require.NoError(t, fixture.db.Where("tab_binding_id = ?", original.TabBindingID).First(&before).Error)
	copyMaterials := newBrowserTabBindingMaterials("copy")
	copyBinding, collision, err := fixture.store.CreateFromCopy(ctx, BrowserTabBindingCopyCreate{
		Create:                  browserTabBindingTestCreate(fixture.caller, copyMaterials),
		CopiedTabBindingID:      original.TabBindingID,
		CopiedResumeNonceDigest: browserTabBindingTestDigest(originalMaterials.resumeNonce),
	})
	require.NoError(t, err)
	require.True(t, collision)
	require.NotEqual(t, original.TabBindingID, copyBinding.TabBindingID)

	var after BrowserTabBinding
	require.NoError(t, fixture.db.Where("tab_binding_id = ?", original.TabBindingID).First(&after).Error)
	require.Equal(t, before.ResumeNonceDigest, after.ResumeNonceDigest)
	require.Equal(t, before.DocumentProofDigest, after.DocumentProofDigest)
	require.Equal(t, before.ReloadTokenDigest, after.ReloadTokenDigest)
	require.Equal(t, before.PinnedSourceID, after.PinnedSourceID)
	require.Equal(t, before.PinnedCheckoutID, after.PinnedCheckoutID)
	require.Equal(t, before.PinnedViewID, after.PinnedViewID)

	originalGuard, err := fixture.store.Guard(ctx, browserTabBindingTestGuard(fixture.caller, original.TabBindingID, originalMaterials.documentProof))
	require.NoError(t, err)
	require.Equal(t, pinned.SourceID, *originalGuard.PinnedSourceID)
	copyGuard, err := fixture.store.Guard(ctx, browserTabBindingTestGuard(fixture.caller, copyBinding.TabBindingID, copyMaterials.documentProof))
	require.NoError(t, err)
	require.Nil(t, copyGuard.PinnedSourceID)
	require.Nil(t, copyGuard.PinnedCheckoutID)

	require.ErrorIs(t, fixture.store.Close(ctx, browserTabBindingTestLease(fixture.caller, original.TabBindingID, copyMaterials.documentProof)), ErrBrowserTabBindingDenied)
	_, err = fixture.store.Guard(ctx, browserTabBindingTestGuard(fixture.caller, original.TabBindingID, originalMaterials.documentProof))
	require.NoError(t, err, "a copied document cannot close the original document lease")
}

func TestBrowserTabBindingStore_CloseResumeRetainsPinAndReplayedProofOrTokenFails(t *testing.T) {
	fixture := newBrowserTabBindingFixture(t)
	ctx := context.Background()
	originalMaterials := newBrowserTabBindingMaterials("reload-original")
	binding := createBrowserTabBinding(t, fixture, originalMaterials)
	pinned := browserTabBindingTestContext()
	require.NoError(t, fixture.store.Pin(ctx, browserTabBindingTestPin(fixture.caller, binding, originalMaterials.documentProof, pinned)))
	require.NoError(t, fixture.store.Close(ctx, browserTabBindingTestLease(fixture.caller, binding.TabBindingID, originalMaterials.documentProof)))

	resumedMaterials := newBrowserTabBindingMaterials("reload-resumed")
	resumed, state, err := fixture.store.Resume(ctx, browserTabBindingTestResume(fixture.caller, binding.TabBindingID, originalMaterials.resumeNonce, originalMaterials.reloadToken, resumedMaterials))
	require.NoError(t, err)
	require.Equal(t, BrowserTabBindingResumed, state)
	require.Equal(t, binding.TabBindingID, resumed.TabBindingID)
	require.Equal(t, BrowserTabBindingLeaseLive, resumed.DocumentLeaseState)
	require.Equal(t, pinned.SourceID, *resumed.PinnedSourceID)
	require.Equal(t, pinned.CheckoutID, *resumed.PinnedCheckoutID)
	require.Equal(t, pinned.ViewID, *resumed.PinnedViewID)

	_, err = fixture.store.Guard(ctx, browserTabBindingTestGuard(fixture.caller, binding.TabBindingID, originalMaterials.documentProof))
	require.ErrorIs(t, err, ErrBrowserTabBindingDenied, "the retired proof cannot authenticate after normal reload")
	_, err = fixture.store.Guard(ctx, browserTabBindingTestGuard(fixture.caller, binding.TabBindingID, resumedMaterials.documentProof))
	require.NoError(t, err)

	_, _, err = fixture.store.Resume(ctx, browserTabBindingTestResume(fixture.caller, binding.TabBindingID, originalMaterials.resumeNonce, originalMaterials.reloadToken, newBrowserTabBindingMaterials("replayed-token")))
	require.ErrorIs(t, err, ErrBrowserTabBindingDenied, "a consumed reload token cannot be replayed")
	_, err = fixture.store.Guard(ctx, browserTabBindingTestGuard(fixture.caller, binding.TabBindingID, resumedMaterials.documentProof))
	require.NoError(t, err, "failed replay cannot rotate or invalidate the current proof")
}

func TestBrowserTabBindingStore_PendingThenLeaseExpiryResumesWithoutLosingPin(t *testing.T) {
	fixture := newBrowserTabBindingFixture(t)
	ctx := context.Background()
	originalMaterials := newBrowserTabBindingMaterials("pending-original")
	binding := createBrowserTabBinding(t, fixture, originalMaterials)
	pinned := browserTabBindingTestContext()
	require.NoError(t, fixture.store.Pin(ctx, browserTabBindingTestPin(fixture.caller, binding, originalMaterials.documentProof, pinned)))

	pendingMaterials := newBrowserTabBindingMaterials("pending-attempt")
	pending, state, err := fixture.store.Resume(ctx, browserTabBindingTestResume(fixture.caller, binding.TabBindingID, originalMaterials.resumeNonce, originalMaterials.reloadToken, pendingMaterials))
	require.NoError(t, err)
	require.Equal(t, BrowserTabBindingReloadPending, state)
	require.Equal(t, binding.TabBindingID, pending.TabBindingID)
	_, err = fixture.store.Guard(ctx, browserTabBindingTestGuard(fixture.caller, binding.TabBindingID, originalMaterials.documentProof))
	require.NoError(t, err, "pending must retain the original live document")

	require.NoError(t, fixture.db.Model(&BrowserTabBinding{}).Where("tab_binding_id = ?", binding.TabBindingID).Update("document_lease_expires_at", time.Now().UTC().Add(-time.Second)).Error)
	_, err = fixture.store.Guard(ctx, browserTabBindingTestGuard(fixture.caller, binding.TabBindingID, originalMaterials.documentProof))
	require.ErrorIs(t, err, ErrBrowserTabBindingDenied, "lease expiry rejects the stale document proof")

	var expired BrowserTabBinding
	require.NoError(t, fixture.db.Where("tab_binding_id = ?", binding.TabBindingID).First(&expired).Error)
	require.Equal(t, BrowserTabBindingLeaseExpired, expired.DocumentLeaseState)
	require.Equal(t, pinned.SourceID, *expired.PinnedSourceID, "lease expiry retains the pinned context")

	resumedMaterials := newBrowserTabBindingMaterials("expired-resume")
	resumed, state, err := fixture.store.Resume(ctx, browserTabBindingTestResume(fixture.caller, binding.TabBindingID, originalMaterials.resumeNonce, originalMaterials.reloadToken, resumedMaterials))
	require.NoError(t, err)
	require.Equal(t, BrowserTabBindingResumed, state)
	require.Equal(t, pinned.ViewID, *resumed.PinnedViewID)
	_, err = fixture.store.Guard(ctx, browserTabBindingTestGuard(fixture.caller, binding.TabBindingID, resumedMaterials.documentProof))
	require.NoError(t, err)
}

func TestBrowserTabBindingStore_InvalidCopyAndBindingOrSessionExpiryClearState(t *testing.T) {
	fixture := newBrowserTabBindingFixture(t)
	ctx := context.Background()
	originalMaterials := newBrowserTabBindingMaterials("invalid-copy-original")
	original := createBrowserTabBinding(t, fixture, originalMaterials)

	newBinding, collision, err := fixture.store.CreateFromCopy(ctx, BrowserTabBindingCopyCreate{
		Create:                  browserTabBindingTestCreate(fixture.caller, newBrowserTabBindingMaterials("invalid-copy-new")),
		CopiedTabBindingID:      original.TabBindingID,
		CopiedResumeNonceDigest: browserTabBindingTestDigest("wrong-resume-nonce"),
	})
	require.NoError(t, err)
	require.False(t, collision, "an invalid pair cannot classify a document as a collision")
	require.NotEqual(t, original.TabBindingID, newBinding.TabBindingID)

	require.NoError(t, fixture.db.Model(&BrowserTabBinding{}).Where("tab_binding_id = ?", original.TabBindingID).Update("binding_expires_at", time.Now().UTC().Add(-time.Second)).Error)
	expiredCopy, collision, err := fixture.store.CreateFromCopy(ctx, BrowserTabBindingCopyCreate{
		Create:                  browserTabBindingTestCreate(fixture.caller, newBrowserTabBindingMaterials("expired-copy-new")),
		CopiedTabBindingID:      original.TabBindingID,
		CopiedResumeNonceDigest: browserTabBindingTestDigest(originalMaterials.resumeNonce),
	})
	require.NoError(t, err)
	require.False(t, collision, "an expired binding is an invalid pair, not a collision")
	require.NotEqual(t, original.TabBindingID, expiredCopy.TabBindingID)
	_, err = fixture.store.Guard(ctx, browserTabBindingTestGuard(fixture.caller, original.TabBindingID, originalMaterials.documentProof))
	require.ErrorIs(t, err, ErrBrowserTabBindingDenied)
	var originalCount int64
	require.NoError(t, fixture.db.Model(&BrowserTabBinding{}).Where("tab_binding_id = ?", original.TabBindingID).Count(&originalCount).Error)
	require.Zero(t, originalCount, "binding expiry deletes its pin, proof digest, and resume digests")

	require.NoError(t, fixture.store.DestroySession(ctx, fixture.caller.SessionID))
	var sessionCount int64
	require.NoError(t, fixture.db.Model(&BrowserTabBinding{}).Where("session_id = ?", fixture.caller.SessionID).Count(&sessionCount).Error)
	require.Zero(t, sessionCount, "browser session end clears every remaining binding")
}

func TestBrowserTabBindingStore_PersistsOnlyOpaqueMaterialDigests(t *testing.T) {
	fixture := newBrowserTabBindingFixture(t)
	materials := newBrowserTabBindingMaterials("digest-only")
	binding := createBrowserTabBinding(t, fixture, materials)

	var persisted BrowserTabBinding
	require.NoError(t, fixture.db.Where("tab_binding_id = ?", binding.TabBindingID).First(&persisted).Error)
	require.Len(t, persisted.DocumentProofDigest, sha256.Size)
	require.Len(t, persisted.ResumeNonceDigest, sha256.Size)
	require.Len(t, persisted.ReloadTokenDigest, sha256.Size)
	require.NotContains(t, fmt.Sprintf("%+v", persisted), materials.documentProof)
	require.NotContains(t, fmt.Sprintf("%+v", persisted), materials.resumeNonce)
	require.NotContains(t, fmt.Sprintf("%+v", persisted), materials.reloadToken)

	var columns []string
	require.NoError(t, fixture.db.Raw(`SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'browser_tab_bindings'`).Scan(&columns).Error)
	for _, forbidden := range []string{"document_nonce", "document_proof", "resume_nonce", "reload_token"} {
		require.NotContains(t, columns, forbidden)
	}
	for _, required := range []string{"document_proof_digest", "resume_nonce_digest", "reload_token_digest"} {
		require.Contains(t, columns, required)
	}
}

func newBrowserTabBindingFixture(t *testing.T) browserTabBindingFixture {
	t.Helper()
	db := openBrowserTabBindingTestDB(t)
	return browserTabBindingFixture{
		db:    db,
		store: NewBrowserTabBindingStore(db),
		caller: BrowserTabBindingCaller{
			SubjectUserID: 41,
			SessionID:     "browser-session-" + uuid.NewString(),
		},
	}
}

func createBrowserTabBinding(t *testing.T, fixture browserTabBindingFixture, materials browserTabBindingMaterials) BrowserTabBinding {
	t.Helper()
	binding, err := fixture.store.Create(context.Background(), browserTabBindingTestCreate(fixture.caller, materials))
	require.NoError(t, err)
	return binding
}

func browserTabBindingTestCreate(caller BrowserTabBindingCaller, materials browserTabBindingMaterials) BrowserTabBindingCreate {
	now := time.Now().UTC()
	return BrowserTabBindingCreate{
		Caller:                 caller,
		DocumentProofDigest:    browserTabBindingTestDigest(materials.documentProof),
		ResumeNonceDigest:      browserTabBindingTestDigest(materials.resumeNonce),
		ReloadTokenDigest:      browserTabBindingTestDigest(materials.reloadToken),
		DocumentLeaseExpiresAt: now.Add(time.Minute),
		BindingExpiresAt:       now.Add(time.Hour),
	}
}

func browserTabBindingTestResume(caller BrowserTabBindingCaller, bindingID, resumeNonce, reloadToken string, next browserTabBindingMaterials) BrowserTabBindingResume {
	return BrowserTabBindingResume{
		Caller:                 caller,
		TabBindingID:           bindingID,
		ResumeNonceDigest:      browserTabBindingTestDigest(resumeNonce),
		ReloadTokenDigest:      browserTabBindingTestDigest(reloadToken),
		NewDocumentProofDigest: browserTabBindingTestDigest(next.documentProof),
		NewReloadTokenDigest:   browserTabBindingTestDigest(next.reloadToken),
		DocumentLeaseExpiresAt: time.Now().UTC().Add(time.Minute),
	}
}

func browserTabBindingTestGuard(caller BrowserTabBindingCaller, bindingID, documentProof string) BrowserTabBindingGuard {
	return BrowserTabBindingGuard{
		Caller:              caller,
		TabBindingID:        bindingID,
		DocumentProofDigest: browserTabBindingTestDigest(documentProof),
	}
}

func browserTabBindingTestLease(caller BrowserTabBindingCaller, bindingID, documentProof string) BrowserTabBindingLease {
	return BrowserTabBindingLease{BrowserTabBindingGuard: browserTabBindingTestGuard(caller, bindingID, documentProof)}
}

func browserTabBindingTestPin(caller BrowserTabBindingCaller, binding BrowserTabBinding, documentProof string, pinned BrowserTabBindingContext) BrowserTabBindingPin {
	return BrowserTabBindingPin{
		BrowserTabBindingGuard: browserTabBindingTestGuard(caller, binding.TabBindingID, documentProof),
		Context:                pinned,
	}
}

func browserTabBindingTestContext() BrowserTabBindingContext {
	return BrowserTabBindingContext{
		SourceID:          uuid.NewString(),
		CheckoutID:        uuid.NewString(),
		ViewID:            uuid.NewString(),
		AnalysisProfileID: uuid.NewString(),
		Generation:        1,
	}
}

func newBrowserTabBindingMaterials(label string) browserTabBindingMaterials {
	seed := label + "-" + uuid.NewString()
	return browserTabBindingMaterials{
		documentProof: "document-proof-" + seed,
		resumeNonce:   "resume-nonce-" + seed,
		reloadToken:   "reload-token-" + seed,
	}
}

func browserTabBindingTestDigest(value string) []byte {
	digest := sha256.Sum256([]byte(value))
	return digest[:]
}

func openBrowserTabBindingTestDB(t *testing.T) *gormlib.DB {
	t.Helper()
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set, skipping browser tab binding integration test")
	}
	db, err := gormlib.Open(postgres.Open(dsn), &gormlib.Config{Logger: logger.Default.LogMode(logger.Warn)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, sqlDB.Ping())

	schema := "t014_browser_tab_binding_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, db.Exec("CREATE SCHEMA "+schema).Error)
	require.NoError(t, db.Exec("SET search_path TO "+schema).Error)
	require.NoError(t, db.AutoMigrate(&BrowserTabBinding{}))
	t.Cleanup(func() {
		_ = db.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		_ = sqlDB.Close()
	})
	return db
}
