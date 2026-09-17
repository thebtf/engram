package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	ucidomain "github.com/thebtf/engram/internal/uci"
)

// DescribeGraphSource returns an exact persisted source-read descriptor for one
// graph node only when the selected published View still has a bounded stored
// source slice. It never reads a checkout or substitutes a current file body.
func (s *UCIProjectionStore) DescribeGraphSource(ctx context.Context, authorized ucidomain.AuthorizedContext, entity ucidomain.QueryEntityRef) (ucidomain.VersionedReadSpec, bool, error) {
	if err := s.requireDB("describe graph source"); err != nil {
		return ucidomain.VersionedReadSpec{}, false, err
	}
	if ctx == nil {
		return ucidomain.VersionedReadSpec{}, false, fmt.Errorf("uci graph source descriptor: context is required")
	}
	if err := ctx.Err(); err != nil {
		return ucidomain.VersionedReadSpec{}, false, err
	}
	ref := authorized.Ref()
	if err := validateUCIQueryContext(ref); err != nil {
		return ucidomain.VersionedReadSpec{}, false, err
	}
	if entity.SourceID != ref.SourceID || entity.ViewID != ref.ViewID || entity.Validate() != nil {
		return ucidomain.VersionedReadSpec{}, false, nil
	}

	coverage, available, err := s.loadUCIGraphCoverage(ctx, ref)
	if err != nil {
		return ucidomain.VersionedReadSpec{}, false, err
	}
	if !available || coverage == ucidomain.IndexCoverageUnavailable {
		return ucidomain.VersionedReadSpec{}, false, nil
	}

	var row uciGraphSourceDescriptorRow
	result := s.db.WithContext(ctx).Raw(browserCodeGraphSourceDescriptorSQL(),
		ref.ViewID,
		ref.SourceID,
		ref.CheckoutID,
		ref.AnalysisProfileID,
		ref.Generation,
		UCIViewPublished,
		UCIViewSuperseded,
		UCIBlobStored,
		UCIFilePresent,
		UCIParseArtifactComplete,
		UCIParseArtifactPartial,
		entity.EntityKey,
		ucidomain.VersionedReadMaxBytes,
	).Scan(&row)
	if result.Error != nil {
		return ucidomain.VersionedReadSpec{}, false, fmt.Errorf("uci graph source descriptor: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ucidomain.VersionedReadSpec{}, false, nil
	}
	descriptor, ok := row.spec(ref)
	if !ok {
		return ucidomain.VersionedReadSpec{}, false, nil
	}
	return descriptor, true, nil
}

// DescribeGraphEvidence resolves one released relation evidence reference in
// the selected immutable View. Entity and partial evidence deliberately retain
// the existing bounded entity descriptor; unsupported precision releases none.
func (s *UCIProjectionStore) DescribeGraphEvidence(ctx context.Context, authorized ucidomain.AuthorizedContext, evidence ucidomain.QueryRelationEvidence) (ucidomain.VersionedReadSpec, bool, error) {
	ref := authorized.Ref()
	if err := validateUCIQueryContext(ref); err != nil {
		return ucidomain.VersionedReadSpec{}, false, err
	}
	if evidence.Ref.SourceID != ref.SourceID || evidence.Ref.ViewID != ref.ViewID || evidence.Ref.Validate() != nil {
		return ucidomain.VersionedReadSpec{}, false, nil
	}
	switch evidence.Precision {
	case ucidomain.QueryEvidencePrecisionEntity, ucidomain.QueryEvidencePrecisionPartial:
		if evidence.ReferenceSiteID != nil {
			return ucidomain.VersionedReadSpec{}, false, nil
		}
		return s.DescribeGraphSource(ctx, authorized, evidence.Ref)
	case ucidomain.QueryEvidencePrecisionUnsupported:
		return ucidomain.VersionedReadSpec{}, false, nil
	case ucidomain.QueryEvidencePrecisionReferenceSite:
		if evidence.ReferenceSiteID == nil || validateUCIUUID("reference_site_id", *evidence.ReferenceSiteID) != nil {
			return ucidomain.VersionedReadSpec{}, false, nil
		}
	default:
		return ucidomain.VersionedReadSpec{}, false, nil
	}
	if err := s.requireDB("describe graph evidence"); err != nil {
		return ucidomain.VersionedReadSpec{}, false, err
	}
	if ctx == nil {
		return ucidomain.VersionedReadSpec{}, false, fmt.Errorf("uci graph evidence descriptor: context is required")
	}
	if err := ctx.Err(); err != nil {
		return ucidomain.VersionedReadSpec{}, false, err
	}
	coverage, available, err := s.loadUCIGraphCoverage(ctx, ref)
	if err != nil {
		return ucidomain.VersionedReadSpec{}, false, err
	}
	if !available || coverage == ucidomain.IndexCoverageUnavailable {
		return ucidomain.VersionedReadSpec{}, false, nil
	}

	var row uciGraphReferenceDescriptorRow
	result := s.db.WithContext(ctx).Raw(browserCodeGraphReferenceDescriptorSQL(),
		ref.ViewID,
		ref.SourceID,
		ref.CheckoutID,
		ref.AnalysisProfileID,
		ref.Generation,
		UCIViewPublished,
		UCIViewSuperseded,
		*evidence.ReferenceSiteID,
		UCIBlobStored,
		UCIFilePresent,
		UCIParseArtifactComplete,
		UCIParseArtifactPartial,
		evidence.Ref.EntityKey,
	).Scan(&row)
	if result.Error != nil {
		return ucidomain.VersionedReadSpec{}, false, fmt.Errorf("uci graph evidence descriptor: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return ucidomain.VersionedReadSpec{}, false, nil
	}
	descriptor, ok := row.spec(ref, *evidence.ReferenceSiteID)
	if !ok {
		return ucidomain.VersionedReadSpec{}, false, nil
	}
	return descriptor, true, nil
}

type uciGraphReferenceDescriptorRow struct {
	EntityKey       string `gorm:"column:entity_key"`
	ContentDigest   string `gorm:"column:content_digest"`
	ReferenceSiteID string `gorm:"column:reference_site_id"`
	ReferenceSpan   string `gorm:"column:reference_span"`
}

func (row uciGraphReferenceDescriptorRow) spec(ref ucidomain.ContextRef, referenceSiteID string) (ucidomain.VersionedReadSpec, bool) {
	if row.ReferenceSiteID != referenceSiteID || validateUCIUUID("reference_site_id", row.ReferenceSiteID) != nil {
		return ucidomain.VersionedReadSpec{}, false
	}
	var span ucidomain.IndexSpan
	if json.Unmarshal([]byte(row.ReferenceSpan), &span) != nil {
		return ucidomain.VersionedReadSpec{}, false
	}
	digest := strings.TrimPrefix(row.ContentDigest, "sha256:")
	copy := row.ReferenceSiteID
	descriptor := ucidomain.VersionedReadSpec{
		Entity:          ucidomain.QueryEntityRef{SourceID: ref.SourceID, ViewID: ref.ViewID, EntityKey: row.EntityKey},
		Span:            ucidomain.QuerySpan{ByteStart: span.ByteStart, ByteEnd: span.ByteEnd, LineStart: int64(span.LineStart), LineEnd: int64(span.LineEnd)},
		ContentDigest:   ucidomain.QueryContentDigest(digest),
		MaxBytes:        int(span.ByteEnd - span.ByteStart),
		ReferenceSiteID: &copy,
	}
	return descriptor, descriptor.Validate() == nil
}

type uciGraphSourceDescriptorRow struct {
	EntityKey     string `gorm:"column:entity_key"`
	ByteStart     int64  `gorm:"column:byte_start"`
	ByteEnd       int64  `gorm:"column:byte_end"`
	LineStart     int64  `gorm:"column:line_start"`
	LineEnd       int64  `gorm:"column:line_end"`
	ContentDigest string `gorm:"column:content_digest"`
}

func (row uciGraphSourceDescriptorRow) spec(ref ucidomain.ContextRef) (ucidomain.VersionedReadSpec, bool) {
	digest := strings.TrimPrefix(row.ContentDigest, "sha256:")
	descriptor := ucidomain.VersionedReadSpec{
		Entity: ucidomain.QueryEntityRef{
			SourceID:  ref.SourceID,
			ViewID:    ref.ViewID,
			EntityKey: row.EntityKey,
		},
		Span: ucidomain.QuerySpan{
			ByteStart: row.ByteStart,
			ByteEnd:   row.ByteEnd,
			LineStart: row.LineStart,
			LineEnd:   row.LineEnd,
		},
		ContentDigest: ucidomain.QueryContentDigest(digest),
		MaxBytes:      int(row.ByteEnd - row.ByteStart),
	}
	return descriptor, descriptor.Validate() == nil
}

func browserCodeGraphSourceDescriptorSQL() string {
	return `
		WITH selected_view AS (
			SELECT
				view_row.source_id,
				view_row.checkout_id,
				view_row.generation,
				profile.parser_bundle_digest
			FROM ci_views AS view_row
			JOIN ci_profiles AS profile ON profile.profile_id = view_row.profile_id
			WHERE view_row.view_id = ?
				AND view_row.source_id = ?
				AND view_row.checkout_id = ?
				AND view_row.profile_id = ?
				AND view_row.generation = ?
				AND view_row.state IN (?, ?)
		)
		SELECT
			COALESCE(NULLIF(definition.qualified_local_name, ''), NULLIF(chunk.symbol_key, ''), membership.path_key || ':' || chunk.ordinal::text) AS entity_key,
			chunk.byte_start,
			chunk.byte_end,
			array_length(regexp_split_to_array(
				convert_from(substring(blob.safe_content FROM 1 FOR chunk.byte_start::integer), replace(upper(blob.encoding), '-', '')),
				E'\n'
			), 1) AS line_start,
			array_length(regexp_split_to_array(
				convert_from(substring(blob.safe_content FROM 1 FOR chunk.byte_end::integer), replace(upper(blob.encoding), '-', '')),
				E'\n'
			), 1) AS line_end,
			blob.content_digest
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
		LEFT JOIN ci_definitions AS definition
			ON definition.artifact_id = chunk.artifact_id
			AND definition.local_symbol_key = chunk.symbol_key
		WHERE blob.storage_state = ?
			AND blob.safe_content IS NOT NULL
			AND membership.file_state = ?
			AND artifact.status IN (?, ?)
			AND artifact.sealed_at IS NOT NULL
			AND artifact.facts_digest IS NOT NULL
			AND COALESCE(NULLIF(definition.qualified_local_name, ''), NULLIF(chunk.symbol_key, ''), membership.path_key) = ?
			AND chunk.byte_end - chunk.byte_start BETWEEN 1 AND ?
		ORDER BY membership.display_path ASC, chunk.byte_start ASC, chunk.chunk_id ASC
		LIMIT 1`
}

func browserCodeGraphReferenceDescriptorSQL() string {
	return `
		WITH selected_view AS (
			SELECT
				view_row.source_id,
				view_row.checkout_id,
				view_row.generation,
				profile.parser_bundle_digest
			FROM ci_views AS view_row
			JOIN ci_profiles AS profile ON profile.profile_id = view_row.profile_id
			WHERE view_row.view_id = ?
				AND view_row.source_id = ?
				AND view_row.checkout_id = ?
				AND view_row.profile_id = ?
				AND view_row.generation = ?
				AND view_row.state IN (?, ?)
		)
		SELECT
			COALESCE(NULLIF(definition.qualified_local_name, ''), NULLIF(edge.source_symbol, ''), membership.path_key) AS entity_key,
			blob.content_digest,
			reference.reference_site_id,
			reference.syntax_span AS reference_span
		FROM selected_view AS view_row
		JOIN ci_resolved_edges AS edge
			ON edge.source_id = view_row.source_id
			AND edge.checkout_id = view_row.checkout_id
			AND edge.valid_from_generation <= view_row.generation
			AND (edge.valid_to_generation IS NULL OR edge.valid_to_generation > view_row.generation)
		JOIN ci_memberships AS membership
			ON membership.source_id = view_row.source_id
			AND membership.checkout_id = view_row.checkout_id
			AND membership.path_key = edge.source_path
			AND membership.artifact_id = edge.source_artifact
			AND membership.valid_from_generation <= view_row.generation
			AND (membership.valid_to_generation IS NULL OR membership.valid_to_generation > view_row.generation)
		JOIN ci_parse_artifacts AS artifact
			ON artifact.source_id = membership.source_id
			AND artifact.artifact_id = membership.artifact_id
			AND artifact.extraction_profile_digest = view_row.parser_bundle_digest
		JOIN ci_blobs AS blob
			ON blob.source_id = artifact.source_id
			AND blob.blob_id = artifact.blob_id
		JOIN ci_reference_sites AS reference
			ON reference.artifact_id = artifact.artifact_id
			AND reference.reference_site_id = ?
			AND edge.evidence_json->>'ReferenceSiteID' = reference.reference_site_id::text
		LEFT JOIN ci_definitions AS definition
			ON definition.artifact_id = artifact.artifact_id
			AND definition.local_symbol_key = edge.source_symbol
		WHERE blob.storage_state = ?
			AND blob.safe_content IS NOT NULL
			AND membership.file_state = ?
			AND artifact.status IN (?, ?)
			AND artifact.sealed_at IS NOT NULL
			AND artifact.facts_digest IS NOT NULL
			AND COALESCE(NULLIF(definition.qualified_local_name, ''), NULLIF(edge.source_symbol, ''), membership.path_key) = ?
		ORDER BY edge.resolved_edge_id ASC
		LIMIT 1`
}
