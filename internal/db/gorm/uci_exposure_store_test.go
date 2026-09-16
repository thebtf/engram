package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/internal/uci"
)

func TestUCIExposureStore(t *testing.T) {
	t.Run("append exact retry mismatch and verified completion", func(t *testing.T) {
		fixture := newUCIExposureStoreFixture(t)
		ctx := context.Background()
		recordedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
		record := fixture.exposureRecord(t, "parent", recordedAt)

		first, err := fixture.store.AppendExposure(ctx, record)
		require.NoError(t, err)
		require.True(t, uci.ValidExposureRef(first.ExposureRef))

		state, err := fixture.store.CompletionState(ctx, first.ExposureRef)
		require.NoError(t, err)
		require.Equal(t, uci.QueryCompletionUnknown, state)

		retry, err := fixture.store.AppendExposure(ctx, record)
		require.NoError(t, err)
		require.Equal(t, first, retry, "an exact retry must return its original receipt")

		mismatch := record
		mismatch.RequestRef = uciExposureStoreHash("different-request")
		bindUCIExposureStoreRecord(t, &mismatch)
		stale, err := fixture.store.AppendExposure(ctx, mismatch)
		require.ErrorIs(t, err, uci.ErrIdempotencyMismatch)
		require.Empty(t, stale.ExposureRef, "a mismatched retry must not disclose the original receipt")

		completionInput := newUCIExposureStoreCompletion(t, first.ExposureRef, "completion", recordedAt)
		completion, err := fixture.store.AppendCompletion(ctx, completionInput)
		require.NoError(t, err)
		completionRetry, err := fixture.store.AppendCompletion(ctx, completionInput)
		require.NoError(t, err)
		require.Equal(t, completion, completionRetry, "an exact callback retry must return its original evidence")

		state, err = fixture.store.CompletionState(ctx, first.ExposureRef)
		require.NoError(t, err)
		require.Equal(t, uci.QueryCompletionPartial, state)

		completionMismatch := completionInput
		completionMismatch.CallbackRef = uciExposureStoreHash("different-callback")
		bindUCIExposureStoreCompletion(t, &completionMismatch)
		staleCompletion, err := fixture.store.AppendCompletion(ctx, completionMismatch)
		require.ErrorIs(t, err, uci.ErrIdempotencyMismatch)
		require.Empty(t, staleCompletion.ExposureRef, "a mismatched callback must not disclose its parent receipt")
	})

	t.Run("concurrent exact retries retain one parent", func(t *testing.T) {
		fixture := newUCIExposureStoreFixture(t)
		ctx := context.Background()
		record := fixture.exposureRecord(t, "concurrent", time.Now().UTC().Truncate(time.Microsecond))

		const workers = 12
		start := make(chan struct{})
		results := make(chan uci.ExposureRecord, workers)
		errs := make(chan error, workers)
		var group sync.WaitGroup
		for range workers {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				result, err := fixture.store.AppendExposure(ctx, record)
				if err != nil {
					errs <- err
					return
				}
				results <- result
			}()
		}
		close(start)
		group.Wait()
		close(results)
		close(errs)

		for err := range errs {
			require.NoError(t, err)
		}
		var exposureRef string
		for result := range results {
			if exposureRef == "" {
				exposureRef = result.ExposureRef
			}
			require.Equal(t, exposureRef, result.ExposureRef)
		}
		require.True(t, uci.ValidExposureRef(exposureRef))

		var rows int64
		require.NoError(t, fixture.db.Model(&UCIExposure{}).
			Where("auth_realm = ? AND client_session_ref = ? AND idempotency_key = ?", record.AuthRealm, record.ClientSessionRef, record.IdempotencyKey).
			Count(&rows).Error)
		require.Equal(t, int64(1), rows, "concurrent idempotent appends must retain exactly one parent row")
	})

	t.Run("append-only retention and integrity are database-enforced", func(t *testing.T) {
		fixture := newUCIExposureStoreFixture(t)
		ctx := context.Background()
		recordedAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
		record := fixture.exposureRecord(t, "retained", recordedAt)
		parent, err := fixture.store.AppendExposure(ctx, record)
		require.NoError(t, err)
		completion, err := fixture.store.AppendCompletion(ctx, newUCIExposureStoreCompletion(t, parent.ExposureRef, "retained-completion", recordedAt))
		require.NoError(t, err)
		require.NoError(t, fixture.store.VerifyIntegrity(ctx))

		var parentRow UCIExposure
		require.NoError(t, fixture.db.Where("exposure_ref = ?", parent.ExposureRef).First(&parentRow).Error)
		require.Error(t, fixture.db.Model(&UCIExposure{}).Where("exposure_id = ?", parentRow.ExposureID).Update("certainty", UCICertaintyPartial).Error)
		require.Error(t, fixture.db.Where("completion_evidence_id = ?", completionEvidenceID(t, fixture.db, completion)).Delete(&UCICompletionEvidence{}).Error)

		pruned, err := fixture.store.PruneExpired(ctx, time.Now().UTC(), 1)
		require.NoError(t, err)
		require.Equal(t, int64(1), pruned.ExposuresDeleted)
		require.Equal(t, int64(1), pruned.CompletionsDeleted)
		require.NoError(t, fixture.store.VerifyIntegrity(ctx))

		corruptRecord := fixture.exposureRecord(t, "corrupt", time.Now().UTC().Truncate(time.Microsecond))
		corrupt := exposureModelFromRecord(corruptRecord)
		corrupt.IdempotencyBindingDigest = uciExposureStoreHash("not-the-canonical-binding")
		require.NoError(t, fixture.db.Create(&corrupt).Error, "the database only checks opaque digest shape")
		require.ErrorIs(t, fixture.store.VerifyIntegrity(ctx), ErrUCIEvidenceIntegrity)
	})
}

type uciExposureStoreFixture struct {
	db         *gormlib.DB
	store      *UCIExposureStore
	authRealm  string
	sourceID   string
	checkoutID string
	viewID     string
}

func newUCIExposureStoreFixture(t *testing.T) *uciExposureStoreFixture {
	t.Helper()

	db, _ := openInterventionReceiptMigrationTestDB(t)
	ctx := context.Background()
	contextStore := NewUCIContextStore(db)
	token := uuid.NewString()
	authRealm := "uci-exposure-store-" + token

	source, err := contextStore.CreateSource(ctx, CreateSourceInput{
		AuthRealm:   authRealm,
		Kind:        UCISourceGit,
		DisplayName: "source-" + token,
	})
	require.NoError(t, err)
	checkout, err := contextStore.RegisterCheckout(ctx, RegisterCheckoutInput{
		SourceID:       source.SourceID,
		WorkstationID:  "workstation-" + token,
		Kind:           UCICheckoutWorkingTree,
		OwnerPrincipal: "principal-" + token,
		LocatorRef:     "file:///uci-exposure-store/" + token,
	})
	require.NoError(t, err)
	profile, err := contextStore.CreateProfile(ctx, CreateProfileInput{
		ParserBundleDigest:   uciExposureStoreHash("parser-bundle"),
		ResolverRevision:     "resolver-" + token,
		ChunkerRevision:      "chunker-" + token,
		IgnorePolicyDigest:   uciExposureStoreHash("ignore-policy"),
		BuildContextJSON:     `{"fixture":"` + token + `"}`,
		SecretPolicyRevision: "secret-policy-" + token,
	})
	require.NoError(t, err)

	scanStart := time.Now().UTC().Truncate(time.Microsecond)
	headOID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	objectFormat := "sha1"
	refLabel := "refs/heads/exposure-" + token
	view, err := contextStore.CreateView(ctx, CreateViewInput{
		CheckoutID:     checkout.CheckoutID,
		SourceID:       checkout.SourceID,
		IncarnationID:  checkout.IncarnationID,
		Generation:     1,
		ProfileID:      profile.ProfileID,
		HeadOID:        &headOID,
		ObjectFormat:   &objectFormat,
		RefLabel:       &refLabel,
		ObservedFSSeq:  1,
		ScanStart:      scanStart,
		ScanEnd:        scanStart.Add(time.Second),
		ManifestDigest: uciExposureStoreHash("manifest"),
		CoverageJSON:   `{"fixture":"` + token + `"}`,
		State:          UCIViewStaging,
	})
	require.NoError(t, err)

	return &uciExposureStoreFixture{
		db:         db,
		store:      NewUCIExposureStore(db),
		authRealm:  authRealm,
		sourceID:   source.SourceID,
		checkoutID: checkout.CheckoutID,
		viewID:     view.ViewID,
	}
}

func (fixture *uciExposureStoreFixture) exposureRecord(t *testing.T, suffix string, recordedAt time.Time) uci.ExposureRecord {
	t.Helper()
	record := uci.ExposureRecord{
		AuthRealm:        fixture.authRealm,
		SourceID:         fixture.sourceID,
		CheckoutID:       fixture.checkoutID,
		ViewID:           fixture.viewID,
		ClientRef:        uciExposureStoreHash("client-" + suffix),
		ClientSessionRef: uciExposureStoreHash("session-" + suffix),
		RequestRef:       uciExposureStoreHash("request-" + suffix),
		Operation:        uci.ExposureOperationCodeSearch,
		Result:           uci.ExposureResultOK,
		Retrieval:        uci.ExposureRetrievalLexical,
		Coverage:         uci.ExposureCoverageComplete,
		Evidence:         uci.ExposureEvidenceFTS,
		Certainty:        uci.ExposureCertaintyEstablished,
		IdempotencyKey:   uciExposureStoreHash("idempotency-" + suffix),
		RecordedAt:       recordedAt,
	}
	bindUCIExposureStoreRecord(t, &record)
	return record
}

func newUCIExposureStoreCompletion(t *testing.T, exposureRef, suffix string, occurredAt time.Time) uci.CompletionEvidence {
	t.Helper()
	evidence := uci.CompletionEvidence{
		ExposureRef:      exposureRef,
		SupportedHostRef: uciExposureStoreHash("host-" + suffix),
		CallbackRef:      uciExposureStoreHash("callback-" + suffix),
		Outcome:          uci.CompletionPartial,
		IdempotencyKey:   uciExposureStoreHash("completion-idempotency-" + suffix),
		OccurredAt:       occurredAt,
	}
	bindUCIExposureStoreCompletion(t, &evidence)
	return evidence
}

func bindUCIExposureStoreRecord(t *testing.T, record *uci.ExposureRecord) {
	t.Helper()
	digest, err := uci.CanonicalExposureBindingDigest(*record)
	require.NoError(t, err)
	record.BindingDigest = digest
}

func bindUCIExposureStoreCompletion(t *testing.T, evidence *uci.CompletionEvidence) {
	t.Helper()
	digest, err := uci.CanonicalCompletionBindingDigest(*evidence)
	require.NoError(t, err)
	evidence.BindingDigest = digest
}

func completionEvidenceID(t *testing.T, db *gormlib.DB, evidence uci.CompletionEvidence) string {
	t.Helper()
	var row UCICompletionEvidence
	require.NoError(t, db.Where("supported_host_ref = ? AND idempotency_key = ?", evidence.SupportedHostRef, evidence.IdempotencyKey).First(&row).Error)
	return row.CompletionEvidenceID
}

func uciExposureStoreHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}
