package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	ucidomain "github.com/thebtf/engram/internal/uci"
)

var _ ucidomain.IndexStatusStore = (*UCIProjectionStore)(nil)

// LoadIndexStatus loads exactly the already-authorized View. The query binds
// Source, checkout, View, profile, and generation before temporal memberships,
// chunks, embeddings, or jobs are counted.
func (s *UCIProjectionStore) LoadIndexStatus(ctx context.Context, authorized ucidomain.AuthorizedContext) (ucidomain.IndexStatusSnapshot, error) {
	if err := s.requireDB("load index status"); err != nil {
		return ucidomain.IndexStatusSnapshot{}, err
	}
	if ctx == nil {
		return ucidomain.IndexStatusSnapshot{}, fmt.Errorf("uci projection load index status: context is required")
	}
	if err := ctx.Err(); err != nil {
		return ucidomain.IndexStatusSnapshot{}, err
	}
	ref := authorized.Ref()
	if err := validateUCIQueryContext(ref); err != nil {
		return ucidomain.IndexStatusSnapshot{}, err
	}

	row, found, err := s.loadUCIIndexStatusRow(ctx, ref)
	if err != nil {
		return ucidomain.IndexStatusSnapshot{}, ucidomain.NewIndexStatusError(ucidomain.IndexStatusUnavailable, err)
	}
	if !found {
		return ucidomain.IndexStatusSnapshot{}, ucidomain.NewIndexStatusError(ucidomain.IndexStatusNotFound, ucidomain.ErrIndexStatusNotFound)
	}
	snapshot, err := uciIndexStatusSnapshotFromRow(ref, row)
	if err != nil {
		return ucidomain.IndexStatusSnapshot{}, ucidomain.NewIndexStatusError(ucidomain.IndexStatusUnavailable, err)
	}
	return snapshot, nil
}

type uciIndexStatusRow struct {
	SourceState         UCISourceState   `gorm:"column:source_state"`
	CheckoutState       UCICheckoutState `gorm:"column:checkout_state"`
	CurrentViewID       *string          `gorm:"column:current_view_id"`
	ViewState           UCIViewState     `gorm:"column:view_state"`
	Dirty               bool             `gorm:"column:dirty"`
	ObservedFSSeq       int64            `gorm:"column:observed_fs_seq"`
	CoverageJSON        string           `gorm:"column:coverage_json"`
	PublishedAt         *time.Time       `gorm:"column:published_at"`
	ScanStartedAt       time.Time        `gorm:"column:scan_started_at"`
	ScanCompletedAt     time.Time        `gorm:"column:scan_completed_at"`
	ChunkCount          int64            `gorm:"column:chunk_count"`
	ReadyEmbeddingCount int64            `gorm:"column:ready_embedding_count"`
	PendingJobCount     int64            `gorm:"column:pending_job_count"`
	JobState            *string          `gorm:"column:job_state"`
	JobTargetGeneration *int64           `gorm:"column:job_target_generation"`
}

func (s *UCIProjectionStore) loadUCIIndexStatusRow(ctx context.Context, ref ucidomain.ContextRef) (uciIndexStatusRow, bool, error) {
	var row uciIndexStatusRow
	result := s.db.WithContext(ctx).Raw(`
		WITH selected_view AS (
			SELECT
				source.state AS source_state,
				checkout.state AS checkout_state,
				checkout.current_view_id,
				view_row.state AS view_state,
				view_row.dirty,
				view_row.observed_fs_seq,
				view_row.coverage_json,
				view_row.published_at,
				view_row.scan_start AS scan_started_at,
				view_row.scan_end AS scan_completed_at,
				view_row.source_id,
				view_row.checkout_id,
				view_row.incarnation_id,
				view_row.profile_id,
				view_row.view_id,
				view_row.generation,
				profile.parser_bundle_digest
			FROM ci_views AS view_row
			JOIN sources AS source
				ON source.source_id = view_row.source_id
			JOIN ci_checkouts AS checkout
				ON checkout.checkout_id = view_row.checkout_id
				AND checkout.source_id = view_row.source_id
				AND checkout.incarnation_id = view_row.incarnation_id
			JOIN ci_profiles AS profile
				ON profile.profile_id = view_row.profile_id
			WHERE view_row.view_id = ?
				AND view_row.source_id = ?
				AND view_row.checkout_id = ?
				AND view_row.profile_id = ?
				AND view_row.generation = ?
				AND view_row.state IN (?, ?)
		),
		scoped_chunks AS (
			SELECT DISTINCT
				chunk.chunk_id,
				chunk.source_id,
				blob.protection_domain
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
		)
		SELECT
			selected_view.source_state,
			selected_view.checkout_state,
			selected_view.current_view_id,
			selected_view.view_state,
			selected_view.dirty,
			selected_view.observed_fs_seq,
			selected_view.coverage_json,
			selected_view.published_at,
			selected_view.scan_started_at,
			selected_view.scan_completed_at,
			(SELECT COUNT(*) FROM scoped_chunks) AS chunk_count,
			(
				SELECT COUNT(DISTINCT chunk.chunk_id)
				FROM scoped_chunks AS chunk
				CROSS JOIN selected_view AS embedding_view
				JOIN ci_chunk_embeddings AS chunk_embedding
					ON chunk_embedding.source_id = chunk.source_id
					AND chunk_embedding.chunk_id = chunk.chunk_id
				JOIN ci_embedding_profiles AS embedding_profile
					ON embedding_profile.embedding_profile_id = chunk_embedding.embedding_profile_id
					AND embedding_profile.analysis_profile_id = embedding_view.profile_id
				JOIN ci_embeddings AS embedding
					ON embedding.source_id = chunk_embedding.source_id
					AND embedding.embedding_id = chunk_embedding.embedding_id
					AND embedding.embedding_profile_id = chunk_embedding.embedding_profile_id
					AND embedding.embedding_input_digest = chunk_embedding.embedding_input_digest
					AND embedding.protection_domain = chunk.protection_domain
					AND embedding.status = ?
					AND embedding.vector IS NOT NULL
			) AS ready_embedding_count,
			(
				SELECT COUNT(*)
				FROM ci_jobs AS pending_job
				WHERE pending_job.source_id = selected_view.source_id
					AND pending_job.checkout_id = selected_view.checkout_id
					AND pending_job.incarnation_id = selected_view.incarnation_id
					AND pending_job.profile_id = selected_view.profile_id
					AND pending_job.expected_parent_view_id = selected_view.view_id
					AND selected_view.view_state = ?
					AND selected_view.current_view_id = selected_view.view_id
					AND pending_job.state IN (?, ?, ?)
			) AS pending_job_count,
			relevant_job.state AS job_state,
			relevant_job.target_generation AS job_target_generation
		FROM selected_view
		LEFT JOIN LATERAL (
			SELECT
				job.state,
				job.target_generation
			FROM ci_jobs AS job
			WHERE job.source_id = selected_view.source_id
				AND job.checkout_id = selected_view.checkout_id
				AND job.incarnation_id = selected_view.incarnation_id
				AND job.profile_id = selected_view.profile_id
				AND (
					job.expected_parent_view_id = selected_view.view_id
					OR job.result_view_id = selected_view.view_id
				)
			ORDER BY
				CASE WHEN job.state IN (?, ?, ?) THEN 0 ELSE 1 END,
				job.updated_at DESC,
				job.job_id ASC
			LIMIT 1
		) AS relevant_job ON TRUE
	`,
		ref.ViewID,
		ref.SourceID,
		ref.CheckoutID,
		ref.AnalysisProfileID,
		ref.Generation,
		UCIViewPublished,
		UCIViewSuperseded,
		UCIFilePresent,
		UCIBlobStored,
		UCIParseArtifactComplete,
		UCIParseArtifactPartial,
		UCIEmbeddingReady,
		UCIViewPublished,
		UCIJobQueued,
		UCIJobRunning,
		UCIJobRetryScheduled,
		UCIJobQueued,
		UCIJobRunning,
		UCIJobRetryScheduled,
	).Scan(&row)
	if result.Error != nil {
		return uciIndexStatusRow{}, false, fmt.Errorf("uci projection load index status row: %w", result.Error)
	}
	return row, result.RowsAffected == 1, nil
}

func uciIndexStatusSnapshotFromRow(ref ucidomain.ContextRef, row uciIndexStatusRow) (ucidomain.IndexStatusSnapshot, error) {
	sourceState, err := uciIndexStatusSourceState(row.SourceState)
	if err != nil {
		return ucidomain.IndexStatusSnapshot{}, err
	}
	checkoutState, err := uciIndexStatusCheckoutState(row.CheckoutState)
	if err != nil {
		return ucidomain.IndexStatusSnapshot{}, err
	}
	viewState, err := uciIndexStatusViewState(row.ViewState)
	if err != nil {
		return ucidomain.IndexStatusSnapshot{}, err
	}
	currentRelation, err := uciIndexStatusCurrentViewRelation(ref.ViewID, row.CurrentViewID)
	if err != nil {
		return ucidomain.IndexStatusSnapshot{}, err
	}
	coverage, err := uciIndexStatusCoverage(row.CoverageJSON)
	if err != nil {
		return ucidomain.IndexStatusSnapshot{}, err
	}
	if row.PublishedAt == nil || row.PublishedAt.IsZero() || row.ObservedFSSeq < 0 || row.ScanStartedAt.IsZero() || row.ScanCompletedAt.IsZero() || row.ScanCompletedAt.Before(row.ScanStartedAt) {
		return ucidomain.IndexStatusSnapshot{}, fmt.Errorf("uci projection index status: stored view metadata is invalid")
	}
	if row.ChunkCount < 0 || row.ReadyEmbeddingCount < 0 || row.PendingJobCount < 0 {
		return ucidomain.IndexStatusSnapshot{}, fmt.Errorf("uci projection index status: scoped counts are invalid")
	}

	snapshot := ucidomain.IndexStatusSnapshot{
		Context:             ref,
		SourceState:         sourceState,
		CheckoutState:       checkoutState,
		ViewState:           viewState,
		CurrentViewRelation: currentRelation,
		Dirty:               row.Dirty,
		ObservedFSSeq:       row.ObservedFSSeq,
		Coverage:            coverage,
		PublishedAt:         row.PublishedAt.UTC(),
		ScanStartedAt:       row.ScanStartedAt.UTC(),
		ScanCompletedAt:     row.ScanCompletedAt.UTC(),
		ChunkCount:          uint64(row.ChunkCount),
		ReadyEmbeddingCount: uint64(row.ReadyEmbeddingCount),
		PendingJobCount:     uint64(row.PendingJobCount),
	}
	if row.JobState != nil {
		jobState, err := uciIndexStatusJobState(*row.JobState)
		if err != nil {
			return ucidomain.IndexStatusSnapshot{}, err
		}
		if row.JobTargetGeneration != nil && *row.JobTargetGeneration < 1 {
			return ucidomain.IndexStatusSnapshot{}, fmt.Errorf("uci projection index status: job target generation is invalid")
		}
		job := ucidomain.IndexStatusJob{State: jobState, TargetGeneration: cloneUCIOptionalInt64(row.JobTargetGeneration)}
		snapshot.RelevantJob = &job
	} else if row.JobTargetGeneration != nil {
		return ucidomain.IndexStatusSnapshot{}, fmt.Errorf("uci projection index status: job target has no job state")
	}
	snapshot.Freshness = uciIndexStatusFreshness(snapshot)
	if err := snapshot.Validate(); err != nil {
		return ucidomain.IndexStatusSnapshot{}, err
	}
	return snapshot, nil
}

func uciIndexStatusSourceState(state UCISourceState) (ucidomain.IndexStatusSourceState, error) {
	switch state {
	case UCISourceActive:
		return ucidomain.IndexStatusSourceActive, nil
	case UCISourceOffline:
		return ucidomain.IndexStatusSourceOffline, nil
	default:
		return "", fmt.Errorf("uci projection index status: source state %q is unavailable", state)
	}
}

func uciIndexStatusCheckoutState(state UCICheckoutState) (ucidomain.IndexStatusCheckoutState, error) {
	switch state {
	case UCICheckoutRegistered:
		return ucidomain.IndexStatusCheckoutRegistered, nil
	case UCICheckoutWatching:
		return ucidomain.IndexStatusCheckoutWatching, nil
	case UCICheckoutCatchingUp:
		return ucidomain.IndexStatusCheckoutCatchingUp, nil
	case UCICheckoutOffline:
		return ucidomain.IndexStatusCheckoutOffline, nil
	default:
		return "", fmt.Errorf("uci projection index status: checkout state %q is unavailable", state)
	}
}

func uciIndexStatusViewState(state UCIViewState) (ucidomain.IndexStatusViewState, error) {
	switch state {
	case UCIViewPublished:
		return ucidomain.IndexStatusViewPublished, nil
	case UCIViewSuperseded:
		return ucidomain.IndexStatusViewSuperseded, nil
	default:
		return "", fmt.Errorf("uci projection index status: view state %q is unavailable", state)
	}
}

func uciIndexStatusCurrentViewRelation(viewID string, currentViewID *string) (ucidomain.IndexStatusCurrentViewRelation, error) {
	if currentViewID == nil {
		return ucidomain.IndexStatusCurrentViewNone, nil
	}
	if err := validateUCIUUID("current_view_id", *currentViewID); err != nil {
		return "", err
	}
	if *currentViewID == viewID {
		return ucidomain.IndexStatusCurrentViewSelected, nil
	}
	return ucidomain.IndexStatusCurrentViewDifferent, nil
}

type uciIndexStatusCoverageWire struct {
	Structural           ucidomain.IndexCoverageState `json:"structural"`
	Lexical              ucidomain.IndexCoverageState `json:"lexical"`
	Vector               ucidomain.IndexCoverageState `json:"vector"`
	ExcludedFiles        uint64                       `json:"excluded_files"`
	UnreadableFiles      uint64                       `json:"unreadable_files"`
	UnresolvedReferences uint64                       `json:"unresolved_references"`
}

func uciIndexStatusCoverage(raw string) (ucidomain.IndexCoverage, error) {
	var wire uciIndexStatusCoverageWire
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return ucidomain.IndexCoverage{}, fmt.Errorf("uci projection index status: decode coverage: %w", err)
	}
	return ucidomain.IndexCoverage{
		Structural:           wire.Structural,
		Lexical:              wire.Lexical,
		Vector:               wire.Vector,
		ExcludedFiles:        wire.ExcludedFiles,
		UnreadableFiles:      wire.UnreadableFiles,
		UnresolvedReferences: wire.UnresolvedReferences,
	}, nil
}

func uciIndexStatusJobState(state string) (ucidomain.IndexStatusJobState, error) {
	switch UCIJobState(state) {
	case UCIJobQueued:
		return ucidomain.IndexStatusJobQueued, nil
	case UCIJobRunning:
		return ucidomain.IndexStatusJobRunning, nil
	case UCIJobRetryScheduled:
		return ucidomain.IndexStatusJobRetryScheduled, nil
	case UCIJobSucceeded:
		return ucidomain.IndexStatusJobSucceeded, nil
	case UCIJobFailedTerminal:
		return ucidomain.IndexStatusJobFailedTerminal, nil
	case UCIJobCancelled:
		return ucidomain.IndexStatusJobCancelled, nil
	case UCIJobObsolete:
		return ucidomain.IndexStatusJobObsolete, nil
	default:
		return "", fmt.Errorf("uci projection index status: job state %q is invalid", state)
	}
}

func uciIndexStatusFreshness(snapshot ucidomain.IndexStatusSnapshot) ucidomain.QueryFreshness {
	watermark := ucidomain.QueryEnrichmentWatermark{Sequence: snapshot.ObservedFSSeq}
	if snapshot.SourceState == ucidomain.IndexStatusSourceOffline || snapshot.CheckoutState == ucidomain.IndexStatusCheckoutOffline {
		watermark.State = ucidomain.QueryEnrichmentUnavailable
		return ucidomain.QueryFreshness{
			State:               ucidomain.QueryFreshnessOffline,
			Method:              ucidomain.QueryFreshnessNone,
			EnrichmentWatermark: watermark,
		}
	}
	if snapshot.ViewState == ucidomain.IndexStatusViewSuperseded {
		watermark.State = ucidomain.QueryEnrichmentCurrent
		return ucidomain.QueryFreshness{
			State:               ucidomain.QueryFreshnessHistorical,
			Method:              ucidomain.QueryFreshnessPinnedHistory,
			EnrichmentWatermark: watermark,
		}
	}
	if snapshot.CheckoutState == ucidomain.IndexStatusCheckoutCatchingUp || snapshot.PendingJobCount > 0 {
		watermark.State = ucidomain.QueryEnrichmentPending
		return ucidomain.QueryFreshness{
			State:               ucidomain.QueryFreshnessCatchingUp,
			Method:              ucidomain.QueryFreshnessWatchWatermark,
			EnrichmentWatermark: watermark,
		}
	}
	zero := int64(0)
	watermark.State = ucidomain.QueryEnrichmentCurrent
	return ucidomain.QueryFreshness{
		State:               ucidomain.QueryFreshnessObservedCurrent,
		Method:              ucidomain.QueryFreshnessWatchWatermark,
		PendingChanges:      &zero,
		EnrichmentWatermark: watermark,
	}
}

func cloneUCIOptionalInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
