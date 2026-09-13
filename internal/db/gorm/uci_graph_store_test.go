package gorm

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	ucidomain "github.com/thebtf/engram/internal/uci"
)

func TestUCIGraphStorePinsTargetsAndEdgesToExactTemporalView(t *testing.T) {
	fixture := openUCIPublicationFixture(t)
	ctx := context.Background()

	entryV1 := uciGraphStoreArtifact(t, fixture, "graph-entry-v1", "func EntryV1() {}\n", "Entry", "graph.Entry", "imports")
	callerV1 := fixture.admitArtifact(t, fixture.source.SourceID, "graph-caller", "func CallerV1() {}\n", UCIParseArtifactComplete)
	targetV1 := fixture.admitArtifact(t, fixture.source.SourceID, "graph-target-v1", "func TargetV1() {}\n", UCIParseArtifactComplete)
	primaryV1 := uciGraphStorePublish(t, fixture, uciGraphStorePublishInput{
		key: "graph-primary-v1", checkout: fixture.checkout,
		artifacts: []uciPublicationArtifact{entryV1, callerV1, targetV1},
		memberships: []ucidomain.IndexMembership{
			uciPublicationPresentMembership("entry.go", entryV1),
			uciPublicationPresentMembership("caller.go", callerV1),
			uciPublicationPresentMembership("target.go", targetV1),
		},
		replacements: []ucidomain.IndexEdgeReplacement{
			{SourcePath: "entry.go", Edges: []ucidomain.IndexEdge{uciGraphStoreResolvedEdge(entryV1, "entry.go", callerV1, "caller.go", "imports", "extracted")}},
			{SourcePath: "caller.go", Edges: []ucidomain.IndexEdge{uciGraphStoreResolvedEdge(callerV1, "caller.go", targetV1, "target.go", "calls", "resolved"), uciGraphStoreUnresolvedEdge(callerV1, "caller.go", "missing-v1")}},
			{SourcePath: "target.go"},
		},
		coverage: uciGraphStoreCoverage(ucidomain.IndexCoverageComplete),
	})

	entrySibling := uciGraphStoreArtifact(t, fixture, "graph-entry-sibling", "func EntrySibling() {}\n", "Entry", "graph.Entry", "imports")
	callerSibling := fixture.admitArtifact(t, fixture.source.SourceID, "graph-caller", "func CallerSibling() {}\n", UCIParseArtifactComplete)
	targetSibling := fixture.admitArtifact(t, fixture.source.SourceID, "graph-target-sibling", "func TargetSibling() {}\n", UCIParseArtifactComplete)
	siblingV1 := uciGraphStorePublish(t, fixture, uciGraphStorePublishInput{
		key: "graph-sibling-v1", checkout: fixture.sibling,
		artifacts: []uciPublicationArtifact{entrySibling, callerSibling, targetSibling},
		memberships: []ucidomain.IndexMembership{
			uciPublicationPresentMembership("entry.go", entrySibling),
			uciPublicationPresentMembership("caller.go", callerSibling),
			uciPublicationPresentMembership("target.go", targetSibling),
		},
		replacements: []ucidomain.IndexEdgeReplacement{
			{SourcePath: "entry.go", Edges: []ucidomain.IndexEdge{uciGraphStoreResolvedEdge(entrySibling, "entry.go", callerSibling, "caller.go", "imports", "extracted")}},
			{SourcePath: "caller.go", Edges: []ucidomain.IndexEdge{uciGraphStoreResolvedEdge(callerSibling, "caller.go", targetSibling, "target.go", "calls", "resolved")}},
			{SourcePath: "target.go"},
		},
		coverage: uciGraphStoreCoverage(ucidomain.IndexCoverageComplete),
	})

	callerV2 := fixture.admitArtifact(t, fixture.source.SourceID, "graph-caller", "func CallerV2() {}\n", UCIParseArtifactComplete)
	targetV2 := fixture.admitArtifact(t, fixture.source.SourceID, "graph-target-v2", "func TargetV2() {}\n", UCIParseArtifactComplete)
	handlerA := uciGraphStoreArtifact(t, fixture, "graph-handler-a", "func HandleA() {}\n", "Handle", "graph.http.Handle", "calls")
	handlerB := uciGraphStoreArtifact(t, fixture, "graph-handler-b", "func HandleB() {}\n", "Handle", "graph.queue.Handle", "calls")
	primaryV2 := uciGraphStorePublish(t, fixture, uciGraphStorePublishInput{
		key: "graph-primary-v2", checkout: fixture.checkout, parent: uciPublicationParent(primaryV1),
		artifacts: []uciPublicationArtifact{entryV1, callerV2, targetV2, handlerA, handlerB},
		memberships: []ucidomain.IndexMembership{
			uciPublicationPresentMembership("entry.go", entryV1),
			uciPublicationPresentMembership("caller.go", callerV2),
			uciPublicationPresentMembership("target.go", targetV2),
			uciPublicationPresentMembership("handler-a.go", handlerA),
			uciPublicationPresentMembership("handler-b.go", handlerB),
		},
		replacements: []ucidomain.IndexEdgeReplacement{
			{SourcePath: "entry.go", Edges: []ucidomain.IndexEdge{uciGraphStoreResolvedEdge(entryV1, "entry.go", callerV2, "caller.go", "imports", "extracted")}},
			{SourcePath: "caller.go", Edges: []ucidomain.IndexEdge{uciGraphStoreResolvedEdge(callerV2, "caller.go", targetV2, "target.go", "calls", "resolved"), uciGraphStoreUnresolvedEdge(callerV2, "caller.go", "missing-v2")}},
			{SourcePath: "target.go"},
			{SourcePath: "handler-a.go"},
			{SourcePath: "handler-b.go"},
		},
		coverage: uciGraphStoreCoverage(ucidomain.IndexCoveragePartial),
	})

	var historical UCIView
	require.NoError(t, fixture.db.Where("view_id = ?", primaryV1.Context.ViewID).First(&historical).Error)
	require.Equal(t, UCIViewSuperseded, historical.State, "the old authorized View remains a readable historical snapshot")

	historicalAuthorized := uciGraphStoreAuthorize(t, fixture, primaryV1.Context)
	currentAuthorized := uciGraphStoreAuthorize(t, fixture, primaryV2.Context)
	siblingAuthorized := uciGraphStoreAuthorize(t, fixture, siblingV1.Context)

	t.Run("resolves exact keys and same-view name ambiguity", func(t *testing.T) {
		current, err := fixture.projection.ResolveGraphTargets(ctx, currentAuthorized, ucidomain.GraphTarget{EntityKey: callerV2.Definition.QualifiedLocalName})
		require.NoError(t, err)
		require.Equal(t, ucidomain.IndexCoveragePartial, current.Coverage)
		require.Equal(t, []ucidomain.QueryEntityRef{uciGraphStoreRef(primaryV2.Context, callerV2.Definition.QualifiedLocalName)}, current.Candidates)

		historical, err := fixture.projection.ResolveGraphTargets(ctx, historicalAuthorized, ucidomain.GraphTarget{EntityKey: callerV1.Definition.QualifiedLocalName})
		require.NoError(t, err)
		require.Equal(t, ucidomain.IndexCoverageComplete, historical.Coverage)
		require.Equal(t, []ucidomain.QueryEntityRef{uciGraphStoreRef(primaryV1.Context, callerV1.Definition.QualifiedLocalName)}, historical.Candidates)

		ambiguous, err := fixture.projection.ResolveGraphTargets(ctx, currentAuthorized, ucidomain.GraphTarget{Name: "Handle"})
		require.NoError(t, err)
		require.Equal(t, []ucidomain.QueryEntityRef{
			uciGraphStoreRef(primaryV2.Context, handlerA.Definition.QualifiedLocalName),
			uciGraphStoreRef(primaryV2.Context, handlerB.Definition.QualifiedLocalName),
		}, ambiguous.Candidates, "same-view local-name collisions remain explicit candidates")
	})

	t.Run("selects outgoing incoming and both evidence without fabricating unresolved targets", func(t *testing.T) {
		caller := uciGraphStoreRef(primaryV2.Context, callerV2.Definition.QualifiedLocalName)
		outgoing, err := fixture.projection.SelectGraphEdges(ctx, currentAuthorized, ucidomain.GraphEdgeQuery{
			Nodes: []ucidomain.QueryEntityRef{caller},
			Filter: ucidomain.GraphFilter{
				Direction:     ucidomain.GraphDirectionOutgoing,
				Relations:     []ucidomain.IndexRelation{"calls"},
				EvidenceKinds: []ucidomain.QueryEvidenceKind{ucidomain.QueryEvidenceResolved},
			},
		})
		require.NoError(t, err)
		require.Equal(t, ucidomain.IndexCoveragePartial, outgoing.Coverage)
		require.Equal(t, []ucidomain.QueryGraphEdge{uciGraphStoreExpectedEdge(primaryV2.Context, callerV2.Definition.QualifiedLocalName, targetV2.Definition.QualifiedLocalName, "calls", ucidomain.QueryEvidenceResolved)}, outgoing.Edges)
		require.Equal(t, []ucidomain.GraphUnresolvedSite{uciGraphStoreExpectedUnresolved(primaryV2.Context, callerV2.Definition.QualifiedLocalName, "calls")}, outgoing.Unresolved)

		incoming, err := fixture.projection.SelectGraphEdges(ctx, currentAuthorized, ucidomain.GraphEdgeQuery{
			Nodes: []ucidomain.QueryEntityRef{caller},
			Filter: ucidomain.GraphFilter{
				Direction:     ucidomain.GraphDirectionIncoming,
				Relations:     []ucidomain.IndexRelation{"imports"},
				EvidenceKinds: []ucidomain.QueryEvidenceKind{ucidomain.QueryEvidenceExtracted},
			},
		})
		require.NoError(t, err)
		require.Equal(t, []ucidomain.QueryGraphEdge{uciGraphStoreExpectedEdge(primaryV2.Context, entryV1.Definition.QualifiedLocalName, callerV2.Definition.QualifiedLocalName, "imports", ucidomain.QueryEvidenceExtracted)}, incoming.Edges)
		require.Empty(t, incoming.Unresolved, "unresolved links have no invented incoming target")

		wrongEvidence, err := fixture.projection.SelectGraphEdges(ctx, currentAuthorized, ucidomain.GraphEdgeQuery{
			Nodes: []ucidomain.QueryEntityRef{caller},
			Filter: ucidomain.GraphFilter{
				Direction:     ucidomain.GraphDirectionIncoming,
				Relations:     []ucidomain.IndexRelation{"imports"},
				EvidenceKinds: []ucidomain.QueryEvidenceKind{ucidomain.QueryEvidenceResolved},
			},
		})
		require.NoError(t, err)
		require.Empty(t, wrongEvidence.Edges, "evidence filtering happens before result selection")

		both, err := fixture.projection.SelectGraphEdges(ctx, currentAuthorized, ucidomain.GraphEdgeQuery{
			Nodes:  []ucidomain.QueryEntityRef{caller},
			Filter: ucidomain.GraphFilter{Direction: ucidomain.GraphDirectionBoth},
		})
		require.NoError(t, err)
		require.Len(t, both.Edges, 2)
		uciGraphStoreRequireEdge(t, both.Edges, primaryV2.Context, entryV1.Definition.QualifiedLocalName, callerV2.Definition.QualifiedLocalName, "imports", ucidomain.QueryEvidenceExtracted)
		uciGraphStoreRequireEdge(t, both.Edges, primaryV2.Context, callerV2.Definition.QualifiedLocalName, targetV2.Definition.QualifiedLocalName, "calls", ucidomain.QueryEvidenceResolved)
		require.Equal(t, []ucidomain.GraphUnresolvedSite{uciGraphStoreExpectedUnresolved(primaryV2.Context, callerV2.Definition.QualifiedLocalName, "calls")}, both.Unresolved)
	})

	t.Run("keeps historical and sibling checkout edges exact", func(t *testing.T) {
		historical, err := fixture.projection.SelectGraphEdges(ctx, historicalAuthorized, ucidomain.GraphEdgeQuery{
			Nodes:  []ucidomain.QueryEntityRef{uciGraphStoreRef(primaryV1.Context, callerV1.Definition.QualifiedLocalName)},
			Filter: ucidomain.GraphFilter{Direction: ucidomain.GraphDirectionOutgoing, Relations: []ucidomain.IndexRelation{"calls"}},
		})
		require.NoError(t, err)
		require.Equal(t, ucidomain.IndexCoverageComplete, historical.Coverage)
		require.Equal(t, []ucidomain.QueryGraphEdge{uciGraphStoreExpectedEdge(primaryV1.Context, callerV1.Definition.QualifiedLocalName, targetV1.Definition.QualifiedLocalName, "calls", ucidomain.QueryEvidenceResolved)}, historical.Edges)

		sibling, err := fixture.projection.SelectGraphEdges(ctx, siblingAuthorized, ucidomain.GraphEdgeQuery{
			Nodes:  []ucidomain.QueryEntityRef{uciGraphStoreRef(siblingV1.Context, callerSibling.Definition.QualifiedLocalName)},
			Filter: ucidomain.GraphFilter{Direction: ucidomain.GraphDirectionOutgoing, Relations: []ucidomain.IndexRelation{"calls"}},
		})
		require.NoError(t, err)
		require.Equal(t, ucidomain.IndexCoverageComplete, sibling.Coverage)
		require.Equal(t, []ucidomain.QueryGraphEdge{uciGraphStoreExpectedEdge(siblingV1.Context, callerSibling.Definition.QualifiedLocalName, targetSibling.Definition.QualifiedLocalName, "calls", ucidomain.QueryEvidenceResolved)}, sibling.Edges)
		require.NotEqual(t, targetV2.Definition.QualifiedLocalName, targetV1.Definition.QualifiedLocalName)
		require.NotEqual(t, targetV2.Definition.QualifiedLocalName, targetSibling.Definition.QualifiedLocalName)
	})

	t.Run("closes stale and cross-view requests without evidence leakage", func(t *testing.T) {
		staleRef := primaryV2.Context
		staleRef.Generation++
		staleAuthorized := uciGraphStoreAuthorize(t, fixture, staleRef)

		resolution, err := fixture.projection.ResolveGraphTargets(ctx, staleAuthorized, ucidomain.GraphTarget{EntityKey: callerV2.Definition.QualifiedLocalName})
		require.NoError(t, err)
		require.Equal(t, ucidomain.IndexCoverageUnavailable, resolution.Coverage)
		require.Empty(t, resolution.Candidates)

		staleEdges, err := fixture.projection.SelectGraphEdges(ctx, staleAuthorized, ucidomain.GraphEdgeQuery{
			Nodes:  []ucidomain.QueryEntityRef{uciGraphStoreRef(staleRef, callerV2.Definition.QualifiedLocalName)},
			Filter: ucidomain.GraphFilter{Direction: ucidomain.GraphDirectionOutgoing},
		})
		require.NoError(t, err)
		require.Equal(t, ucidomain.IndexCoverageUnavailable, staleEdges.Coverage)
		require.Empty(t, staleEdges.Edges)
		require.Empty(t, staleEdges.Unresolved)

		crossViewEdges, err := fixture.projection.SelectGraphEdges(ctx, currentAuthorized, ucidomain.GraphEdgeQuery{
			Nodes:  []ucidomain.QueryEntityRef{uciGraphStoreRef(siblingV1.Context, callerSibling.Definition.QualifiedLocalName)},
			Filter: ucidomain.GraphFilter{Direction: ucidomain.GraphDirectionOutgoing},
		})
		require.NoError(t, err)
		require.Equal(t, ucidomain.IndexCoveragePartial, crossViewEdges.Coverage)
		require.Empty(t, crossViewEdges.Edges)
		require.Empty(t, crossViewEdges.Unresolved)
	})

	t.Run("applies scope and filters before the bounded adjacency result", func(t *testing.T) {
		uciGraphStoreInsertNoiseEdges(t, fixture, uciGraphStoreNoiseInput{checkout: fixture.checkout, generation: primaryV2.Context.Generation, source: callerV2, sourcePath: "caller.go", target: targetV2, targetPath: "target.go", relation: "imports", prefix: "current-filter-noise", count: uciGraphStoreAdjacencyLimit + 1})
		uciGraphStoreInsertNoiseEdges(t, fixture, uciGraphStoreNoiseInput{checkout: fixture.sibling, generation: siblingV1.Context.Generation, source: callerSibling, sourcePath: "caller.go", target: targetSibling, targetPath: "target.go", relation: "calls", prefix: "sibling-scope-noise", count: uciGraphStoreAdjacencyLimit + 1})

		selected, err := fixture.projection.SelectGraphEdges(ctx, currentAuthorized, ucidomain.GraphEdgeQuery{
			Nodes: []ucidomain.QueryEntityRef{uciGraphStoreRef(primaryV2.Context, callerV2.Definition.QualifiedLocalName)},
			Filter: ucidomain.GraphFilter{
				Direction:     ucidomain.GraphDirectionOutgoing,
				Relations:     []ucidomain.IndexRelation{"calls"},
				EvidenceKinds: []ucidomain.QueryEvidenceKind{ucidomain.QueryEvidenceResolved},
			},
		})
		require.NoError(t, err)
		require.Equal(t, []ucidomain.QueryGraphEdge{uciGraphStoreExpectedEdge(primaryV2.Context, callerV2.Definition.QualifiedLocalName, targetV2.Definition.QualifiedLocalName, "calls", ucidomain.QueryEvidenceResolved)}, selected.Edges)
		require.Equal(t, []ucidomain.GraphUnresolvedSite{uciGraphStoreExpectedUnresolved(primaryV2.Context, callerV2.Definition.QualifiedLocalName, "calls")}, selected.Unresolved)
	})
}

func uciGraphStoreArtifact(t *testing.T, fixture *uciPublicationFixture, label, source, name, qualifiedName, referenceRelation string) uciPublicationArtifact {
	t.Helper()

	artifact := fixture.insertArtifact(t, fixture.source.SourceID, label, source, UCIParseArtifactComplete)
	definitionUpdate := fixture.db.Model(&UCIDefinition{}).Where("definition_id = ?", artifact.Definition.DefinitionID).Updates(map[string]any{
		"name":                 name,
		"qualified_local_name": qualifiedName,
	})
	require.NoError(t, definitionUpdate.Error)
	require.Equal(t, int64(1), definitionUpdate.RowsAffected)
	referenceUpdate := fixture.db.Model(&UCIReferenceSite{}).Where("reference_site_id = ?", artifact.Reference.ReferenceSiteID).Update("relation", referenceRelation)
	require.NoError(t, referenceUpdate.Error)
	require.Equal(t, int64(1), referenceUpdate.RowsAffected)

	artifact.Definition.Name = name
	artifact.Definition.QualifiedLocalName = qualifiedName
	artifact.Reference.Relation = referenceRelation
	artifact.Proof = fixture.describeArtifact(t, fixture.source.SourceID, artifact)
	return artifact
}

type uciGraphStorePublishInput struct {
	key          string
	checkout     *UCICheckout
	parent       *ucidomain.ContextRef
	artifacts    []uciPublicationArtifact
	memberships  []ucidomain.IndexMembership
	replacements []ucidomain.IndexEdgeReplacement
	coverage     ucidomain.IndexCoverage
}

func uciGraphStorePublish(t *testing.T, fixture *uciPublicationFixture, input uciGraphStorePublishInput) ucidomain.IndexPublishedView {
	t.Helper()
	draft := newUCIPublicationDraft(
		[]ucidomain.IndexPart{uciPublicationPart(input.artifacts, input.memberships, nil, input.replacements)},
		input.memberships,
		input.replacements,
	)
	draft.coverage = input.coverage
	kind := ucidomain.IndexJobInitial
	if input.parent != nil {
		kind = ucidomain.IndexJobReconcile
	}
	_, published := fixture.publish(t, fixture.publisher, fixture.caller("graph-store-"+input.key), fixture.publishInput(input.key, input.checkout, fixture.profile.ProfileID, input.parent, ucidomain.IndexManifestFull, kind, draft))
	return published
}

func uciGraphStoreCoverage(structural ucidomain.IndexCoverageState) ucidomain.IndexCoverage {
	return ucidomain.IndexCoverage{
		Structural: structural,
		Lexical:    structural,
		Vector:     ucidomain.IndexCoverageUnavailable,
	}
}

func uciGraphStoreResolvedEdge(source uciPublicationArtifact, sourcePath string, target uciPublicationArtifact, targetPath string, relation ucidomain.IndexRelation, evidence ucidomain.IndexEvidenceKind) ucidomain.IndexEdge {
	edge := uciPublicationResolvedEdge(source, sourcePath, target, targetPath)
	edge.EdgeKey += ":" + string(relation) + ":" + string(evidence)
	edge.Relation = relation
	edge.EvidenceKind = evidence
	return edge
}

func uciGraphStoreUnresolvedEdge(source uciPublicationArtifact, sourcePath, suffix string) ucidomain.IndexEdge {
	sourceSymbol := source.Definition.LocalSymbolKey
	referenceSiteID := source.Reference.ReferenceSiteID
	return ucidomain.IndexEdge{
		EdgeKey:          sourcePath + "->" + suffix,
		SourceArtifactID: source.Artifact.ArtifactID,
		SourceSymbolKey:  &sourceSymbol,
		Relation:         "calls",
		EvidenceKind:     "unresolved",
		ResolutionState:  "unresolved",
		ResolverRevision: "graph-store-fixture",
		Evidence: ucidomain.IndexEdgeEvidence{
			ReferenceSiteID: &referenceSiteID,
			Span:            ucidomain.IndexSpan{ByteStart: 0, ByteEnd: 1, LineStart: 1, LineEnd: 1},
			RuleKey:         "graph-store-unresolved",
			Explanation:     "fixture cannot resolve target",
		},
	}
}

func uciGraphStoreAuthorize(t *testing.T, fixture *uciPublicationFixture, ref ucidomain.ContextRef) ucidomain.AuthorizedContext {
	t.Helper()

	resolver := ucidomain.NewContextResolver(uciGraphStoreCatalog{realm: fixture.realm}, fixture.authorizer, nil)
	authorized, err := resolver.Authorize(context.Background(), ucidomain.ResolveContextInput{
		ClientSessionID: "graph-store-client-" + fixture.token + "-" + ref.ViewID,
		AuthRealm:       fixture.realm,
		Principal:       fixture.principal,
		Ref:             &ref,
	})
	require.NoError(t, err)
	require.Equal(t, ref, authorized.Ref())
	return authorized
}

type uciGraphStoreCatalog struct {
	realm string
}

func (catalog uciGraphStoreCatalog) LoadContext(_ context.Context, ref ucidomain.ContextRef) (ucidomain.ContextRecord, error) {
	return ucidomain.ContextRecord{Ref: ref, AuthRealm: catalog.realm}, nil
}

func uciGraphStoreRef(ref ucidomain.ContextRef, entityKey string) ucidomain.QueryEntityRef {
	return ucidomain.QueryEntityRef{SourceID: ref.SourceID, ViewID: ref.ViewID, EntityKey: entityKey}
}

func uciGraphStoreExpectedEdge(ref ucidomain.ContextRef, from, to string, relation ucidomain.IndexRelation, evidence ucidomain.QueryEvidenceKind) ucidomain.QueryGraphEdge {
	fromRef := uciGraphStoreRef(ref, from)
	return ucidomain.QueryGraphEdge{
		From:         fromRef,
		To:           uciGraphStoreRef(ref, to),
		Relation:     relation,
		EvidenceKind: evidence,
		EvidenceRefs: []ucidomain.QueryEntityRef{fromRef},
	}
}

func uciGraphStoreExpectedUnresolved(ref ucidomain.ContextRef, from string, relation ucidomain.IndexRelation) ucidomain.GraphUnresolvedSite {
	fromRef := uciGraphStoreRef(ref, from)
	return ucidomain.GraphUnresolvedSite{
		From:         fromRef,
		Relation:     relation,
		EvidenceRefs: []ucidomain.QueryEntityRef{fromRef},
	}
}

func uciGraphStoreRequireEdge(t *testing.T, edges []ucidomain.QueryGraphEdge, ref ucidomain.ContextRef, from, to string, relation ucidomain.IndexRelation, evidence ucidomain.QueryEvidenceKind) {
	t.Helper()

	want := uciGraphStoreExpectedEdge(ref, from, to, relation, evidence)
	for _, edge := range edges {
		if edge.From == want.From && edge.To == want.To && edge.Relation == want.Relation && edge.EvidenceKind == want.EvidenceKind && len(edge.EvidenceRefs) == 1 && edge.EvidenceRefs[0] == want.EvidenceRefs[0] {
			return
		}
	}
	t.Fatalf("missing graph edge %#v in %#v", want, edges)
}

type uciGraphStoreNoiseInput struct {
	checkout   *UCICheckout
	generation int64
	source     uciPublicationArtifact
	sourcePath string
	target     uciPublicationArtifact
	targetPath string
	relation   string
	prefix     string
	count      int
}

func uciGraphStoreInsertNoiseEdges(t *testing.T, fixture *uciPublicationFixture, input uciGraphStoreNoiseInput) {
	t.Helper()
	sourceSymbol := input.source.Definition.LocalSymbolKey
	targetArtifactID := input.target.Artifact.ArtifactID
	targetSymbol := input.target.Definition.LocalSymbolKey
	now := time.Now().UTC()
	rows := make([]UCIResolvedEdge, 0, input.count)
	for index := range input.count {
		rows = append(rows, UCIResolvedEdge{
			ResolvedEdgeID:      uuid.NewString(),
			SourceID:            fixture.source.SourceID,
			CheckoutID:          input.checkout.CheckoutID,
			EdgeKey:             fmt.Sprintf("%s-%06d", input.prefix, index),
			SourcePath:          input.sourcePath,
			SourceArtifact:      input.source.Artifact.ArtifactID,
			SourceSymbol:        &sourceSymbol,
			TargetPath:          &input.targetPath,
			TargetArtifact:      &targetArtifactID,
			TargetSymbol:        &targetSymbol,
			Relation:            input.relation,
			EvidenceKind:        UCIResolvedEdgeEvidenceResolved,
			ResolverRevision:    "graph-store-noise",
			EvidenceJSON:        `{}`,
			ResolutionState:     UCIResolvedEdgeResolved,
			ValidFromGeneration: input.generation,
			CreatedAt:           now,
		})
	}
	require.NoError(t, fixture.db.CreateInBatches(rows, 500).Error)
}
