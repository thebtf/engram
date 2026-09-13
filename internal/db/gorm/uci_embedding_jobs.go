package gorm

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	ucidomain "github.com/thebtf/engram/internal/uci"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ ucidomain.EmbeddingJobStore = (*UCIProjectionStore)(nil)

const (
	uciEmbeddingJobLeaseWhere = "job_id = ? AND state = ? AND owner_epoch = ? AND lease_owner = ? AND lease_expiry > ?"
	uciEmbeddingJobIDWhere    = "job_id = ?"
)

type uciEmbeddingProgress struct {
	Version         int                              `json:"version"`
	Cursor          *ucidomain.EmbeddingCandidateKey `json:"cursor,omitempty"`
	Total           uint64                           `json:"total"`
	Scanned         uint64                           `json:"scanned"`
	Ready           uint64                           `json:"ready"`
	LastBatchDigest string                           `json:"last_batch_digest,omitempty"`
	Exhausted       bool                             `json:"exhausted"`
}

type uciEmbeddingJobScope struct {
	Job      UCIJob
	Source   UCISource
	Checkout UCICheckout
	View     UCIView
	Profile  UCIEmbeddingProfile
}

type uciEmbeddingCandidateRow struct {
	MembershipID       string `gorm:"column:membership_id"`
	ChunkID            string `gorm:"column:chunk_id"`
	ArtifactID         string `gorm:"column:artifact_id"`
	ChunkContentDigest string `gorm:"column:chunk_content_digest"`
	FactsDigest        string `gorm:"column:facts_digest"`
	DefinitionCount    int64  `gorm:"column:definition_count"`
	ReferenceSiteCount int64  `gorm:"column:reference_site_count"`
	ChunkCount         int64  `gorm:"column:chunk_count"`
	EntityKey          string `gorm:"column:entity_key"`
	LocalName          string `gorm:"column:local_name"`
	QualifiedSymbol    string `gorm:"column:qualified_symbol"`
	RelativePath       string `gorm:"column:relative_path"`
	ByteStart          int64  `gorm:"column:byte_start"`
	ByteEnd            int64  `gorm:"column:byte_end"`
	LineStart          int    `gorm:"column:line_start"`
	LineEnd            int    `gorm:"column:line_end"`
	Text               string `gorm:"column:text"`
	ChunkKind          string `gorm:"column:chunk_kind"`
	Language           string `gorm:"column:language"`
	ProtectionDomain   string `gorm:"column:protection_domain"`
}

func (row uciEmbeddingCandidateRow) candidate(ref ucidomain.ContextRef, profile ucidomain.VectorProfile) (ucidomain.EmbeddingCandidate, bool) {
	candidate, ok := (uciQueryCandidateRow{
		ArtifactID:         row.ArtifactID,
		ChunkContentDigest: row.ChunkContentDigest,
		FactsDigest:        row.FactsDigest,
		DefinitionCount:    row.DefinitionCount,
		ReferenceSiteCount: row.ReferenceSiteCount,
		ChunkCount:         row.ChunkCount,
		EntityKey:          row.EntityKey,
		LocalName:          row.LocalName,
		QualifiedSymbol:    row.QualifiedSymbol,
		RelativePath:       row.RelativePath,
		ByteStart:          row.ByteStart,
		ByteEnd:            row.ByteEnd,
		LineStart:          row.LineStart,
		LineEnd:            row.LineEnd,
		Text:               row.Text,
		ChunkKind:          row.ChunkKind,
		Language:           row.Language,
	}).queryCandidate(ref)
	if !ok || validateUCIUUID("membership_id", row.MembershipID) != nil || validateUCIUUID("chunk_id", row.ChunkID) != nil || validateUCIRequiredText("protection_domain", row.ProtectionDomain) != nil {
		return ucidomain.EmbeddingCandidate{}, false
	}
	input, digest, err := ucidomain.SemanticEmbeddingInput(profile, candidate)
	if err != nil {
		return ucidomain.EmbeddingCandidate{}, false
	}
	return ucidomain.EmbeddingCandidate{
		Key:              ucidomain.EmbeddingCandidateKey{MembershipID: row.MembershipID, ChunkID: row.ChunkID},
		Candidate:        candidate,
		ProtectionDomain: row.ProtectionDomain,
		Input:            input,
		InputDigest:      digest,
	}, true
}

func (s *UCIProjectionStore) EnsureCurrentEmbeddingJobs(ctx context.Context, profile ucidomain.VectorProfile, afterCheckoutID string, limit int) (string, bool, error) {
	if err := s.requireDB("ensure current embedding jobs"); err != nil {
		return "", false, err
	}
	if ctx == nil || ctx.Err() != nil || validateUCISemanticProfile(profile) != nil || limit < 1 {
		return "", false, fmt.Errorf("uci embedding adoption: invalid request")
	}
	if afterCheckoutID != "" && validateUCIUUID("after_checkout_id", afterCheckoutID) != nil {
		return "", false, fmt.Errorf("uci embedding adoption: invalid cursor")
	}
	cursor := afterCheckoutID
	if cursor == "" {
		cursor = uuid.Nil.String()
	}

	type row struct {
		CheckoutID     string `gorm:"column:checkout_id"`
		SourceID       string `gorm:"column:source_id"`
		AuthRealm      string `gorm:"column:auth_realm"`
		OwnerPrincipal string `gorm:"column:owner_principal"`
		ViewID         string `gorm:"column:view_id"`
	}
	var rows []row
	result := s.db.WithContext(ctx).Raw(`
		SELECT
			checkout.checkout_id,
			checkout.source_id,
			source.auth_realm,
			checkout.owner_principal,
			view_row.view_id
		FROM ci_checkouts AS checkout
		JOIN sources AS source ON source.source_id = checkout.source_id
		JOIN ci_views AS view_row
			ON view_row.view_id = checkout.current_view_id
			AND view_row.checkout_id = checkout.checkout_id
			AND view_row.source_id = checkout.source_id
			AND view_row.incarnation_id = checkout.incarnation_id
		WHERE checkout.checkout_id > ?
			AND source.state = ?
			AND checkout.state IN (?, ?, ?)
			AND view_row.state = ?
		ORDER BY checkout.checkout_id ASC
		LIMIT ?`, cursor, UCISourceActive, UCICheckoutRegistered, UCICheckoutWatching, UCICheckoutCatchingUp, UCIViewPublished, limit).Scan(&rows)
	if result.Error != nil {
		return "", false, fmt.Errorf("uci embedding adoption select: %w", result.Error)
	}
	if len(rows) == 0 {
		return afterCheckoutID, true, nil
	}
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, candidate := range rows {
			if err := ensureUCIEmbeddingJobForCurrentView(ctx, tx, uciEmbeddingCurrentViewCandidate{
				sourceID:       candidate.SourceID,
				checkoutID:     candidate.CheckoutID,
				viewID:         candidate.ViewID,
				authRealm:      candidate.AuthRealm,
				ownerPrincipal: candidate.OwnerPrincipal,
				profile:        profile,
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return "", false, err
	}
	return rows[len(rows)-1].CheckoutID, len(rows) < limit, nil
}

type uciEmbeddingCurrentViewCandidate struct {
	sourceID       string
	checkoutID     string
	viewID         string
	authRealm      string
	ownerPrincipal string
	profile        ucidomain.VectorProfile
}

func ensureUCIEmbeddingJobForCurrentView(ctx context.Context, tx *gorm.DB, candidate uciEmbeddingCurrentViewCandidate) error {
	sourceID, checkoutID, viewID := candidate.sourceID, candidate.checkoutID, candidate.viewID
	authRealm, ownerPrincipal, profile := candidate.authRealm, candidate.ownerPrincipal, candidate.profile
	var checkout UCICheckout
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("checkout_id = ? AND source_id = ?", checkoutID, sourceID).First(&checkout).Error; err != nil {
		return fmt.Errorf("uci embedding adoption lock checkout: %w", err)
	}
	if checkout.CurrentViewID == nil || *checkout.CurrentViewID != viewID || checkout.OwnerPrincipal != ownerPrincipal {
		return nil
	}
	var source UCISource
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_id = ?", sourceID).First(&source).Error; err != nil {
		return fmt.Errorf("uci embedding adoption lock source: %w", err)
	}
	if source.State != UCISourceActive || source.AuthRealm != authRealm {
		return nil
	}
	var view UCIView
	if err := tx.WithContext(ctx).Where("view_id = ? AND checkout_id = ? AND source_id = ? AND incarnation_id = ? AND state = ?", viewID, checkoutID, sourceID, checkout.IncarnationID, UCIViewPublished).First(&view).Error; err != nil {
		return nil
	}
	var publication UCIJob
	result := tx.WithContext(ctx).Where("result_view_id = ? AND source_id = ? AND checkout_id = ? AND state = ? AND requested_by IS NOT NULL AND sealed_manifest IS NOT NULL", view.ViewID, sourceID, checkoutID, UCIJobSucceeded).Order("created_at ASC, job_id ASC").Limit(1).Find(&publication)
	if result.Error != nil {
		return fmt.Errorf("uci embedding adoption publication proof: %w", result.Error)
	}
	if result.RowsAffected != 1 || publication.RequestedBy == nil || *publication.RequestedBy != ownerPrincipal {
		return nil
	}
	return enqueueUCIEmbeddingJob(ctx, tx, source.AuthRealm, *publication.RequestedBy, view, profile, nil)
}

func (s *UCIProjectionStore) ClaimEmbeddingJob(ctx context.Context, profile ucidomain.VectorProfile, owner string, leaseTTL time.Duration) (ucidomain.EmbeddingJobClaim, bool, error) {
	if err := s.requireDB("claim embedding job"); err != nil {
		return ucidomain.EmbeddingJobClaim{}, false, err
	}
	if ctx == nil || ctx.Err() != nil || validateUCISemanticProfile(profile) != nil || !validUCIEmbeddingOwner(owner) || leaseTTL <= 0 {
		return ucidomain.EmbeddingJobClaim{}, false, fmt.Errorf("uci embedding claim: invalid request")
	}
	var claim ucidomain.EmbeddingJobClaim
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		var source UCISource
		result := tx.WithContext(ctx).Raw(`
			SELECT source.*
			FROM sources AS source
			WHERE source.state = ?
				AND EXISTS (
					SELECT 1
					FROM ci_jobs AS job
					JOIN ci_embedding_profiles AS profile_row ON profile_row.embedding_profile_id = job.embedding_profile_id
					WHERE job.source_id = source.source_id
						AND job.job_kind = 'embed'
						AND profile_row.provider_ref = ?
						AND profile_row.model = ?
						AND profile_row.dimension = ?
						AND profile_row.preprocessing_revision = ?
						AND profile_row.include_relative_path = ?
						AND (
							job.state = 'queued'
							OR (job.state = 'retry_scheduled' AND (job.retry_after IS NULL OR job.retry_after <= ?))
							OR (job.state = 'running' AND job.lease_expiry IS NOT NULL AND job.lease_expiry <= ?)
						)
				)
			ORDER BY source.source_id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1`, UCISourceActive, profile.ProviderRef, profile.Model, profile.Dimension, profile.PreprocessingRevision, profile.IncludeRelativePath, now, now).Scan(&source)
		if result.Error != nil {
			return fmt.Errorf("uci embedding claim select source: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
		var job UCIJob
		result = tx.WithContext(ctx).Raw(`
			SELECT job.*
			FROM ci_jobs AS job
			JOIN ci_embedding_profiles AS profile_row ON profile_row.embedding_profile_id = job.embedding_profile_id
			WHERE job.source_id = ?
				AND job.job_kind = 'embed'
				AND profile_row.provider_ref = ?
				AND profile_row.model = ?
				AND profile_row.dimension = ?
				AND profile_row.preprocessing_revision = ?
				AND profile_row.include_relative_path = ?
				AND (
					job.state = 'queued'
					OR (job.state = 'retry_scheduled' AND (job.retry_after IS NULL OR job.retry_after <= ?))
					OR (job.state = 'running' AND job.lease_expiry IS NOT NULL AND job.lease_expiry <= ?)
				)
			ORDER BY job.created_at ASC, job.job_id ASC
			FOR UPDATE SKIP LOCKED
			LIMIT 1`, source.SourceID, profile.ProviderRef, profile.Model, profile.Dimension, profile.PreprocessingRevision, profile.IncludeRelativePath, now, now).Scan(&job)
		if result.Error != nil {
			return fmt.Errorf("uci embedding claim select job: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
		facts, disposition, err := loadUCIEmbeddingClaimFacts(ctx, tx, job, source, profile)
		if err != nil {
			return err
		}
		if disposition != "" {
			if err := setUCIEmbeddingUnclaimedState(ctx, tx, job.JobID, disposition, now); err != nil {
				return err
			}
			return nil
		}
		nextEpoch := *job.OwnerEpoch + 1
		leaseExpiry := now.Add(leaseTTL)
		result = tx.WithContext(ctx).Model(&UCIJob{}).Where("job_id = ? AND state = ? AND owner_epoch = ?", job.JobID, job.State, *job.OwnerEpoch).Updates(map[string]any{
			"state":        UCIJobRunning,
			"attempt":      job.Attempt + 1,
			"owner_epoch":  nextEpoch,
			"lease_owner":  owner,
			"lease_expiry": leaseExpiry,
			"retry_after":  nil,
			"error_code":   nil,
			"updated_at":   now,
		})
		if result.Error != nil {
			return fmt.Errorf("uci embedding claim update: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return nil
		}
		ref := embeddingJobRefFromRows(job, facts.View, owner, nextEpoch)
		claim = ucidomain.EmbeddingJobClaim{
			Ref: ref,
			Access: ucidomain.ContextAccess{
				AuthRealm:  facts.Source.AuthRealm,
				Principal:  *job.RequestedBy,
				SourceID:   job.SourceID,
				CheckoutID: *job.CheckoutID,
			},
			Profile:        profile,
			Attempt:        job.Attempt + 1,
			LeaseExpiresAt: leaseExpiry,
		}
		if !claim.Ref.Context.ValidForEmbeddingJob() {
			return fmt.Errorf("uci embedding claim: invalid claimed context")
		}
		claimed = true
		return nil
	})
	if err != nil {
		return ucidomain.EmbeddingJobClaim{}, false, err
	}
	return claim, claimed, nil
}

func (s *UCIProjectionStore) RenewEmbeddingJob(ctx context.Context, ref ucidomain.EmbeddingJobRef, leaseTTL time.Duration) error {
	if err := s.requireDB("renew embedding job"); err != nil {
		return err
	}
	if ctx == nil || ctx.Err() != nil || !ref.ValidForEmbeddingJob() || leaseTTL <= 0 {
		return fmt.Errorf("uci embedding renew: invalid request")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, now, err := lockUCIEmbeddingJobScope(ctx, tx, ref)
		if err != nil {
			return err
		}
		return renewUCIEmbeddingLease(ctx, tx, scope.Job, ref, now.Add(leaseTTL), now)
	})
}

func renewUCIEmbeddingLease(ctx context.Context, tx *gorm.DB, job UCIJob, ref ucidomain.EmbeddingJobRef, leaseExpiry, now time.Time) error {
	result := tx.WithContext(ctx).Model(&UCIJob{}).Where(uciEmbeddingJobLeaseWhere, job.JobID, UCIJobRunning, ref.LeaseEpoch, ref.LeaseOwner, now).Updates(map[string]any{
		"lease_expiry": leaseExpiry,
		"updated_at":   now,
	})
	if result.Error != nil {
		return fmt.Errorf("uci embedding renew update: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ucidomain.ErrEmbeddingJobLeaseLost
	}
	return nil
}

func (s *UCIProjectionStore) PrepareEmbeddingBatch(ctx context.Context, claim ucidomain.EmbeddingJobClaim, authorized ucidomain.AuthorizedContext, limit int) (ucidomain.EmbeddingBatch, error) {
	if err := s.requireDB("prepare embedding batch"); err != nil {
		return ucidomain.EmbeddingBatch{}, err
	}
	if ctx == nil || ctx.Err() != nil || !claim.ValidForEmbeddingJob() || !embeddingAuthorizedContextMatches(claim, authorized) || limit < 1 {
		return ucidomain.EmbeddingBatch{}, fmt.Errorf("uci embedding prepare: invalid request")
	}
	var batch ucidomain.EmbeddingBatch
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, _, err := lockUCIEmbeddingJobScope(ctx, tx, claim.Ref)
		if err != nil {
			return err
		}
		if !uciEmbeddingProfileMatches(scope.Profile, claim.Profile) {
			return ucidomain.ErrEmbeddingJobObsolete
		}
		progress, err := parseUCIEmbeddingProgress(scope.Job.Counts)
		if err != nil {
			return err
		}
		candidates, exhausted, err := loadUCIEmbeddingCandidatePage(ctx, tx, claim.Ref.Context, progress.Cursor, limit, claim.Profile)
		if err != nil {
			return err
		}
		missing, err := attachUCIEmbeddingBatchCacheHits(ctx, tx, scope, candidates)
		if err != nil {
			return err
		}
		next := progress.Cursor
		if len(candidates) != 0 {
			last := candidates[len(candidates)-1].Key
			next = &last
		}
		batch = ucidomain.EmbeddingBatch{
			Job:                 claim.Ref,
			StartAfter:          cloneUCIEmbeddingCursor(progress.Cursor),
			NextAfter:           cloneUCIEmbeddingCursor(next),
			Candidates:          candidates,
			MissingInputIndexes: missing,
			Exhausted:           exhausted && len(missing) == 0,
		}
		digest, err := digestUCIEmbeddingBatch(batch)
		if err != nil {
			return err
		}
		batch.BatchDigest = digest
		if len(missing) == 0 {
			progress.LastBatchDigest = string(digest)
			progress.Exhausted = exhausted
			if len(candidates) != 0 {
				progress.Cursor = next
				progress.Scanned += uint64(len(candidates))
				progress.Ready += uint64(len(candidates))
			}
			if err := updateUCIEmbeddingProgress(ctx, tx, scope.Job, claim.Ref, progress); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return ucidomain.EmbeddingBatch{}, err
	}
	return batch, nil
}

func attachUCIEmbeddingBatchCacheHits(ctx context.Context, tx *gorm.DB, scope uciEmbeddingJobScope, candidates []ucidomain.EmbeddingCandidate) ([]int, error) {
	missing := make([]int, 0, len(candidates))
	for index, candidate := range candidates {
		hit, err := attachUCIEmbeddingCacheHit(ctx, tx, scope, candidate)
		if err != nil {
			return nil, err
		}
		if !hit {
			missing = append(missing, index)
		}
	}
	return missing, nil
}

func (s *UCIProjectionStore) CommitEmbeddingBatch(ctx context.Context, claim ucidomain.EmbeddingJobClaim, authorized ucidomain.AuthorizedContext, batch ucidomain.EmbeddingBatch, vectors [][]float32) error {
	if err := s.requireDB("commit embedding batch"); err != nil {
		return err
	}
	if ctx == nil || ctx.Err() != nil || !claim.ValidForEmbeddingJob() || !embeddingAuthorizedContextMatches(claim, authorized) || !batch.ValidForEmbeddingJob(claim) {
		return fmt.Errorf("uci embedding commit: invalid request")
	}
	if len(vectors) != len(batch.MissingInputIndexes) {
		return fmt.Errorf("uci embedding commit: provider cardinality is invalid")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, now, err := lockUCIEmbeddingJobScope(ctx, tx, claim.Ref)
		if err != nil {
			return err
		}
		progress, replay, err := prepareUCIEmbeddingBatchCommit(scope, claim, batch)
		if err != nil {
			return err
		}
		if replay {
			return nil
		}
		if err := writeUCIEmbeddingBatch(ctx, tx, scope, claim, batch, vectors, now); err != nil {
			return err
		}
		progress.Cursor = cloneUCIEmbeddingCursor(batch.NextAfter)
		progress.Scanned += uint64(len(batch.Candidates))
		progress.Ready += uint64(len(batch.Candidates))
		progress.LastBatchDigest = string(batch.BatchDigest)
		progress.Exhausted = false
		return updateUCIEmbeddingProgress(ctx, tx, scope.Job, claim.Ref, progress)
	})
}

func prepareUCIEmbeddingBatchCommit(scope uciEmbeddingJobScope, claim ucidomain.EmbeddingJobClaim, batch ucidomain.EmbeddingBatch) (uciEmbeddingProgress, bool, error) {
	if !uciEmbeddingProfileMatches(scope.Profile, claim.Profile) {
		return uciEmbeddingProgress{}, false, ucidomain.ErrEmbeddingJobObsolete
	}
	digest, err := digestUCIEmbeddingBatch(batch)
	if err != nil || digest != batch.BatchDigest {
		return uciEmbeddingProgress{}, false, fmt.Errorf("uci embedding commit: batch digest is invalid")
	}
	progress, err := parseUCIEmbeddingProgress(scope.Job.Counts)
	if err != nil {
		return uciEmbeddingProgress{}, false, err
	}
	if progress.LastBatchDigest == string(batch.BatchDigest) && uciEmbeddingCursorsEqual(progress.Cursor, batch.NextAfter) {
		return progress, true, nil
	}
	if !uciEmbeddingCursorsEqual(progress.Cursor, batch.StartAfter) || progress.Exhausted {
		return uciEmbeddingProgress{}, false, fmt.Errorf("uci embedding commit: cursor is stale")
	}
	return progress, false, nil
}

func writeUCIEmbeddingBatch(ctx context.Context, tx *gorm.DB, scope uciEmbeddingJobScope, claim ucidomain.EmbeddingJobClaim, batch ucidomain.EmbeddingBatch, vectors [][]float32, now time.Time) error {
	for vectorIndex, candidateIndex := range batch.MissingInputIndexes {
		if err := validateUCISemanticVector(vectors[vectorIndex], claim.Profile.Dimension); err != nil {
			return fmt.Errorf("uci embedding commit: provider vector is invalid")
		}
		candidate := batch.Candidates[candidateIndex]
		embedding, err := upsertUCIReadyEmbedding(ctx, tx, scope, candidate, vectors[vectorIndex], now)
		if err != nil {
			return err
		}
		if err := linkUCIEmbeddingCandidate(ctx, tx, scope, candidate, embedding.EmbeddingID); err != nil {
			return err
		}
	}
	return nil
}

func (s *UCIProjectionStore) CompleteEmbeddingJob(ctx context.Context, claim ucidomain.EmbeddingJobClaim, authorized ucidomain.AuthorizedContext) error {
	if err := s.requireDB("complete embedding job"); err != nil {
		return err
	}
	if ctx == nil || ctx.Err() != nil || !claim.ValidForEmbeddingJob() || !embeddingAuthorizedContextMatches(claim, authorized) {
		return fmt.Errorf("uci embedding complete: invalid request")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scope, now, err := lockUCIEmbeddingJobScope(ctx, tx, claim.Ref)
		if err != nil {
			return err
		}
		if !uciEmbeddingProfileMatches(scope.Profile, claim.Profile) {
			return ucidomain.ErrEmbeddingJobObsolete
		}
		progress, err := parseUCIEmbeddingProgress(scope.Job.Counts)
		if err != nil || !progress.Exhausted {
			return fmt.Errorf("uci embedding complete: enumeration is incomplete")
		}
		total, ready, last, err := loadUCIEmbeddingCoverage(ctx, tx, claim.Ref.Context, scope.Profile.EmbeddingProfileID, claim.Profile)
		if err != nil {
			return err
		}
		if !uciEmbeddingCursorsEqual(progress.Cursor, last) || progress.Total != total || progress.Scanned != total || progress.Ready != ready || total != ready {
			return fmt.Errorf("uci embedding complete: exact coverage is incomplete")
		}
		updates := map[string]any{
			"state":        UCIJobSucceeded,
			"lease_owner":  nil,
			"lease_expiry": nil,
			"retry_after":  nil,
			"updated_at":   now,
		}
		if total == 0 {
			updates["error_code"] = string(ucidomain.EmbeddingFailureNoCandidates)
		} else {
			updates["error_code"] = nil
		}
		result := tx.WithContext(ctx).Model(&UCIJob{}).Where(uciEmbeddingJobLeaseWhere, scope.Job.JobID, UCIJobRunning, claim.Ref.LeaseEpoch, claim.Ref.LeaseOwner, now).Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("uci embedding complete update: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ucidomain.ErrEmbeddingJobLeaseLost
		}
		return nil
	})
}

func (s *UCIProjectionStore) FailEmbeddingJob(ctx context.Context, ref ucidomain.EmbeddingJobRef, failure ucidomain.EmbeddingFailure) error {
	if err := s.requireDB("fail embedding job"); err != nil {
		return err
	}
	if ctx == nil || ctx.Err() != nil || !ref.ValidForEmbeddingJob() || !failure.ValidForEmbeddingJob() {
		return fmt.Errorf("uci embedding failure: invalid request")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job UCIJob
		if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where(uciEmbeddingJobIDWhere, ref.JobID).First(&job).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ucidomain.ErrEmbeddingJobLeaseLost
			}
			return err
		}
		now, err := uciDatabaseClock(ctx, tx)
		if err != nil {
			return err
		}
		if !matchesUCIEmbeddingLease(job, ref, now) {
			return ucidomain.ErrEmbeddingJobLeaseLost
		}
		updates := map[string]any{
			"lease_owner":  nil,
			"lease_expiry": nil,
			"error_code":   string(failure.Code),
			"updated_at":   now,
		}
		switch failure.Disposition {
		case ucidomain.EmbeddingFailureRetry:
			updates["state"] = UCIJobRetryScheduled
			retryAfter := now.Add(uciEmbeddingRetryDelay(job.JobID, job.Attempt))
			updates["retry_after"] = retryAfter
		case ucidomain.EmbeddingFailureTerminal:
			updates["state"] = UCIJobFailedTerminal
			updates["retry_after"] = nil
		case ucidomain.EmbeddingFailureObsolete:
			updates["state"] = UCIJobObsolete
			updates["retry_after"] = nil
		case ucidomain.EmbeddingFailureCancelled:
			updates["state"] = UCIJobCancelled
			updates["retry_after"] = nil
		default:
			return fmt.Errorf("uci embedding failure: unsupported disposition")
		}
		result := tx.WithContext(ctx).Model(&UCIJob{}).Where(uciEmbeddingJobLeaseWhere, job.JobID, UCIJobRunning, ref.LeaseEpoch, ref.LeaseOwner, now).Updates(updates)
		if result.Error != nil {
			return fmt.Errorf("uci embedding failure update: %w", result.Error)
		}
		if result.RowsAffected != 1 {
			return ucidomain.ErrEmbeddingJobLeaseLost
		}
		return nil
	})
}

func enqueueUCIEmbeddingJob(ctx context.Context, tx *gorm.DB, authRealm, requestedBy string, view UCIView, profile ucidomain.VectorProfile, candidateTotal *uint64) error {
	if validateUCISemanticProfile(profile) != nil || validateUCIRequiredText("auth_realm", authRealm) != nil || validateUCIRequiredText("requested_by", requestedBy) != nil || validateUCIUUID("view_id", view.ViewID) != nil || view.Generation < 1 {
		return fmt.Errorf("uci embedding enqueue: invalid binding")
	}
	now, err := uciDatabaseClock(ctx, tx)
	if err != nil {
		return err
	}
	embeddingProfile, err := upsertUCIEmbeddingProfileTx(ctx, tx, view.ProfileID, profile, now)
	if err != nil {
		return err
	}
	fingerprint, err := canonicalUCIEmbeddingJobFingerprint(authRealm, requestedBy, view, *embeddingProfile)
	if err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Model(&UCIJob{}).Where("source_id = ? AND checkout_id = ? AND incarnation_id = ? AND job_kind = ? AND target_view_id <> ? AND state IN (?, ?, ?)", view.SourceID, view.CheckoutID, view.IncarnationID, "embed", view.ViewID, UCIJobQueued, UCIJobRunning, UCIJobRetryScheduled).Updates(map[string]any{
		"state":        UCIJobObsolete,
		"error_code":   string(ucidomain.EmbeddingFailureTargetObsolete),
		"retry_after":  nil,
		"lease_owner":  nil,
		"lease_expiry": nil,
		"updated_at":   now,
	}).Error; err != nil {
		return fmt.Errorf("uci embedding obsolete prior targets: %w", err)
	}
	if err := tx.WithContext(ctx).Model(&UCIJob{}).Where("source_id = ? AND checkout_id = ? AND incarnation_id = ? AND job_kind = ? AND target_view_id = ? AND embedding_profile_id <> ? AND state IN (?, ?, ?)", view.SourceID, view.CheckoutID, view.IncarnationID, "embed", view.ViewID, embeddingProfile.EmbeddingProfileID, UCIJobQueued, UCIJobRunning, UCIJobRetryScheduled).Updates(map[string]any{
		"state":        UCIJobObsolete,
		"error_code":   string(ucidomain.EmbeddingFailureTargetObsolete),
		"retry_after":  nil,
		"lease_owner":  nil,
		"lease_expiry": nil,
		"updated_at":   now,
	}).Error; err != nil {
		return fmt.Errorf("uci embedding obsolete replaced profile: %w", err)
	}
	if candidateTotal == nil {
		total, _, err := loadUCIEmbeddingCoverageSummary(ctx, tx, uciContextRefFromView(view), embeddingProfile.EmbeddingProfileID, profile)
		if err != nil {
			return err
		}
		candidateTotal = &total
	}
	counts, err := marshalUCIEmbeddingProgress(uciEmbeddingProgress{Version: 1, Total: *candidateTotal})
	if err != nil {
		return err
	}
	checkoutID := view.CheckoutID
	incarnationID := view.IncarnationID
	analysisProfileID := view.ProfileID
	targetViewID := view.ViewID
	embeddingProfileID := embeddingProfile.EmbeddingProfileID
	targetGeneration := view.Generation
	ownerEpoch := int64(0)
	job := UCIJob{
		JobID:              uuid.NewString(),
		SourceID:           view.SourceID,
		CheckoutID:         &checkoutID,
		JobKind:            "embed",
		InputFingerprint:   fingerprint,
		OwnerEpoch:         &ownerEpoch,
		TargetGeneration:   &targetGeneration,
		TargetViewID:       &targetViewID,
		State:              UCIJobQueued,
		Attempt:            0,
		Counts:             counts,
		RequestedBy:        &requestedBy,
		IncarnationID:      &incarnationID,
		ProfileID:          &analysisProfileID,
		EmbeddingProfileID: &embeddingProfileID,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	result := tx.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&job)
	if result.Error != nil {
		return fmt.Errorf("uci embedding enqueue: %w", result.Error)
	}
	return nil
}

func upsertUCIEmbeddingProfileTx(ctx context.Context, tx *gorm.DB, analysisProfileID string, profile ucidomain.VectorProfile, now time.Time) (*UCIEmbeddingProfile, error) {
	if validateUCIUUID("analysis_profile_id", analysisProfileID) != nil || validateUCISemanticProfile(profile) != nil {
		return nil, fmt.Errorf("uci embedding profile: invalid binding")
	}
	row := &UCIEmbeddingProfile{
		EmbeddingProfileID:    uuid.NewString(),
		AnalysisProfileID:     analysisProfileID,
		ProviderRef:           profile.ProviderRef,
		Model:                 profile.Model,
		Dimension:             profile.Dimension,
		PreprocessingRevision: profile.PreprocessingRevision,
		IncludeRelativePath:   profile.IncludeRelativePath,
		CreatedAt:             now,
	}
	result := tx.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "analysis_profile_id"}, {Name: "provider_ref"}, {Name: "model"}, {Name: "dimension"}, {Name: "preprocessing_revision"}, {Name: "include_relative_path"}},
		DoNothing: true,
	}).Create(row)
	if result.Error != nil {
		return nil, fmt.Errorf("uci embedding profile upsert: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return row, nil
	}
	var existing UCIEmbeddingProfile
	if err := tx.WithContext(ctx).Where("analysis_profile_id = ? AND provider_ref = ? AND model = ? AND dimension = ? AND preprocessing_revision = ? AND include_relative_path = ?", analysisProfileID, profile.ProviderRef, profile.Model, profile.Dimension, profile.PreprocessingRevision, profile.IncludeRelativePath).First(&existing).Error; err != nil {
		return nil, fmt.Errorf("uci embedding profile load: %w", err)
	}
	return &existing, nil
}

func loadUCIEmbeddingClaimFacts(ctx context.Context, tx *gorm.DB, job UCIJob, source UCISource, configured ucidomain.VectorProfile) (uciEmbeddingJobScope, string, error) {
	if job.JobKind != "embed" || job.CheckoutID == nil || job.IncarnationID == nil || job.ProfileID == nil || job.TargetViewID == nil || job.EmbeddingProfileID == nil || job.RequestedBy == nil || job.TargetGeneration == nil || job.OwnerEpoch == nil {
		return uciEmbeddingJobScope{}, string(UCIJobObsolete), nil
	}
	var checkout UCICheckout
	if err := tx.WithContext(ctx).Where("checkout_id = ? AND source_id = ? AND incarnation_id = ?", *job.CheckoutID, job.SourceID, *job.IncarnationID).First(&checkout).Error; err != nil {
		return uciEmbeddingJobScope{}, string(UCIJobCancelled), nil
	}
	if source.State != UCISourceActive || checkout.State == UCICheckoutOffline {
		return uciEmbeddingJobScope{}, "", nil
	}
	if checkout.CurrentViewID == nil || *checkout.CurrentViewID != *job.TargetViewID {
		return uciEmbeddingJobScope{}, string(UCIJobObsolete), nil
	}
	if source.AuthRealm == "" || checkout.OwnerPrincipal != *job.RequestedBy {
		return uciEmbeddingJobScope{}, string(UCIJobCancelled), nil
	}
	var view UCIView
	if err := tx.WithContext(ctx).Where("view_id = ? AND checkout_id = ? AND source_id = ? AND incarnation_id = ? AND profile_id = ? AND generation = ? AND state = ?", *job.TargetViewID, *job.CheckoutID, job.SourceID, *job.IncarnationID, *job.ProfileID, *job.TargetGeneration, UCIViewPublished).First(&view).Error; err != nil {
		return uciEmbeddingJobScope{}, string(UCIJobObsolete), nil
	}
	var embeddingProfile UCIEmbeddingProfile
	if err := tx.WithContext(ctx).Where("embedding_profile_id = ?", *job.EmbeddingProfileID).First(&embeddingProfile).Error; err != nil {
		return uciEmbeddingJobScope{}, string(UCIJobObsolete), nil
	}
	if !uciEmbeddingProfileMatches(embeddingProfile, configured) || embeddingProfile.AnalysisProfileID != view.ProfileID {
		return uciEmbeddingJobScope{}, string(UCIJobObsolete), nil
	}
	fingerprint, err := canonicalUCIEmbeddingJobFingerprint(source.AuthRealm, *job.RequestedBy, view, embeddingProfile)
	if err != nil {
		return uciEmbeddingJobScope{}, "", err
	}
	if fingerprint != job.InputFingerprint {
		return uciEmbeddingJobScope{}, string(UCIJobCancelled), nil
	}
	return uciEmbeddingJobScope{Job: job, Source: source, Checkout: checkout, View: view, Profile: embeddingProfile}, "", nil
}

func setUCIEmbeddingUnclaimedState(ctx context.Context, tx *gorm.DB, jobID, state string, now time.Time) error {
	if state != string(UCIJobObsolete) && state != string(UCIJobCancelled) {
		return fmt.Errorf("uci embedding claim: invalid terminal transition")
	}
	code := ucidomain.EmbeddingFailureTargetObsolete
	if state == string(UCIJobCancelled) {
		code = ucidomain.EmbeddingFailureAuthorityLost
	}
	return tx.WithContext(ctx).Model(&UCIJob{}).Where(uciEmbeddingJobIDWhere, jobID).Updates(map[string]any{
		"state":        UCIJobState(state),
		"error_code":   string(code),
		"retry_after":  nil,
		"lease_owner":  nil,
		"lease_expiry": nil,
		"updated_at":   now,
	}).Error
}

func lockUCIEmbeddingJobScope(ctx context.Context, tx *gorm.DB, ref ucidomain.EmbeddingJobRef) (uciEmbeddingJobScope, time.Time, error) {
	if !ref.ValidForEmbeddingJob() {
		return uciEmbeddingJobScope{}, time.Time{}, fmt.Errorf("uci embedding scope: invalid reference")
	}
	var checkout UCICheckout
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("checkout_id = ? AND source_id = ? AND incarnation_id = ?", ref.Scope.CheckoutID, ref.Scope.SourceID, ref.Scope.IncarnationID).First(&checkout).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uciEmbeddingJobScope{}, time.Time{}, ucidomain.ErrEmbeddingJobCancelled
		}
		return uciEmbeddingJobScope{}, time.Time{}, err
	}
	var source UCISource
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_id = ?", ref.Scope.SourceID).First(&source).Error; err != nil {
		return uciEmbeddingJobScope{}, time.Time{}, err
	}
	var job UCIJob
	if err := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where(uciEmbeddingJobIDWhere, ref.JobID).First(&job).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return uciEmbeddingJobScope{}, time.Time{}, ucidomain.ErrEmbeddingJobLeaseLost
		}
		return uciEmbeddingJobScope{}, time.Time{}, err
	}
	now, err := uciDatabaseClock(ctx, tx)
	if err != nil {
		return uciEmbeddingJobScope{}, time.Time{}, err
	}
	if !matchesUCIEmbeddingLease(job, ref, now) {
		return uciEmbeddingJobScope{}, time.Time{}, ucidomain.ErrEmbeddingJobLeaseLost
	}
	if source.State != UCISourceActive || checkout.State == UCICheckoutOffline {
		return uciEmbeddingJobScope{}, time.Time{}, fmt.Errorf("uci embedding scope: source or checkout unavailable")
	}
	if job.CheckoutID == nil || job.IncarnationID == nil || job.TargetViewID == nil || job.ProfileID == nil || job.EmbeddingProfileID == nil || job.RequestedBy == nil || job.TargetGeneration == nil || *job.CheckoutID != ref.Scope.CheckoutID || *job.IncarnationID != ref.Scope.IncarnationID || *job.TargetViewID != ref.Context.ViewID || *job.ProfileID != ref.Context.AnalysisProfileID || *job.TargetGeneration != ref.Context.Generation || *job.EmbeddingProfileID != ref.EmbeddingProfileID {
		return uciEmbeddingJobScope{}, time.Time{}, ucidomain.ErrEmbeddingJobObsolete
	}
	if checkout.CurrentViewID == nil || *checkout.CurrentViewID != ref.Context.ViewID {
		return uciEmbeddingJobScope{}, time.Time{}, ucidomain.ErrEmbeddingJobObsolete
	}
	if checkout.OwnerPrincipal != *job.RequestedBy {
		return uciEmbeddingJobScope{}, time.Time{}, ucidomain.ErrEmbeddingJobCancelled
	}
	var view UCIView
	if err := tx.WithContext(ctx).Where("view_id = ? AND checkout_id = ? AND source_id = ? AND incarnation_id = ? AND profile_id = ? AND generation = ? AND state = ?", ref.Context.ViewID, ref.Scope.CheckoutID, ref.Scope.SourceID, ref.Scope.IncarnationID, ref.Context.AnalysisProfileID, ref.Context.Generation, UCIViewPublished).First(&view).Error; err != nil {
		return uciEmbeddingJobScope{}, time.Time{}, ucidomain.ErrEmbeddingJobObsolete
	}
	var profile UCIEmbeddingProfile
	if err := tx.WithContext(ctx).Where("embedding_profile_id = ?", ref.EmbeddingProfileID).First(&profile).Error; err != nil {
		return uciEmbeddingJobScope{}, time.Time{}, ucidomain.ErrEmbeddingJobObsolete
	}
	if profile.AnalysisProfileID != view.ProfileID {
		return uciEmbeddingJobScope{}, time.Time{}, ucidomain.ErrEmbeddingJobObsolete
	}
	fingerprint, err := canonicalUCIEmbeddingJobFingerprint(source.AuthRealm, *job.RequestedBy, view, profile)
	if err != nil {
		return uciEmbeddingJobScope{}, time.Time{}, err
	}
	if fingerprint != job.InputFingerprint {
		return uciEmbeddingJobScope{}, time.Time{}, ucidomain.ErrEmbeddingJobCancelled
	}
	return uciEmbeddingJobScope{Job: job, Source: source, Checkout: checkout, View: view, Profile: profile}, now, nil
}

func matchesUCIEmbeddingLease(job UCIJob, ref ucidomain.EmbeddingJobRef, now time.Time) bool {
	return job.JobKind == "embed" && job.SourceID == ref.Scope.SourceID && job.CheckoutID != nil && *job.CheckoutID == ref.Scope.CheckoutID && job.IncarnationID != nil && *job.IncarnationID == ref.Scope.IncarnationID && job.TargetViewID != nil && *job.TargetViewID == ref.Context.ViewID && job.EmbeddingProfileID != nil && *job.EmbeddingProfileID == ref.EmbeddingProfileID && job.OwnerEpoch != nil && *job.OwnerEpoch == ref.LeaseEpoch && job.LeaseOwner != nil && *job.LeaseOwner == ref.LeaseOwner && job.State == UCIJobRunning && job.LeaseExpiry != nil && job.LeaseExpiry.After(now)
}

func embeddingJobRefFromRows(job UCIJob, view UCIView, owner string, epoch int64) ucidomain.EmbeddingJobRef {
	return ucidomain.EmbeddingJobRef{
		JobID:              job.JobID,
		Scope:              ucidomain.IndexScope{SourceID: job.SourceID, CheckoutID: *job.CheckoutID, IncarnationID: *job.IncarnationID},
		Context:            uciContextRefFromView(view),
		EmbeddingProfileID: *job.EmbeddingProfileID,
		InputFingerprint:   ucidomain.IndexDigest(job.InputFingerprint),
		LeaseOwner:         owner,
		LeaseEpoch:         epoch,
	}
}

func loadUCIEmbeddingCandidatePage(ctx context.Context, db *gorm.DB, ref ucidomain.ContextRef, after *ucidomain.EmbeddingCandidateKey, limit int, profile ucidomain.VectorProfile) ([]ucidomain.EmbeddingCandidate, bool, error) {
	if validateUCIQueryContext(ref) != nil || validateUCISemanticProfile(profile) != nil || limit < 1 {
		return nil, false, fmt.Errorf("uci embedding candidates: invalid request")
	}
	arguments := []any{ref.ViewID, ref.SourceID, ref.CheckoutID, ref.AnalysisProfileID, ref.Generation, UCIViewPublished, UCIViewSuperseded, UCIBlobStored, UCIFilePresent, UCIParseArtifactComplete, UCIParseArtifactPartial}
	cursorClause := ""
	if after != nil {
		if validateUCIUUID("membership_id", after.MembershipID) != nil || validateUCIUUID("chunk_id", after.ChunkID) != nil {
			return nil, false, fmt.Errorf("uci embedding candidates: invalid cursor")
		}
		cursorClause = "AND (membership.membership_id, chunk.chunk_id) > (?, ?)"
		arguments = append(arguments, after.MembershipID, after.ChunkID)
	}
	arguments = append(arguments, limit)
	query := `
		WITH selected_view AS (
			SELECT view_row.source_id, view_row.checkout_id, view_row.generation, view_row.profile_id AS analysis_profile_id, profile_row.parser_bundle_digest
			FROM ci_views AS view_row
			JOIN ci_profiles AS profile_row ON profile_row.profile_id = view_row.profile_id
			WHERE view_row.view_id = ? AND view_row.source_id = ? AND view_row.checkout_id = ? AND view_row.profile_id = ? AND view_row.generation = ? AND view_row.state IN (?, ?)
		)
		SELECT
			membership.membership_id,
			chunk.chunk_id,
			artifact.artifact_id,
			blob.content_digest AS chunk_content_digest,
			artifact.facts_digest,
			(SELECT COUNT(*) FROM ci_definitions AS proof_definition WHERE proof_definition.artifact_id = artifact.artifact_id) AS definition_count,
			(SELECT COUNT(*) FROM ci_reference_sites AS proof_reference WHERE proof_reference.artifact_id = artifact.artifact_id) AS reference_site_count,
			(SELECT COUNT(*) FROM ci_chunks AS proof_chunk WHERE proof_chunk.artifact_id = artifact.artifact_id) AS chunk_count,
			COALESCE(NULLIF(definition.qualified_local_name, ''), NULLIF(chunk.symbol_key, ''), membership.path_key || ':' || chunk.ordinal::text) AS entity_key,
			COALESCE(definition.name, '') AS local_name,
			COALESCE(definition.qualified_local_name, '') AS qualified_symbol,
			membership.display_path AS relative_path,
			chunk.byte_start,
			chunk.byte_end,
			array_length(regexp_split_to_array(convert_from(substring(blob.safe_content FROM 1 FOR chunk.byte_start::integer), replace(upper(blob.encoding), '-', '')), E'\n'), 1) AS line_start,
			array_length(regexp_split_to_array(convert_from(substring(blob.safe_content FROM 1 FOR chunk.byte_end::integer), replace(upper(blob.encoding), '-', '')), E'\n'), 1) AS line_end,
			chunk.text_for_search AS text,
			chunk.chunk_kind,
			artifact.language,
			blob.protection_domain
		FROM selected_view AS view_row
		JOIN ci_memberships AS membership ON membership.source_id = view_row.source_id AND membership.checkout_id = view_row.checkout_id AND membership.valid_from_generation <= view_row.generation AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view_row.generation)
		JOIN ci_parse_artifacts AS artifact ON artifact.source_id = membership.source_id AND artifact.artifact_id = membership.artifact_id AND artifact.extraction_profile_digest = view_row.parser_bundle_digest
		JOIN ci_blobs AS blob ON blob.source_id = artifact.source_id AND blob.blob_id = artifact.blob_id
		JOIN ci_chunks AS chunk ON chunk.source_id = artifact.source_id AND chunk.artifact_id = artifact.artifact_id
		LEFT JOIN ci_definitions AS definition ON definition.artifact_id = chunk.artifact_id AND definition.local_symbol_key = chunk.symbol_key
		WHERE blob.storage_state = ? AND membership.file_state = ? AND artifact.status IN (?, ?) AND artifact.sealed_at IS NOT NULL AND artifact.facts_digest IS NOT NULL
		` + cursorClause + `
		ORDER BY membership.membership_id ASC, chunk.chunk_id ASC
		LIMIT ?`
	var rows []uciEmbeddingCandidateRow
	if err := db.WithContext(ctx).Raw(query, arguments...).Scan(&rows).Error; err != nil {
		return nil, false, fmt.Errorf("uci embedding candidates: %w", err)
	}
	candidates := make([]ucidomain.EmbeddingCandidate, 0, len(rows))
	for _, row := range rows {
		candidate, ok := row.candidate(ref, profile)
		if !ok {
			return nil, false, fmt.Errorf("uci embedding candidates: invalid stored candidate")
		}
		candidates = append(candidates, candidate)
	}
	return candidates, len(rows) < limit, nil
}

func attachUCIEmbeddingCacheHit(ctx context.Context, tx *gorm.DB, scope uciEmbeddingJobScope, candidate ucidomain.EmbeddingCandidate) (bool, error) {
	var embedding UCIEmbedding
	result := tx.WithContext(ctx).Where("embedding_profile_id = ? AND embedding_input_digest = ? AND source_id = ? AND protection_domain = ? AND status = ? AND vector IS NOT NULL", scope.Profile.EmbeddingProfileID, string(candidate.InputDigest), scope.View.SourceID, candidate.ProtectionDomain, UCIEmbeddingReady).Order("embedding_id ASC").Limit(1).Find(&embedding)
	if result.Error != nil {
		return false, fmt.Errorf("uci embedding cache lookup: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return false, nil
	}
	if err := linkUCIEmbeddingCandidate(ctx, tx, scope, candidate, embedding.EmbeddingID); err != nil {
		return false, err
	}
	return true, nil
}

func upsertUCIReadyEmbedding(ctx context.Context, tx *gorm.DB, scope uciEmbeddingJobScope, candidate ucidomain.EmbeddingCandidate, vector []float32, now time.Time) (UCIEmbedding, error) {
	var existing UCIEmbedding
	result := tx.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).Where("embedding_profile_id = ? AND embedding_input_digest = ? AND source_id = ? AND protection_domain = ?", scope.Profile.EmbeddingProfileID, string(candidate.InputDigest), scope.View.SourceID, candidate.ProtectionDomain).Find(&existing)
	if result.Error != nil {
		return UCIEmbedding{}, fmt.Errorf("uci embedding vector lookup: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		copyVector := pgvector.NewVector(append([]float32(nil), vector...))
		created := UCIEmbedding{EmbeddingID: uuid.NewString(), EmbeddingProfileID: scope.Profile.EmbeddingProfileID, EmbeddingInputDigest: string(candidate.InputDigest), Vector: &copyVector, SourceID: scope.View.SourceID, ProtectionDomain: candidate.ProtectionDomain, CompletionSeq: scope.View.Generation, Status: UCIEmbeddingReady, CreatedAt: now}
		if err := tx.WithContext(ctx).Create(&created).Error; err != nil {
			return UCIEmbedding{}, fmt.Errorf("uci embedding vector create: %w", err)
		}
		return created, nil
	}
	if existing.Status == UCIEmbeddingReady {
		if existing.Vector == nil || validateUCISemanticVector(existing.Vector.Slice(), scope.Profile.Dimension) != nil {
			return UCIEmbedding{}, fmt.Errorf("uci embedding vector ready row is invalid")
		}
		return existing, nil
	}
	copyVector := pgvector.NewVector(append([]float32(nil), vector...))
	result = tx.WithContext(ctx).Model(&UCIEmbedding{}).Where("embedding_id = ? AND status IN (?, ?)", existing.EmbeddingID, UCIEmbeddingPending, UCIEmbeddingFailed).Updates(map[string]any{"vector": copyVector, "status": UCIEmbeddingReady, "completion_seq": scope.View.Generation})
	if result.Error != nil {
		return UCIEmbedding{}, fmt.Errorf("uci embedding vector promote: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return UCIEmbedding{}, fmt.Errorf("uci embedding vector promotion lost")
	}
	existing.Vector = &copyVector
	existing.Status = UCIEmbeddingReady
	existing.CompletionSeq = scope.View.Generation
	return existing, nil
}

func linkUCIEmbeddingCandidate(ctx context.Context, tx *gorm.DB, scope uciEmbeddingJobScope, candidate ucidomain.EmbeddingCandidate, embeddingID string) error {
	pathFingerprint := uciSemanticRelativePathFingerprint(ucidomain.VectorProfile{IncludeRelativePath: scope.Profile.IncludeRelativePath}, candidate.Candidate.RelativePath)
	var existing UCIChunkEmbedding
	result := tx.WithContext(ctx).Where("source_id = ? AND chunk_id = ? AND relative_path_fingerprint = ? AND embedding_profile_id = ? AND embedding_input_digest = ?", scope.View.SourceID, candidate.Key.ChunkID, pathFingerprint, scope.Profile.EmbeddingProfileID, string(candidate.InputDigest)).Limit(1).Find(&existing)
	if result.Error != nil {
		return fmt.Errorf("uci embedding link lookup: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		if existing.EmbeddingID != embeddingID {
			return fmt.Errorf("uci embedding link conflicts with immutable cache identity")
		}
		return nil
	}
	link := UCIChunkEmbedding{ChunkEmbeddingID: uuid.NewString(), SourceID: scope.View.SourceID, ChunkID: candidate.Key.ChunkID, EmbeddingID: embeddingID, RelativePathFingerprint: pathFingerprint, EmbeddingProfileID: scope.Profile.EmbeddingProfileID, EmbeddingInputDigest: string(candidate.InputDigest), CreatedAt: time.Now().UTC()}
	if err := tx.WithContext(ctx).Create(&link).Error; err != nil {
		return fmt.Errorf("uci embedding link create: %w", err)
	}
	return nil
}

type uciEmbeddingCoverageKey struct {
	ChunkID                 string `gorm:"column:chunk_id"`
	RelativePathFingerprint string `gorm:"column:relative_path_fingerprint"`
	EmbeddingInputDigest    string `gorm:"column:embedding_input_digest"`
	ProtectionDomain        string `gorm:"column:protection_domain"`
}

func loadUCIEmbeddingCoverageSummary(ctx context.Context, db *gorm.DB, ref ucidomain.ContextRef, embeddingProfileID string, profile ucidomain.VectorProfile) (uint64, uint64, error) {
	if validateUCIQueryContext(ref) != nil || validateUCIUUID("embedding_profile_id", embeddingProfileID) != nil || validateUCISemanticProfile(profile) != nil {
		return 0, 0, fmt.Errorf("uci embedding coverage summary: invalid request")
	}
	var row struct {
		Total int64 `gorm:"column:total"`
		Ready int64 `gorm:"column:ready"`
	}
	err := db.WithContext(ctx).Raw(`
		WITH selected_view AS (
			SELECT view_row.source_id, view_row.checkout_id, view_row.generation, profile_row.parser_bundle_digest
			FROM ci_views AS view_row
			JOIN ci_profiles AS profile_row ON profile_row.profile_id = view_row.profile_id
			WHERE view_row.view_id = ? AND view_row.source_id = ? AND view_row.checkout_id = ?
				AND view_row.profile_id = ? AND view_row.generation = ? AND view_row.state IN (?, ?)
		)
		SELECT
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE EXISTS (
				SELECT 1
				FROM ci_chunk_embeddings AS link
				JOIN ci_embeddings AS embedding
					ON embedding.source_id = link.source_id
					AND embedding.embedding_id = link.embedding_id
					AND embedding.embedding_profile_id = link.embedding_profile_id
					AND embedding.embedding_input_digest = link.embedding_input_digest
				WHERE link.source_id = chunk.source_id
					AND link.chunk_id = chunk.chunk_id
					AND link.embedding_profile_id = ?
					AND link.relative_path_fingerprint = CASE WHEN ? THEN membership.display_path ELSE ? END
					AND embedding.status = ?
					AND embedding.vector IS NOT NULL
			)) AS ready
		FROM selected_view AS view_row
		JOIN ci_memberships AS membership
			ON membership.source_id = view_row.source_id
			AND membership.checkout_id = view_row.checkout_id
			AND membership.valid_from_generation <= view_row.generation
			AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view_row.generation)
		JOIN ci_parse_artifacts AS artifact
			ON artifact.source_id = membership.source_id
			AND artifact.artifact_id = membership.artifact_id
			AND artifact.extraction_profile_digest = view_row.parser_bundle_digest
		JOIN ci_blobs AS blob
			ON blob.source_id = artifact.source_id
			AND blob.blob_id = artifact.blob_id
		JOIN ci_chunks AS chunk
			ON chunk.source_id = artifact.source_id
			AND chunk.artifact_id = artifact.artifact_id
		WHERE membership.file_state = ?
			AND blob.storage_state = ?
			AND artifact.status IN (?, ?)
			AND artifact.sealed_at IS NOT NULL
			AND artifact.facts_digest IS NOT NULL
	`,
		ref.ViewID,
		ref.SourceID,
		ref.CheckoutID,
		ref.AnalysisProfileID,
		ref.Generation,
		UCIViewPublished,
		UCIViewSuperseded,
		embeddingProfileID,
		profile.IncludeRelativePath,
		uciSemanticPathIndependentFingerprint,
		UCIEmbeddingReady,
		UCIFilePresent,
		UCIBlobStored,
		UCIParseArtifactComplete,
		UCIParseArtifactPartial,
	).Scan(&row).Error
	if err != nil {
		return 0, 0, fmt.Errorf("uci embedding coverage summary: %w", err)
	}
	if row.Total < 0 || row.Ready < 0 || row.Ready > row.Total {
		return 0, 0, fmt.Errorf("uci embedding coverage summary is invalid")
	}
	return uint64(row.Total), uint64(row.Ready), nil
}

func loadUCIEmbeddingCoverage(ctx context.Context, db *gorm.DB, ref ucidomain.ContextRef, embeddingProfileID string, profile ucidomain.VectorProfile) (uint64, uint64, *ucidomain.EmbeddingCandidateKey, error) {
	var total, ready uint64
	var cursor *ucidomain.EmbeddingCandidateKey
	for {
		candidates, exhausted, err := loadUCIEmbeddingCandidatePage(ctx, db, ref, cursor, 128, profile)
		if err != nil {
			return 0, 0, nil, err
		}
		readyKeys, err := loadUCIEmbeddingReadyCoverageKeys(ctx, db, ref.SourceID, embeddingProfileID, profile, candidates)
		if err != nil {
			return 0, 0, nil, err
		}
		for _, candidate := range candidates {
			total++
			key := uciEmbeddingCoverageKey{
				ChunkID:                 candidate.Key.ChunkID,
				RelativePathFingerprint: uciSemanticRelativePathFingerprint(profile, candidate.Candidate.RelativePath),
				EmbeddingInputDigest:    string(candidate.InputDigest),
				ProtectionDomain:        candidate.ProtectionDomain,
			}
			if _, found := readyKeys[key]; found {
				ready++
			}
		}
		if len(candidates) != 0 {
			last := candidates[len(candidates)-1].Key
			cursor = &last
		}
		if exhausted {
			return total, ready, cursor, nil
		}
	}
}

func loadUCIEmbeddingReadyCoverageKeys(ctx context.Context, db *gorm.DB, sourceID, embeddingProfileID string, profile ucidomain.VectorProfile, candidates []ucidomain.EmbeddingCandidate) (map[uciEmbeddingCoverageKey]struct{}, error) {
	ready := make(map[uciEmbeddingCoverageKey]struct{}, len(candidates))
	if len(candidates) == 0 {
		return ready, nil
	}
	conditions := make([]string, len(candidates))
	arguments := make([]any, 0, 3+len(candidates)*4)
	arguments = append(arguments, sourceID, embeddingProfileID, UCIEmbeddingReady)
	for index, candidate := range candidates {
		conditions[index] = "(link.chunk_id = ? AND link.relative_path_fingerprint = ? AND link.embedding_input_digest = ? AND embedding.protection_domain = ?)"
		arguments = append(arguments,
			candidate.Key.ChunkID,
			uciSemanticRelativePathFingerprint(profile, candidate.Candidate.RelativePath),
			string(candidate.InputDigest),
			candidate.ProtectionDomain,
		)
	}
	var rows []uciEmbeddingCoverageKey
	query := `
		SELECT link.chunk_id, link.relative_path_fingerprint, link.embedding_input_digest, embedding.protection_domain
		FROM ci_chunk_embeddings AS link
		JOIN ci_embeddings AS embedding
			ON embedding.source_id = link.source_id
			AND embedding.embedding_id = link.embedding_id
			AND embedding.embedding_profile_id = link.embedding_profile_id
			AND embedding.embedding_input_digest = link.embedding_input_digest
		WHERE link.source_id = ? AND link.embedding_profile_id = ?
			AND embedding.status = ? AND embedding.vector IS NOT NULL
			AND (` + strings.Join(conditions, " OR ") + `)`
	if err := db.WithContext(ctx).Raw(query, arguments...).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("uci embedding coverage links: %w", err)
	}
	for _, row := range rows {
		ready[row] = struct{}{}
	}
	return ready, nil
}

func parseUCIEmbeddingProgress(raw string) (uciEmbeddingProgress, error) {
	var progress uciEmbeddingProgress
	if err := json.Unmarshal([]byte(raw), &progress); err != nil {
		return uciEmbeddingProgress{}, fmt.Errorf("uci embedding progress decode: %w", err)
	}
	if progress.Version != 1 || (progress.Cursor != nil && (validateUCIUUID("membership_id", progress.Cursor.MembershipID) != nil || validateUCIUUID("chunk_id", progress.Cursor.ChunkID) != nil)) || (progress.LastBatchDigest != "" && !isUCIDigest(progress.LastBatchDigest)) || progress.Ready > progress.Scanned || progress.Scanned > progress.Total || (progress.Exhausted && progress.Scanned != progress.Total) {
		return uciEmbeddingProgress{}, fmt.Errorf("uci embedding progress is invalid")
	}
	return progress, nil
}

func marshalUCIEmbeddingProgress(progress uciEmbeddingProgress) (string, error) {
	if progress.Version != 1 || (progress.Cursor != nil && (validateUCIUUID("membership_id", progress.Cursor.MembershipID) != nil || validateUCIUUID("chunk_id", progress.Cursor.ChunkID) != nil)) || (progress.LastBatchDigest != "" && !isUCIDigest(progress.LastBatchDigest)) || progress.Ready > progress.Scanned || progress.Scanned > progress.Total || (progress.Exhausted && progress.Scanned != progress.Total) {
		return "", fmt.Errorf("uci embedding progress is invalid")
	}
	encoded, err := json.Marshal(progress)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func updateUCIEmbeddingProgress(ctx context.Context, tx *gorm.DB, job UCIJob, ref ucidomain.EmbeddingJobRef, progress uciEmbeddingProgress) error {
	counts, err := marshalUCIEmbeddingProgress(progress)
	if err != nil {
		return err
	}
	result := tx.WithContext(ctx).Model(&UCIJob{}).Where("job_id = ? AND state = ? AND owner_epoch = ? AND lease_owner = ?", job.JobID, UCIJobRunning, ref.LeaseEpoch, ref.LeaseOwner).Updates(map[string]any{"counts": counts, "updated_at": time.Now().UTC()})
	if result.Error != nil {
		return fmt.Errorf("uci embedding progress update: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ucidomain.ErrEmbeddingJobLeaseLost
	}
	return nil
}

func digestUCIEmbeddingBatch(batch ucidomain.EmbeddingBatch) (ucidomain.IndexDigest, error) {
	type candidate struct {
		MembershipID     string `json:"membership_id"`
		ChunkID          string `json:"chunk_id"`
		ProtectionDomain string `json:"protection_domain"`
		InputDigest      string `json:"input_digest"`
	}
	candidates := make([]candidate, 0, len(batch.Candidates))
	for _, value := range batch.Candidates {
		candidates = append(candidates, candidate{MembershipID: value.Key.MembershipID, ChunkID: value.Key.ChunkID, ProtectionDomain: value.ProtectionDomain, InputDigest: string(value.InputDigest)})
	}
	digest, err := canonicalUCIPublicationDigest("embedding_batch", struct {
		JobID       string                           `json:"job_id"`
		Fingerprint string                           `json:"fingerprint"`
		StartAfter  *ucidomain.EmbeddingCandidateKey `json:"start_after"`
		NextAfter   *ucidomain.EmbeddingCandidateKey `json:"next_after"`
		Candidates  []candidate                      `json:"candidates"`
		Missing     []int                            `json:"missing"`
	}{JobID: batch.Job.JobID, Fingerprint: string(batch.Job.InputFingerprint), StartAfter: batch.StartAfter, NextAfter: batch.NextAfter, Candidates: candidates, Missing: batch.MissingInputIndexes})
	if err != nil {
		return "", err
	}
	return ucidomain.IndexDigest(digest), nil
}

func canonicalUCIEmbeddingJobFingerprint(authRealm, requestedBy string, view UCIView, profile UCIEmbeddingProfile) (string, error) {
	return canonicalUCIPublicationDigest("embedding_job", struct {
		AuthRealm             string `json:"auth_realm"`
		RequestedBy           string `json:"requested_by"`
		SourceID              string `json:"source_id"`
		CheckoutID            string `json:"checkout_id"`
		IncarnationID         string `json:"incarnation_id"`
		ViewID                string `json:"view_id"`
		Generation            int64  `json:"generation"`
		ManifestDigest        string `json:"manifest_digest"`
		AnalysisProfileID     string `json:"analysis_profile_id"`
		EmbeddingProfileID    string `json:"embedding_profile_id"`
		ProviderRef           string `json:"provider_ref"`
		Model                 string `json:"model"`
		Dimension             int    `json:"dimension"`
		PreprocessingRevision string `json:"preprocessing_revision"`
		IncludeRelativePath   bool   `json:"include_relative_path"`
	}{authRealm, requestedBy, view.SourceID, view.CheckoutID, view.IncarnationID, view.ViewID, view.Generation, view.ManifestDigest, view.ProfileID, profile.EmbeddingProfileID, profile.ProviderRef, profile.Model, profile.Dimension, profile.PreprocessingRevision, profile.IncludeRelativePath})
}

func uciEmbeddingProfileMatches(row UCIEmbeddingProfile, profile ucidomain.VectorProfile) bool {
	return row.ProviderRef == profile.ProviderRef && row.Model == profile.Model && row.Dimension == profile.Dimension && row.PreprocessingRevision == profile.PreprocessingRevision && row.IncludeRelativePath == profile.IncludeRelativePath
}

func uciEmbeddingRetryDelay(jobID string, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := 5 * time.Second
	for index := 1; index < attempt && delay < 15*time.Minute; index++ {
		delay *= 2
	}
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	sum := sha256.Sum256([]byte(jobID))
	return delay + (delay * time.Duration(sum[0]%10) / 100)
}

func cloneUCIEmbeddingCursor(cursor *ucidomain.EmbeddingCandidateKey) *ucidomain.EmbeddingCandidateKey {
	if cursor == nil {
		return nil
	}
	copy := *cursor
	return &copy
}

func uciEmbeddingCursorsEqual(left, right *ucidomain.EmbeddingCandidateKey) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.MembershipID == right.MembershipID && left.ChunkID == right.ChunkID
}

func embeddingAuthorizedContextMatches(claim ucidomain.EmbeddingJobClaim, authorized ucidomain.AuthorizedContext) bool {
	return claim.Ref.Context.ValidForEmbeddingJob() && authorized.Ref().ValidForEmbeddingJob() && claim.Ref.Context == authorized.Ref()
}

func validUCIEmbeddingOwner(owner string) bool {
	return len(owner) <= 256 && validateUCIRequiredText("lease_owner", owner) == nil
}

type uciEmbeddingReadiness struct {
	ProfileID  string
	JobState   *UCIJobState
	ErrorCode  *string
	RetryAfter *time.Time
	Total      uint64
	Ready      uint64
}

func (readiness uciEmbeddingReadiness) complete() bool {
	return readiness.JobState != nil && *readiness.JobState == UCIJobSucceeded && readiness.ErrorCode == nil && readiness.Total > 0 && readiness.Ready == readiness.Total
}

func (s *UCIProjectionStore) loadUCIEmbeddingReadiness(ctx context.Context, ref ucidomain.ContextRef, profile *ucidomain.VectorProfile) (uciEmbeddingReadiness, error) {
	if profile == nil {
		return uciEmbeddingReadiness{}, nil
	}
	if validateUCIQueryContext(ref) != nil || validateUCISemanticProfile(*profile) != nil {
		return uciEmbeddingReadiness{}, fmt.Errorf("uci embedding readiness: invalid request")
	}
	var embeddingProfile UCIEmbeddingProfile
	result := s.db.WithContext(ctx).Where("analysis_profile_id = ? AND provider_ref = ? AND model = ? AND dimension = ? AND preprocessing_revision = ? AND include_relative_path = ?", ref.AnalysisProfileID, profile.ProviderRef, profile.Model, profile.Dimension, profile.PreprocessingRevision, profile.IncludeRelativePath).Limit(1).Find(&embeddingProfile)
	if result.Error != nil {
		return uciEmbeddingReadiness{}, fmt.Errorf("uci embedding readiness profile: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return uciEmbeddingReadiness{}, nil
	}
	readiness := uciEmbeddingReadiness{ProfileID: embeddingProfile.EmbeddingProfileID}
	type jobRow struct {
		State      UCIJobState `gorm:"column:state"`
		ErrorCode  *string     `gorm:"column:error_code"`
		RetryAfter *time.Time  `gorm:"column:retry_after"`
		Counts     string      `gorm:"column:counts"`
	}
	var job jobRow
	result = s.db.WithContext(ctx).Raw(`
		SELECT state, error_code, retry_after, counts
		FROM ci_jobs
		WHERE job_kind = 'embed' AND source_id = ? AND checkout_id = ? AND incarnation_id = (
			SELECT incarnation_id FROM ci_views WHERE view_id = ?
		) AND profile_id = ? AND target_view_id = ? AND target_generation = ? AND embedding_profile_id = ?
		ORDER BY updated_at DESC, job_id ASC
		LIMIT 1`, ref.SourceID, ref.CheckoutID, ref.ViewID, ref.AnalysisProfileID, ref.ViewID, ref.Generation, embeddingProfile.EmbeddingProfileID).Scan(&job)
	if result.Error != nil {
		return uciEmbeddingReadiness{}, fmt.Errorf("uci embedding readiness job: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		state := job.State
		readiness.JobState = &state
		if job.ErrorCode != nil {
			value := *job.ErrorCode
			readiness.ErrorCode = &value
		}
		if job.RetryAfter != nil {
			value := job.RetryAfter.UTC()
			readiness.RetryAfter = &value
		}
		progress, progressErr := parseUCIEmbeddingProgress(job.Counts)
		if progressErr == nil && (job.State != UCIJobSucceeded || progress.Exhausted) {
			readiness.Total = progress.Total
			readiness.Ready = progress.Ready
			return readiness, nil
		}
	}
	total, ready, err := loadUCIEmbeddingCoverageSummary(ctx, s.db, ref, embeddingProfile.EmbeddingProfileID, *profile)
	if err != nil {
		return uciEmbeddingReadiness{}, err
	}
	readiness.Total = total
	readiness.Ready = ready
	return readiness, nil
}
