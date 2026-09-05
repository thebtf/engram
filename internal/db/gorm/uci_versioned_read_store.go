package gorm

import (
	"context"
	"fmt"

	ucidomain "github.com/thebtf/engram/internal/uci"
)

var _ ucidomain.VersionedReadStore = (*UCIProjectionStore)(nil)

// ReadExact reads one bounded source slice only after binding it to the exact
// authorized View. The SQL's selected_view CTE is deliberately the first
// candidate relation; it prevents a current checkout or another View from
// contributing a body before the exact entity, span, and digest predicates hold.
func (s *UCIProjectionStore) ReadExact(ctx context.Context, authorized ucidomain.AuthorizedContext, spec ucidomain.VersionedReadSpec) (ucidomain.VersionedReadStoreResult, error) {
	if err := s.requireDB("read exact versioned content"); err != nil {
		return ucidomain.VersionedReadStoreResult{}, err
	}
	if ctx == nil {
		return ucidomain.VersionedReadStoreResult{}, fmt.Errorf("uci versioned read: context is required")
	}

	ref := authorized.Ref()
	if err := validateUCIQueryContext(ref); err != nil {
		return ucidomain.VersionedReadStoreResult{}, err
	}
	if err := spec.Validate(); err != nil {
		return ucidomain.VersionedReadStoreResult{}, err
	}

	metadata, found, err := s.loadUCIQueryViewMetadata(ctx, ref)
	if err != nil {
		return ucidomain.VersionedReadStoreResult{}, err
	}
	if !found || (metadata.State != UCIViewPublished && metadata.State != UCIViewSuperseded) {
		return uciVersionedReadUnavailableResult(ucidomain.QueryErrorBuildIncomplete), nil
	}
	coverage, ok := parseUCIQueryCoverage(metadata.Structural)
	if !ok || coverage == ucidomain.IndexCoverageUnavailable {
		return uciVersionedReadUnavailableResult(ucidomain.QueryErrorBuildIncomplete), nil
	}

	query, arguments := buildUCIVersionedReadSQL(ref, spec)
	var row uciVersionedReadRow
	result := s.db.WithContext(ctx).Raw(query, arguments...).Scan(&row)
	if result.Error != nil {
		return ucidomain.VersionedReadStoreResult{}, fmt.Errorf("uci versioned read: select exact content: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return ucidomain.VersionedReadStoreResult{Coverage: coverage}, nil
	}

	hit, ok := row.hit(spec)
	if !ok {
		return ucidomain.VersionedReadStoreResult{Coverage: coverage}, nil
	}
	return ucidomain.VersionedReadStoreResult{Hit: &hit, Coverage: coverage}, nil
}

type uciVersionedReadRow struct {
	EntityKey        string `gorm:"column:entity_key"`
	RelativePath     string `gorm:"column:relative_path"`
	ByteStart        int64  `gorm:"column:byte_start"`
	ByteEnd          int64  `gorm:"column:byte_end"`
	LineStart        int64  `gorm:"column:line_start"`
	LineEnd          int64  `gorm:"column:line_end"`
	ContentDigest    string `gorm:"column:content_digest"`
	ChunkKind        string `gorm:"column:chunk_kind"`
	Language         string `gorm:"column:language"`
	SourceByteLength int64  `gorm:"column:source_byte_length"`
	Text             string `gorm:"column:text"`
}

func (row uciVersionedReadRow) hit(spec ucidomain.VersionedReadSpec) (ucidomain.VersionedReadHit, bool) {
	if row.EntityKey != spec.Entity.EntityKey ||
		row.ByteStart != spec.Span.ByteStart || row.ByteEnd != spec.Span.ByteEnd ||
		row.LineStart != spec.Span.LineStart || row.LineEnd != spec.Span.LineEnd ||
		row.ContentDigest != "sha256:"+string(spec.ContentDigest) ||
		row.SourceByteLength != spec.Span.ByteEnd-spec.Span.ByteStart {
		return ucidomain.VersionedReadHit{}, false
	}
	return ucidomain.VersionedReadHit{
		Entity:           spec.Entity,
		Path:             row.RelativePath,
		Span:             spec.Span,
		ContentDigest:    spec.ContentDigest,
		Kind:             uciQueryItemKind(row.ChunkKind),
		Language:         row.Language,
		SourceByteLength: row.SourceByteLength,
		Text:             row.Text,
	}, true
}

func uciVersionedReadUnavailableResult(code ucidomain.QueryErrorCode) ucidomain.VersionedReadStoreResult {
	return ucidomain.VersionedReadStoreResult{
		Coverage:    ucidomain.IndexCoverageUnavailable,
		Unavailable: &ucidomain.QueryError{Code: code},
	}
}

func buildUCIVersionedReadSQL(ref ucidomain.ContextRef, spec ucidomain.VersionedReadSpec) (string, []any) {
	arguments := []any{
		ref.ViewID,
		ref.SourceID,
		ref.CheckoutID,
		ref.AnalysisProfileID,
		ref.Generation,
		spec.Entity.ViewID,
		spec.Entity.SourceID,
		UCIViewPublished,
		UCIViewSuperseded,
		UCIBlobStored,
		UCIFilePresent,
		UCIParseArtifactComplete,
		UCIParseArtifactPartial,
		"sha256:" + string(spec.ContentDigest),
		spec.Span.ByteEnd,
		spec.Span.ByteStart,
		spec.Span.ByteEnd,
		spec.Entity.EntityKey,
		spec.Span.ByteEnd - spec.Span.ByteStart,
		spec.Span.LineStart,
		spec.Span.LineEnd,
	}
	return `
		WITH selected_view AS (
			SELECT
				view_row.view_id,
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
				AND view_row.view_id = ?
				AND view_row.source_id = ?
				AND view_row.state IN (?, ?)
		),
		candidate AS (
			SELECT
				blob.blob_id,
				blob.source_id,
				membership.display_path AS relative_path,
				COALESCE(NULLIF(definition.qualified_local_name, ''), NULLIF(chunk.symbol_key, ''), membership.path_key || ':' || chunk.ordinal::text) AS entity_key,
				chunk.byte_start,
				chunk.byte_end,
				blob.content_digest,
				chunk.chunk_kind,
				artifact.language
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
				AND blob.content_digest = ?
				AND blob.byte_length >= ?
				AND chunk.byte_start = ?
				AND chunk.byte_end = ?
				AND COALESCE(NULLIF(definition.qualified_local_name, ''), NULLIF(chunk.symbol_key, ''), membership.path_key || ':' || chunk.ordinal::text) = ?
		),
		bounded_bytes AS (
			SELECT
				candidate.entity_key,
				candidate.relative_path,
				candidate.byte_start,
				candidate.byte_end,
				candidate.content_digest,
				candidate.chunk_kind,
				candidate.language,
				blob.encoding,
				substring(blob.safe_content FROM (candidate.byte_start + 1)::integer FOR ?::integer) AS content,
				array_length(regexp_split_to_array(
					convert_from(substring(blob.safe_content FROM 1 FOR candidate.byte_start::integer), replace(upper(blob.encoding), '-', '')),
					E'\n'
				), 1) AS line_start,
				array_length(regexp_split_to_array(
					convert_from(substring(blob.safe_content FROM 1 FOR candidate.byte_end::integer), replace(upper(blob.encoding), '-', '')),
					E'\n'
				), 1) AS line_end
			FROM candidate
			JOIN ci_blobs AS blob
				ON blob.blob_id = candidate.blob_id
				AND blob.source_id = candidate.source_id
		)
		SELECT
			entity_key,
			relative_path,
			byte_start,
			byte_end,
			line_start,
			line_end,
			content_digest,
			chunk_kind,
			language,
			octet_length(content) AS source_byte_length,
			convert_from(content, replace(upper(encoding), '-', '')) AS text
		FROM bounded_bytes
		WHERE line_start = ?
			AND line_end = ?
		LIMIT 1`, arguments
}
