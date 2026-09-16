package uci

import (
	"context"
	"fmt"
	"unicode/utf8"
)

const (
	// VersionedReadMaxBytes bounds one exact source slice before it reaches the
	// closed pre-exposure response boundary.
	VersionedReadMaxBytes = 8 << 10

	// VersionedReadWorkingCopyNotVerifiedWarning records a caller's request to
	// verify a working copy. The server does not have a working-copy read
	// capability, so the response remains exclusively bound to stored View bytes.
	VersionedReadWorkingCopyNotVerifiedWarning = "working_copy_not_verified_server_side"
)

// VersionedReadSpec names one already-discovered, immutable source slice. Its
// entity, span, and digest must all agree with the already-authorized View; it
// intentionally contains no locator, path hint, or current-disk capability.
type VersionedReadSpec struct {
	Entity            QueryEntityRef
	Span              QuerySpan
	ContentDigest     QueryContentDigest
	MaxBytes          int
	VerifyWorkingCopy bool
}

// Validate verifies that an exact read can remain bounded without changing its
// requested citation span.
func (spec VersionedReadSpec) Validate() error {
	if err := spec.Entity.Validate(); err != nil {
		return fmt.Errorf("uci versioned read: invalid entity: %w", err)
	}
	if err := spec.Span.Validate(); err != nil {
		return fmt.Errorf("uci versioned read: invalid span: %w", err)
	}
	if !validQueryContentDigest(spec.ContentDigest) {
		return fmt.Errorf("uci versioned read: content digest must be a bare lowercase SHA-256 digest")
	}
	if spec.MaxBytes < 1 || spec.MaxBytes > VersionedReadMaxBytes {
		return fmt.Errorf("uci versioned read: max bytes must be between 1 and %d", VersionedReadMaxBytes)
	}
	if spec.Span.ByteEnd-spec.Span.ByteStart > int64(spec.MaxBytes) {
		return fmt.Errorf("uci versioned read: exact span exceeds max bytes")
	}
	return nil
}

// VersionedReadStore reads one exact stored slice from an already-authorized
// immutable View. It must never derive bytes from a current checkout or disk.
type VersionedReadStore interface {
	ReadExact(context.Context, AuthorizedContext, VersionedReadSpec) (VersionedReadStoreResult, error)
}

// VersionedReadStoreResult is the closed store result vocabulary. A nil Hit is
// an authorized miss; Unavailable describes a selected View that cannot serve
// an exact read without disclosing a persistence error.
type VersionedReadStoreResult struct {
	Hit         *VersionedReadHit
	Coverage    IndexCoverageState
	Unavailable *QueryError
}

// VersionedReadHit is one fully materialized, bounded stored slice. The
// service independently checks every cited field before constructing a response.
type VersionedReadHit struct {
	Entity           QueryEntityRef
	Path             string
	Span             QuerySpan
	ContentDigest    QueryContentDigest
	Kind             QueryItemKind
	Language         string
	SourceByteLength int64
	Text             string
}

// VersionedReadService maps one exact persisted read to the existing closed
// QueryResponse boundary. Exposure recording belongs to the later MCP boundary.
type VersionedReadService struct {
	store VersionedReadStore
}

// NewVersionedReadService creates a service over one exact persisted-read port.
func NewVersionedReadService(store VersionedReadStore) *VersionedReadService {
	return &VersionedReadService{store: store}
}

// Read returns an exposure-free response bound to the supplied immutable
// authorization result. It never resolves authority again and never reads a
// local working copy, including when VerifyWorkingCopy is requested.
func (service *VersionedReadService) Read(ctx context.Context, authorized AuthorizedContext, spec VersionedReadSpec) (QueryResponse, error) {
	if service == nil || service.store == nil {
		return QueryResponse{}, fmt.Errorf("uci versioned read: store is not configured")
	}
	if ctx == nil {
		return QueryResponse{}, fmt.Errorf("uci versioned read: context is required")
	}
	if err := ctx.Err(); err != nil {
		return QueryResponse{}, err
	}

	ref := authorized.Ref()
	if !ref.valid() {
		return QueryResponse{}, fmt.Errorf("uci versioned read: authorized context is invalid")
	}
	if err := spec.Validate(); err != nil {
		return QueryResponse{}, err
	}

	selected, err := service.store.ReadExact(ctx, authorized, spec)
	if err != nil {
		return QueryResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return QueryResponse{}, err
	}
	if err := validateVersionedReadStoreResult(ref, spec, selected); err != nil {
		return QueryResponse{}, err
	}

	var response QueryResponse
	if selected.Unavailable != nil {
		response = queryUnavailableResponse(ref, selected.Coverage, *selected.Unavailable)
		warnings := versionedReadWarnings(spec)
		response.Warnings = &warnings
	} else {
		response = versionedReadAvailableResponse(ref, spec, selected)
	}
	if err := response.ValidatePreExposure(); err != nil {
		return QueryResponse{}, fmt.Errorf("uci versioned read: invalid pre-exposure response: %w", err)
	}
	return response, nil
}

func validateVersionedReadStoreResult(ref ContextRef, spec VersionedReadSpec, result VersionedReadStoreResult) error {
	if !validQueryCoverage(result.Coverage) {
		return fmt.Errorf("uci versioned read: store returned invalid coverage %q", result.Coverage)
	}
	if result.Unavailable != nil {
		if result.Hit != nil || result.Coverage != IndexCoverageUnavailable || result.Unavailable.Validate() != nil || !result.Unavailable.Code.isAuthorizedUnavailable() {
			return fmt.Errorf("uci versioned read: store returned invalid unavailable result")
		}
		return nil
	}
	if result.Coverage == IndexCoverageUnavailable {
		return fmt.Errorf("uci versioned read: unavailable coverage requires an unavailable result")
	}
	if result.Hit == nil {
		return nil
	}

	hit := result.Hit
	if hit.Entity != spec.Entity || hit.Span != spec.Span || hit.ContentDigest != spec.ContentDigest {
		return fmt.Errorf("uci versioned read: store returned a mismatched hit")
	}
	if hit.Entity.SourceID != ref.SourceID || hit.Entity.ViewID != ref.ViewID {
		return fmt.Errorf("uci versioned read: store returned a hit outside the authorized view")
	}
	if !queryBoundedText(hit.Path, 1, queryMaxPath) || !hit.Kind.valid() || !queryBoundedText(hit.Language, 1, queryMaxLanguage) {
		return fmt.Errorf("uci versioned read: store returned invalid hit metadata")
	}
	if hit.SourceByteLength != spec.Span.ByteEnd-spec.Span.ByteStart {
		return fmt.Errorf("uci versioned read: store returned text with a mismatched source byte length")
	}
	if !utf8.ValidString(hit.Text) {
		return fmt.Errorf("uci versioned read: store returned invalid text")
	}
	return nil
}

func versionedReadAvailableResponse(ref ContextRef, spec VersionedReadSpec, result VersionedReadStoreResult) QueryResponse {
	items := QueryItems{}
	if result.Hit != nil {
		hit := result.Hit
		items = append(items, QueryItem{
			Ref:           hit.Entity,
			Path:          hit.Path,
			Span:          hit.Span,
			ContentDigest: hit.ContentDigest,
			Kind:          hit.Kind,
			Language:      hit.Language,
			Excerpt:       hit.Text,
			MatchSources:  []QueryMatchSource{QueryMatchExact},
		})
	}

	zero := int64(0)
	truncated := false
	warnings := versionedReadWarnings(spec)
	response := QueryResponse{
		Schema:    QueryResponseSchema,
		Status:    QueryStatusOK,
		Contexts:  &QueryContexts{queryContextRef(ref)},
		Freshness: queryPinnedFreshness(ref.Generation),
		Retrieval: &QueryRetrieval{
			Mode:               QueryRetrievalExact,
			DegradationReasons: []string{},
		},
		Coverage: &QueryCoverage{
			Structural:       result.Coverage,
			UnresolvedSites:  &zero,
			UnsupportedFiles: &zero,
		},
		Items:        &items,
		Truncated:    &truncated,
		Warnings:     &warnings,
		Continuation: &QueryContinuation{},
	}
	if result.Coverage == IndexCoveragePartial {
		response.Status = QueryStatusPartial
	} else if len(items) == 0 {
		response.Status = QueryStatusEmpty
	}
	return response
}

func versionedReadWarnings(spec VersionedReadSpec) QueryWarnings {
	if !spec.VerifyWorkingCopy {
		return QueryWarnings{}
	}
	return QueryWarnings{VersionedReadWorkingCopyNotVerifiedWarning}
}
