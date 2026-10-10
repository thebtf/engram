package gorm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
)

func TestUCIVersionedReadStoreReadsBoundedHistoricalBytesAndIsolatesCurrentView(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()

	oldArtifact := fixture.insertArtifact(t, fixture.source.SourceID, "versioned-read-shared", `func VersionedReadShared() string { return "old" } // trailing-secret-must-not-leak
`, UCIParseArtifactComplete)
	oldStart, oldEnd, oldText := uciVersionedReadAddBoundedChunk(t, fixture, &oldArtifact, `return "old"`)
	oldArtifact.Proof = uciVersionedReadDescribeArtifact(t, fixture, oldArtifact)
	oldPublished := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "versioned-read-v1", path: "shared/versioned_read.go", checkout: fixture.checkout, kind: ucidomain.IndexJobInitial, artifact: oldArtifact})
	oldAuthorized := uciSemanticAuthorize(t, fixture, oldPublished.Context)
	oldSpec := uciVersionedReadSpecForPathAndSpan(t, fixture, oldAuthorized, "shared/versioned_read.go", oldStart, oldEnd)
	oldSpec.MaxBytes += len(` } // trailing-secret-must-not-leak`)
	require.Greater(t, oldSpec.MaxBytes, int(oldEnd-oldStart), "regression requires a response bound larger than the exact span")

	oldResult, err := fixture.projection.ReadExact(ctx, oldAuthorized, oldSpec)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexCoverageComplete, oldResult.Coverage)
	require.NotNil(t, oldResult.Hit)
	require.Equal(t, oldText, oldResult.Hit.Text)
	require.Equal(t, int64(len(oldText)), oldResult.Hit.SourceByteLength)
	require.Equal(t, oldSpec.Entity, oldResult.Hit.Entity)
	require.Equal(t, oldSpec.Span, oldResult.Hit.Span)
	require.Equal(t, oldSpec.ContentDigest, oldResult.Hit.ContentDigest)

	currentArtifact := fixture.insertArtifact(t, fixture.source.SourceID, "versioned-read-shared", `func VersionedReadShared() string { return "new" }
`, UCIParseArtifactComplete)
	currentStart, currentEnd, currentText := uciVersionedReadAddBoundedChunk(t, fixture, &currentArtifact, `return "new"`)
	currentArtifact.Proof = uciVersionedReadDescribeArtifact(t, fixture, currentArtifact)
	currentPublished := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "versioned-read-v2", path: "shared/versioned_read.go", checkout: fixture.checkout, parent: uciPublicationParent(oldPublished), kind: ucidomain.IndexJobReconcile, artifact: currentArtifact})
	currentAuthorized := uciSemanticAuthorize(t, fixture, currentPublished.Context)
	currentSpec := uciVersionedReadSpecForPathAndSpan(t, fixture, currentAuthorized, "shared/versioned_read.go", currentStart, currentEnd)

	oldResult, err = fixture.projection.ReadExact(ctx, oldAuthorized, oldSpec)
	require.NoError(t, err)
	require.NotNil(t, oldResult.Hit)
	require.Equal(t, oldText, oldResult.Hit.Text, "superseded Views must retain their stored historical bytes")

	currentResult, err := fixture.projection.ReadExact(ctx, currentAuthorized, currentSpec)
	require.NoError(t, err)
	require.NotNil(t, currentResult.Hit)
	require.Equal(t, currentText, currentResult.Hit.Text)
	require.NotEqual(t, oldResult.Hit.Text, currentResult.Hit.Text)

	foreignResult, err := fixture.projection.ReadExact(ctx, currentAuthorized, oldSpec)
	require.NoError(t, err)
	require.Nil(t, foreignResult.Hit, "an old View citation must not materialize through the current View")
	require.Nil(t, foreignResult.Unavailable)
	require.Equal(t, ucidomain.IndexCoverageComplete, foreignResult.Coverage)
}

func TestUCIVersionedReadStoreReadsBoundedSubspanOfLargeIndexedChunk(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	lead := "func Large() {"
	body := lead + strings.Repeat("x", 8191-len(lead)) + "€\n" + strings.Repeat("z", 65536-8195)
	artifact := fixture.insertArtifact(t, fixture.source.SourceID, "large-indexed-source", body, UCIParseArtifactComplete)
	start, end, _ := uciVersionedReadAddBoundedChunk(t, fixture, &artifact, body)
	artifact.Proof = uciVersionedReadDescribeArtifact(t, fixture, artifact)
	published := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "large-indexed-source", path: "large/source.go", checkout: fixture.checkout, kind: ucidomain.IndexJobInitial, artifact: artifact})
	authorized := uciSemanticAuthorize(t, fixture, published.Context)
	spec := uciVersionedReadSpecForPathAndSpan(t, fixture, authorized, "large/source.go", start, end)
	indexed := spec.Span
	spec.IndexedSpan = &indexed
	spec.Span.ByteEnd = start + 8192
	spec.MaxBytes = 8192
	response, err := ucidomain.NewVersionedReadService(fixture.projection).Read(context.Background(), authorized, spec)
	require.NoError(t, err)
	require.NoError(t, response.ValidatePreExposure())
	require.Len(t, *response.Items, 1)
	item := (*response.Items)[0]
	require.Equal(t, body[:8191], item.Excerpt)
	require.Equal(t, start+8191, item.Span.ByteEnd)
	require.Contains(t, []string(*response.Warnings), "source_partial_indexed_chunk")
	interior := spec
	interior.Span.ByteStart = start + 8195
	interior.Span.ByteEnd = interior.Span.ByteStart + 8192
	continued, err := fixture.projection.ReadExact(context.Background(), authorized, interior)
	require.NoError(t, err)
	require.NotNil(t, continued.Hit)
	require.Equal(t, strings.Repeat("z", 8192), continued.Hit.Text)
	require.Equal(t, ucidomain.QuerySpan{ByteStart: interior.Span.ByteStart, ByteEnd: interior.Span.ByteEnd, LineStart: indexed.LineStart + 1, LineEnd: indexed.LineStart + 1}, continued.Hit.Span)

	outside := spec
	outside.Span.ByteStart = end - 4
	outside.Span.ByteEnd = end + 4
	require.Error(t, outside.Validate())
	_, err = fixture.projection.ReadExact(context.Background(), authorized, outside)
	require.Error(t, err)

	for _, mutation := range []func(*ucidomain.VersionedReadSpec){
		func(value *ucidomain.VersionedReadSpec) { value.IndexedSpan.ByteEnd-- },
		func(value *ucidomain.VersionedReadSpec) {
			value.ContentDigest = ucidomain.QueryContentDigest(strings.Repeat("0", 64))
		},
	} {
		invalid := spec
		bound := indexed
		invalid.IndexedSpan = &bound
		mutation(&invalid)
		result, readErr := fixture.projection.ReadExact(context.Background(), authorized, invalid)
		require.NoError(t, readErr)
		require.Nil(t, result.Hit, "a mismatched indexed span or digest cannot disclose bytes")
	}
}

func TestUCIVersionedReadStoreReturnsEmptyForStaleAndCrossScopedEvidence(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "versioned-read-refusal", `func VersionedReadRefusal() string { return "stored-only" }
`, UCIParseArtifactComplete)
	published := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "versioned-read-refusal-v1", path: "refusal/versioned_read.go", checkout: fixture.checkout, kind: ucidomain.IndexJobInitial, artifact: artifact})
	authorized := uciSemanticAuthorize(t, fixture, published.Context)
	spec := uciVersionedReadSpecForPathAndSpan(t, fixture, authorized, "refusal/versioned_read.go", 0, int64(len(artifact.Body)))
	service := ucidomain.NewVersionedReadService(fixture.projection)

	for _, test := range []struct {
		name   string
		mutate func(*ucidomain.VersionedReadSpec)
	}{
		{
			name: "stale digest",
			mutate: func(candidate *ucidomain.VersionedReadSpec) {
				candidate.ContentDigest = ucidomain.QueryContentDigest(strings.Repeat("0", 64))
			},
		},
		{
			name: "cross source",
			mutate: func(candidate *ucidomain.VersionedReadSpec) {
				candidate.Entity.SourceID = fixture.foreign.SourceID
			},
		},
		{
			name: "cross view",
			mutate: func(candidate *ucidomain.VersionedReadSpec) {
				candidate.Entity.ViewID = fixture.sibling.CheckoutID
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := spec
			test.mutate(&candidate)
			result, err := fixture.projection.ReadExact(context.Background(), authorized, candidate)
			require.NoError(t, err)
			require.Nil(t, result.Hit, "stale or foreign evidence must be an authorized empty result before body materialization")
			require.Nil(t, result.Unavailable)
			require.Equal(t, ucidomain.IndexCoverageComplete, result.Coverage)
			response, err := service.Read(context.Background(), authorized, candidate)
			require.NoError(t, err)
			require.NoError(t, response.ValidatePreExposure())
			require.Equal(t, ucidomain.QueryStatusEmpty, response.Status)
			require.Nil(t, response.Exposure)
			require.NotNil(t, response.Items)
			require.Empty(t, *response.Items)
		})
	}
}

func TestUCIVersionedReadStoreReportsPartialAndUnavailableCoverage(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "versioned-read-coverage", `func VersionedReadCoverage() string { return "coverage" }
`, UCIParseArtifactComplete)
	published := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "versioned-read-coverage-v1", path: "coverage/versioned_read.go", checkout: fixture.checkout, kind: ucidomain.IndexJobInitial, artifact: artifact})
	authorized := uciSemanticAuthorize(t, fixture, published.Context)
	spec := uciVersionedReadSpecForPathAndSpan(t, fixture, authorized, "coverage/versioned_read.go", 0, int64(len(artifact.Body)))

	require.NoError(t, fixture.db.Exec(`UPDATE ci_views SET coverage_json = ? WHERE view_id = ?`, `{"structural":"partial","lexical":"complete","vector":"unavailable"}`, published.Context.ViewID).Error)
	partial, err := fixture.projection.ReadExact(context.Background(), authorized, spec)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexCoveragePartial, partial.Coverage)
	require.NotNil(t, partial.Hit)
	require.Nil(t, partial.Unavailable)

	require.NoError(t, fixture.db.Exec(`UPDATE ci_views SET coverage_json = ? WHERE view_id = ?`, `{"structural":"unavailable","lexical":"unavailable","vector":"unavailable"}`, published.Context.ViewID).Error)
	unavailable, err := fixture.projection.ReadExact(context.Background(), authorized, spec)
	require.NoError(t, err)
	require.Equal(t, ucidomain.IndexCoverageUnavailable, unavailable.Coverage)
	require.Nil(t, unavailable.Hit)
	require.Equal(t, &ucidomain.QueryError{Code: ucidomain.QueryErrorBuildIncomplete}, unavailable.Unavailable)
}

func TestUCIVersionedReadStoreNeverFallsBackWithoutStoredBytes(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "versioned-read-no-disk", `func VersionedReadNoDisk() string { return "persisted" }
`, UCIParseArtifactComplete)
	published := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "versioned-read-no-disk-v1", path: "no_disk/versioned_read.go", checkout: fixture.checkout, kind: ucidomain.IndexJobInitial, artifact: artifact})
	authorized := uciSemanticAuthorize(t, fixture, published.Context)
	spec := uciVersionedReadSpecForPathAndSpan(t, fixture, authorized, "no_disk/versioned_read.go", 0, int64(len(artifact.Body)))

	require.NoError(t, fixture.db.Exec(`UPDATE ci_blobs SET storage_state = ?, safe_content = NULL WHERE blob_id = ?`, UCIBlobMetadataOnly, artifact.Blob.BlobID).Error)
	result, err := fixture.projection.ReadExact(context.Background(), authorized, spec)
	require.NoError(t, err)
	require.Nil(t, result.Hit, "a missing stored body is not replaced with any current working-copy bytes")
	require.Nil(t, result.Unavailable)
	require.Equal(t, ucidomain.IndexCoverageComplete, result.Coverage)
}

type uciVersionedReadPublishInput struct {
	key, path string
	checkout  *UCICheckout
	parent    *ucidomain.ContextRef
	kind      ucidomain.IndexJobKind
	artifact  uciPublicationArtifact
}

func uciVersionedReadPublish(t *testing.T, fixture *uciPublicationFixture, input uciVersionedReadPublishInput) ucidomain.IndexPublishedView {
	t.Helper()
	membership := uciPublicationPresentMembership(input.path, input.artifact)
	replacement := ucidomain.IndexEdgeReplacement{SourcePath: membership.PathKey}
	draft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{input.artifact}, []ucidomain.IndexMembership{membership}, nil, []ucidomain.IndexEdgeReplacement{replacement})},
		[]ucidomain.IndexMembership{membership},
		[]ucidomain.IndexEdgeReplacement{replacement},
	)
	_, published := fixture.publish(t, fixture.publisher, fixture.caller("versioned-read-"+input.key), fixture.publishInput(input.key, input.checkout, fixture.profile.ProfileID, input.parent, ucidomain.IndexManifestFull, input.kind, draft))
	return published
}

func uciVersionedReadDescribeArtifact(t *testing.T, fixture *uciPublicationFixture, artifact uciPublicationArtifact) ucidomain.IndexArtifactProof {
	t.Helper()
	proof, err := fixture.projection.DescribeIndexArtifact(context.Background(), fixture.source.SourceID, artifact.Artifact.ArtifactID)
	require.NoError(t, err)
	require.Equal(t, artifact.Artifact.ArtifactID, proof.ArtifactID)
	require.Equal(t, ucidomain.IndexDigest(artifact.Blob.ContentDigest), proof.ContentDigest)
	return proof
}

func uciVersionedReadAddBoundedChunk(t *testing.T, fixture *uciPublicationFixture, artifact *uciPublicationArtifact, text string) (int64, int64, string) {
	t.Helper()
	start := int64(strings.Index(string(artifact.Body), text))
	require.GreaterOrEqual(t, start, int64(0))
	end := start + int64(len(text))
	symbolKey := artifact.Definition.LocalSymbolKey
	_, err := fixture.projection.UpsertChunk(context.Background(), UpsertUCIChunkInput{
		SourceID:      artifact.Artifact.SourceID,
		ArtifactID:    artifact.Artifact.ArtifactID,
		SymbolKey:     &symbolKey,
		ChunkKind:     "statement",
		Ordinal:       1,
		ByteStart:     start,
		ByteEnd:       end,
		ContentDigest: uciPublicationDigestBytes([]byte(text)),
		TextForSearch: text,
	})
	require.NoError(t, err)
	return start, end, text
}

func uciVersionedReadSpecForPathAndSpan(t *testing.T, fixture *uciPublicationFixture, authorized ucidomain.AuthorizedContext, path string, start, end int64) ucidomain.VersionedReadSpec {
	t.Helper()
	selected, err := fixture.projection.SelectCandidates(context.Background(), authorized, ucidomain.QuerySpec{
		Mode:  ucidomain.QueryModeExactRelativePath,
		Text:  path,
		Order: ucidomain.QueryOrderPath,
		Limit: 50,
	})
	require.NoError(t, err)
	require.NotNil(t, selected.Candidates)
	for _, candidate := range selected.Candidates {
		if candidate.Span.ByteStart != start || candidate.Span.ByteEnd != end {
			continue
		}
		digest := strings.TrimPrefix(string(candidate.Proof.ContentDigest), "sha256:")
		spec := ucidomain.VersionedReadSpec{
			Entity: ucidomain.QueryEntityRef{
				SourceID:  candidate.Context.SourceID,
				ViewID:    candidate.Context.ViewID,
				EntityKey: candidate.EntityKey,
			},
			MembershipID: candidate.MembershipID,
			Span: ucidomain.QuerySpan{
				ByteStart: candidate.Span.ByteStart,
				ByteEnd:   candidate.Span.ByteEnd,
				LineStart: int64(candidate.Span.LineStart),
				LineEnd:   int64(candidate.Span.LineEnd),
			},
			ContentDigest: ucidomain.QueryContentDigest(digest),
			MaxBytes:      min(int(candidate.Span.ByteEnd-candidate.Span.ByteStart), ucidomain.VersionedReadMaxBytes),
		}
		if candidate.Span.ByteEnd-candidate.Span.ByteStart <= ucidomain.VersionedReadMaxBytes {
			require.NoError(t, spec.Validate())
		}
		return spec
	}
	t.Fatalf("no candidate at %d:%d for %q", start, end, path)
	return ucidomain.VersionedReadSpec{}
}

func TestUCIVersionedReadStorePreservesSelectedMembershipForSharedArtifact(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()
	artifact := fixture.admitArtifact(t, fixture.source.SourceID, "citation-shared", "func SharedCitation() { SharedCitation() }\n", UCIParseArtifactComplete)
	paths := []string{"a-before.tsx", "a-after.tsx"}
	memberships := []ucidomain.IndexMembership{uciPublicationPresentMembership(paths[0], artifact), uciPublicationPresentMembership(paths[1], artifact)}
	replacements := []ucidomain.IndexEdgeReplacement{
		{SourcePath: paths[0], Edges: []ucidomain.IndexEdge{uciPublicationResolvedEdge(artifact, paths[0], artifact, paths[1])}},
		{SourcePath: paths[1], Edges: []ucidomain.IndexEdge{uciPublicationResolvedEdge(artifact, paths[1], artifact, paths[0])}},
	}
	draft := newUCIPublicationDraft([]ucidomain.IndexPart{uciPublicationPart([]uciPublicationArtifact{artifact}, memberships, nil, replacements)}, memberships, replacements)
	_, published := fixture.publish(t, fixture.publisher, fixture.caller("citation-shared-v1"), fixture.publishInput("citation-shared-v1", fixture.checkout, fixture.profile.ProfileID, nil, ucidomain.IndexManifestFull, ucidomain.IndexJobInitial, draft))
	authorized := uciSemanticAuthorize(t, fixture, published.Context)
	query := ucidomain.NewQueryService(fixture.projection)
	reader := ucidomain.NewVersionedReadService(fixture.projection)
	selectors := make([]ucidomain.VersionedReadSpec, 0, len(paths))
	for _, path := range paths {
		result, err := query.Query(ctx, authorized, ucidomain.QuerySpec{
			ClientSessionID: "citation-shared-client", Mode: ucidomain.QueryModeExactQualifiedSymbol,
			Text: artifact.Definition.QualifiedLocalName, Filter: ucidomain.QueryFilter{PathPrefix: path}, Order: ucidomain.QueryOrderPath, Limit: 10,
		})
		require.NoError(t, err)
		payload, err := json.Marshal(result.Response)
		require.NoError(t, err)
		var citation ucidomain.QueryResponse
		require.NoError(t, json.Unmarshal(payload, &citation))
		require.NoError(t, citation.ValidatePreExposure())
		require.Len(t, *citation.Items, 1)
		item := (*citation.Items)[0]
		require.Equal(t, path, item.Path)
		spec := ucidomain.VersionedReadSpec{Entity: item.Ref, MembershipID: item.MembershipID, Span: item.Span, ContentDigest: item.ContentDigest, MaxBytes: ucidomain.VersionedReadMaxBytes}
		selectors = append(selectors, spec)
		read, err := reader.Read(ctx, authorized, spec)
		require.NoError(t, err)
		require.Len(t, *read.Items, 1)
		readItem := (*read.Items)[0]
		require.Equal(t, item.Ref, readItem.Ref)
		require.Equal(t, item.MembershipID, readItem.MembershipID)
		require.Equal(t, path, readItem.Path)
		require.Equal(t, item.Span, readItem.Span)
		require.Equal(t, item.ContentDigest, readItem.ContentDigest)
		require.Equal(t, string(artifact.Body), readItem.Excerpt)
	}
	require.Equal(t, selectors[0].Entity, selectors[1].Entity)
	require.Equal(t, selectors[0].Span, selectors[1].Span)
	require.Equal(t, selectors[0].ContentDigest, selectors[1].ContentDigest)
	require.NotEqual(t, selectors[0].MembershipID, selectors[1].MembershipID)
	_, available, err := fixture.projection.DescribeGraphSource(ctx, authorized, selectors[0].Entity)
	require.NoError(t, err)
	require.False(t, available, "a pathless graph ref must not choose one of two memberships")
	site := artifact.Reference.ReferenceSiteID
	evidence := ucidomain.QueryRelationEvidence{Ref: selectors[0].Entity, Precision: ucidomain.QueryEvidencePrecisionReferenceSite, ReferenceSiteID: &site}
	_, available, err = fixture.projection.DescribeGraphEvidence(ctx, authorized, evidence)
	require.NoError(t, err)
	require.False(t, available, "a shared reference site cannot choose an arbitrary source edge path")

	missing := selectors[0]
	missing.MembershipID = ""
	_, err = fixture.projection.ReadExact(ctx, authorized, missing)
	require.Error(t, err)
	sibling := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "citation-sibling", path: paths[0], checkout: fixture.sibling, kind: ucidomain.IndexJobInitial, artifact: artifact})
	siblingSpec := uciVersionedReadSpecForPathAndSpan(t, fixture, uciSemanticAuthorize(t, fixture, sibling.Context), paths[0], 0, int64(len(artifact.Body)))
	foreignCheckout := fixture.registerCheckout(t, fixture.foreign, "citation-foreign")
	foreignArtifact := fixture.admitArtifact(t, fixture.foreign.SourceID, "citation-shared", strings.TrimPrefix(string(artifact.Body), "package fixture\n"), UCIParseArtifactComplete)
	fixture.authorizer.allowedSources[fixture.foreign.SourceID] = true
	foreign := uciVersionedReadPublish(t, fixture, uciVersionedReadPublishInput{key: "citation-foreign", path: paths[0], checkout: foreignCheckout, kind: ucidomain.IndexJobInitial, artifact: foreignArtifact})
	foreignSpec := uciVersionedReadSpecForPathAndSpan(t, fixture, uciSemanticAuthorize(t, fixture, foreign.Context), paths[0], 0, int64(len(artifact.Body)))
	require.Equal(t, artifact.Body, foreignArtifact.Body)
	require.Equal(t, selectors[0].ContentDigest, foreignSpec.ContentDigest)
	delete(fixture.authorizer.allowedSources, fixture.foreign.SourceID)
	for _, testCase := range []struct {
		name   string
		mutate func(*ucidomain.VersionedReadSpec)
	}{
		{"unknown membership", func(spec *ucidomain.VersionedReadSpec) { spec.MembershipID = "60000000-0000-4000-8000-000000000099" }},
		{"foreign membership", func(spec *ucidomain.VersionedReadSpec) { spec.MembershipID = foreignSpec.MembershipID }},
		{"other checkout membership", func(spec *ucidomain.VersionedReadSpec) { spec.MembershipID = siblingSpec.MembershipID }},
		{"wrong source", func(spec *ucidomain.VersionedReadSpec) { spec.Entity.SourceID = foreign.Context.SourceID }},
		{"wrong view", func(spec *ucidomain.VersionedReadSpec) { spec.Entity.ViewID = sibling.Context.ViewID }},
		{"wrong entity", func(spec *ucidomain.VersionedReadSpec) { spec.Entity.EntityKey = "not-the-selected-entity" }},
		{"wrong span", func(spec *ucidomain.VersionedReadSpec) { spec.Span.ByteEnd-- }},
		{"wrong digest", func(spec *ucidomain.VersionedReadSpec) {
			spec.ContentDigest = ucidomain.QueryContentDigest(strings.Repeat("0", 64))
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			spec := selectors[0]
			testCase.mutate(&spec)
			read, err := reader.Read(ctx, authorized, spec)
			require.NoError(t, err)
			require.Equal(t, ucidomain.QueryStatusEmpty, read.Status)
			require.Empty(t, *read.Items)
		})
	}

	changed := fixture.admitArtifact(t, fixture.source.SourceID, "citation-shared", "func SharedCitation() { /* changed */ }\n", UCIParseArtifactComplete)
	currentMemberships := []ucidomain.IndexMembership{uciPublicationPresentMembership(paths[0], changed), memberships[1]}
	edge := uciPublicationResolvedEdge(artifact, paths[1], changed, paths[0])
	secondEdge := edge
	secondEdge.EdgeKey += ":second"
	current := uciGraphStorePublish(t, fixture, uciGraphStorePublishInput{
		key: "citation-shared-v2", checkout: fixture.checkout, parent: uciPublicationParent(published), artifacts: []uciPublicationArtifact{changed, artifact}, memberships: currentMemberships,
		replacements: []ucidomain.IndexEdgeReplacement{{SourcePath: paths[0]}, {SourcePath: paths[1], Edges: []ucidomain.IndexEdge{edge, secondEdge}}},
		coverage:     uciGraphStoreCoverage(ucidomain.IndexCoveragePartial),
	})
	currentAuthorized := uciSemanticAuthorize(t, fixture, current.Context)
	inactive := selectors[0]
	inactive.Entity.ViewID = current.Context.ViewID
	miss, err := reader.Read(ctx, currentAuthorized, inactive)
	require.NoError(t, err)
	require.Equal(t, ucidomain.QueryStatusPartial, miss.Status)
	require.Empty(t, *miss.Items, "an inactive membership cannot fall back to the still-active same-body path")
	for index, spec := range selectors {
		read, err := reader.Read(ctx, authorized, spec)
		require.NoError(t, err)
		require.Equal(t, paths[index], (*read.Items)[0].Path)
		require.Equal(t, spec.MembershipID, (*read.Items)[0].MembershipID)
		require.Equal(t, string(artifact.Body), (*read.Items)[0].Excerpt)
	}
	evidence.Ref.ViewID = current.Context.ViewID
	descriptor, available, err := fixture.projection.DescribeGraphEvidence(ctx, currentAuthorized, evidence)
	require.NoError(t, err)
	require.True(t, available, "multiple edges of one membership are still one location")
	require.Equal(t, selectors[1].MembershipID, descriptor.MembershipID)
	read, err := reader.Read(ctx, currentAuthorized, descriptor)
	require.NoError(t, err)
	require.Len(t, *read.Items, 1)
	require.Equal(t, paths[1], (*read.Items)[0].Path)
	require.Equal(t, string(artifact.Body[descriptor.Span.ByteStart:descriptor.Span.ByteEnd]), (*read.Items)[0].Excerpt)
	wrongReferenceMembership := descriptor
	wrongReferenceMembership.MembershipID = selectors[0].MembershipID
	miss, err = reader.Read(ctx, currentAuthorized, wrongReferenceMembership)
	require.NoError(t, err)
	require.Empty(t, *miss.Items, "reference-site reads must enforce the same membership interval")
}
