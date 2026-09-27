package codeintel

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/uci"
)

func TestUCIPreparedIndexResolvesTreeSitterModulesAliasesAndCalls(t *testing.T) {
	t.Parallel()
	artifact := func(id string, definitions []uci.IndexAdmissionDefinition, references []uci.IndexAdmissionReference) *uci.IndexAdmissionArtifact {
		return &uci.IndexAdmissionArtifact{
			ArtifactID:  id,
			Profile:     uci.IndexAdmissionArtifactProfile{Language: uci.IndexAdmissionLanguageTypeScript},
			Definitions: definitions,
			References:  references,
		}
	}
	remoteID := "11111111-1111-4111-8111-111111111111"
	callerID := "22222222-2222-4222-8222-222222222222"
	bridgeID := "33333333-3333-4333-8333-333333333333"
	callerOwner := "function:start"
	files := []uciPreparedAdmissionFile{
		{
			path:       "remote.ts",
			membership: uci.IndexAdmissionMembership{PathKey: "remote.ts", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &remoteID},
			artifact: artifact(remoteID, []uci.IndexAdmissionDefinition{{
				LocalSymbolKey: "function:build",
				Kind:           "function",
				SymbolKey:      "typescript:function:build",
			}}, nil),
		},
		{
			path:       "caller.ts",
			membership: uci.IndexAdmissionMembership{PathKey: "caller.ts", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &callerID},
			artifact: artifact(callerID, []uci.IndexAdmissionDefinition{{
				LocalSymbolKey: "function:start",
				Kind:           "function",
				SymbolKey:      "typescript:function:start",
			}}, []uci.IndexAdmissionReference{
				{SiteKey: "import:./remote.ts@0:20", Kind: "import", SymbolKey: "typescript:import:./remote.ts@0:20", RawTarget: "import from remote", Relation: uci.IndexRelation("imports")},
				{SiteKey: "import:./remote.ts#build:localBuild@21:40", Kind: "import_alias", SymbolKey: "typescript:import:./remote.ts#build:localBuild@21:40", RawTarget: "build as localBuild", Relation: uci.IndexRelation("imports")},
				{SiteKey: "call:localBuild@41:52", Kind: "call", SymbolKey: "typescript:call:localBuild@41:52", OwnerSymbolKey: &callerOwner, RawTarget: "localBuild()", Relation: uci.IndexRelation("calls")},
			}),
		},
		{
			path:       "bridge.ts",
			membership: uci.IndexAdmissionMembership{PathKey: "bridge.ts", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &bridgeID},
			artifact: artifact(bridgeID, nil, []uci.IndexAdmissionReference{
				{SiteKey: "reexport:./remote.ts@0:20", Kind: "reexport", SymbolKey: "typescript:reexport:./remote.ts@0:20", RawTarget: "export from remote", Relation: uci.IndexRelation("exports")},
				{SiteKey: "reexport:./remote.ts#build:publicBuild@21:45", Kind: "reexport_alias", SymbolKey: "typescript:reexport:./remote.ts#build:publicBuild@21:45", RawTarget: "build as publicBuild", Relation: uci.IndexRelation("exports")},
			}),
		},
	}

	unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
	require.NoError(t, err)
	require.Zero(t, unresolved)
	require.Len(t, files[1].edges, 3)
	require.Len(t, files[2].edges, 2)
	for _, edge := range append(append([]uci.IndexAdmissionEdge(nil), files[1].edges...), files[2].edges...) {
		require.NotNil(t, edge.Target)
		require.Equal(t, "remote.ts", edge.Target.PathKey)
		require.Equal(t, remoteID, edge.Target.ArtifactID)
		if edge.Relation == uci.IndexRelation("calls") || edge.Evidence.ReferenceSiteKey == "import:./remote.ts#build:localBuild@21:40" || edge.Evidence.ReferenceSiteKey == "reexport:./remote.ts#build:publicBuild@21:45" {
			require.NotNil(t, edge.Target.SymbolKey)
			require.Equal(t, "function:build", *edge.Target.SymbolKey)
		}
	}
}

func TestUCIPreparedIndexLeavesAmbiguousTreeSitterImportsUnresolved(t *testing.T) {
	t.Parallel()
	remoteID := "11111111-1111-4111-8111-111111111111"
	callerID := "22222222-2222-4222-8222-222222222222"
	files := []uciPreparedAdmissionFile{
		{
			path:       "remote.ts",
			membership: uci.IndexAdmissionMembership{PathKey: "remote.ts", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &remoteID},
			artifact: &uci.IndexAdmissionArtifact{
				ArtifactID: remoteID,
				Profile:    uci.IndexAdmissionArtifactProfile{Language: uci.IndexAdmissionLanguageTypeScript},
				Definitions: []uci.IndexAdmissionDefinition{
					{LocalSymbolKey: "function:build", Kind: "function", SymbolKey: "typescript:function:build"},
					{LocalSymbolKey: "class:build", Kind: "class", SymbolKey: "typescript:class:build"},
				},
			},
		},
		{
			path:       "caller.ts",
			membership: uci.IndexAdmissionMembership{PathKey: "caller.ts", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &callerID},
			artifact: &uci.IndexAdmissionArtifact{
				ArtifactID: callerID,
				Profile:    uci.IndexAdmissionArtifactProfile{Language: uci.IndexAdmissionLanguageTypeScript},
				References: []uci.IndexAdmissionReference{{
					SiteKey:   "import:./remote.ts#build:localBuild@0:40",
					Kind:      "import_alias",
					SymbolKey: "typescript:import:./remote.ts#build:localBuild@0:40",
					RawTarget: "build as localBuild",
					Relation:  uci.IndexRelation("imports"),
				}},
			},
		},
	}

	unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
	require.NoError(t, err)
	require.Equal(t, uint64(1), unresolved)
	require.Empty(t, files[1].edges, "an ambiguous symbol must not be emitted as a resolved module-level edge")
}

func TestUCIPreparedIndexResolvesSameFileExportedCalls(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, path string
		language   uci.IndexAdmissionLanguage
		body       string
		target     string
		caller     string
	}{
		{"javascript", "comet.js", uci.IndexAdmissionLanguageJavaScript, "export function calibrateInfraredPrism(pulseCount) { return pulseCount; }\nexport function guideCometOptics(pulseCount) { return calibrateInfraredPrism(pulseCount); }", "calibrateInfraredPrism", "guideCometOptics"},
		{"typescript", "tidal.ts", uci.IndexAdmissionLanguageTypeScript, "export function preserveFreshwaterDuringSalineSurge(gate: string): string { return gate; }\nexport function controlEstuaryGate(gate: string): string { return preserveFreshwaterDuringSalineSurge(gate); }", "preserveFreshwaterDuringSalineSurge", "controlEstuaryGate"},
		{"tsx", "trail.tsx", uci.IndexAdmissionLanguageTSX, "export function labelAvalancheEscapeCorridor(trail: string): string { return trail; }\nexport function showMountainTrailGuide(trail: string) { const warning = labelAvalancheEscapeCorridor(trail); return <strong>{warning}</strong>; }", "labelAvalancheEscapeCorridor", "showMountainTrailGuide"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			artifactID := "11111111-1111-4111-8111-111111111111"
			owner := "function:" + test.caller
			callStart := strings.LastIndex(test.body, test.target+"(")
			callerStart := strings.Index(test.body, "export function "+test.caller)
			callEnd := callStart + len(test.target) + 1
			call := "call:" + test.target + "@" + strconv.Itoa(callStart) + ":" + strconv.Itoa(callEnd)
			files := []uciPreparedAdmissionFile{{
				path:       test.path,
				membership: uci.IndexAdmissionMembership{PathKey: test.path, State: uci.IndexAdmissionMembershipPresent, ArtifactID: &artifactID},
				artifact: &uci.IndexAdmissionArtifact{
					ArtifactID: artifactID, Body: []byte(test.body), Profile: uci.IndexAdmissionArtifactProfile{Language: test.language},
					Definitions: []uci.IndexAdmissionDefinition{
						{LocalSymbolKey: "function:" + test.target, Kind: "function", SymbolKey: string(test.language) + ":function:" + test.target, Span: uci.IndexSpan{ByteStart: 0, ByteEnd: int64(callerStart - 1)}},
						{LocalSymbolKey: owner, Kind: "function", SymbolKey: string(test.language) + ":" + owner, Span: uci.IndexSpan{ByteStart: int64(callerStart), ByteEnd: int64(len(test.body))}},
					},
					References: []uci.IndexAdmissionReference{{SiteKey: call, Kind: "call", SymbolKey: string(test.language) + ":" + call, OwnerSymbolKey: &owner, RawTarget: test.target, Relation: uci.IndexRelation("calls"), Span: uci.IndexSpan{ByteStart: int64(callStart), ByteEnd: int64(callEnd)}}},
				},
			}}
			unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			require.Zero(t, unresolved)
			require.Len(t, files[0].edges, 1)
			edge := files[0].edges[0]
			require.Equal(t, uci.IndexRelation("calls"), edge.Relation)
			require.Equal(t, uci.IndexResolutionState("resolved"), edge.ResolutionState)
			require.Equal(t, "uci-prepared-tree-sitter-local-call/v1", edge.ResolverRevision)
			require.Equal(t, "tree-sitter-direct-local-call/v1", edge.Evidence.RuleKey)
			require.Equal(t, &owner, edge.SourceSymbolKey)
			require.Equal(t, test.path, edge.Target.PathKey)
			require.Equal(t, artifactID, edge.Target.ArtifactID)
			require.Equal(t, "function:"+test.target, *edge.Target.SymbolKey)
		})
	}
}

func TestUCIPreparedIndexDoesNotGuessShadowedOrDynamicLocalCalls(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, source, kind, site, raw string
		ambiguous                     bool
	}{
		{"parameter shadow", "export function helper(x) { return x; }\nexport function caller(helper) { return helper(1); }", "call", "call:helper", "helper", false},
		{"arrow parameter shadow", "export function helper(x) { return x; }\nexport function caller(x) { return ((helper) => helper(1))(x); }", "call", "call:helper", "helper", false},
		{"nested function shadow", "export function helper(x) { return x; }\nexport function caller(x) { function helper(y) { return y; } return helper(x); }", "call", "call:helper", "helper", false},
		{"optional dynamic call", "export function helper(x) { return x; }\nexport function caller(x) { return helper?.(x); }", "call", "call:helper?.", "helper?.", false},
		{"ambiguous declaration", "export function helper(x) { return x; }\nconst helper = 1;\nexport function caller(x) { return helper(x); }", "call", "call:helper", "helper", true},
		{"duplicate exported function", "export function helper(x) { return x; }\nexport function helper (y) { return y; }\nexport function caller(x) { return helper(x); }", "call", "call:helper", "helper", false},
		{"dynamic member", "export function helper(x) { return x; }\nexport function caller(x) { return x.helper(1); }", "call", "call:x.helper", "x.helper", false},
		{"non call", "export function helper(x) { return x; }\nexport function caller(x) { return helper(x); }", "reference", "reference:helper", "helper", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			artifactID := "11111111-1111-4111-8111-111111111111"
			owner := "function:caller"
			callerStart := strings.Index(test.source, "export function caller")
			callStart := strings.LastIndex(test.source, test.raw+"(")
			callEnd := callStart + len(test.raw) + 1
			definitions := []uci.IndexAdmissionDefinition{
				{LocalSymbolKey: "function:helper", Kind: "function", SymbolKey: "javascript:function:helper", Span: uci.IndexSpan{ByteStart: 0, ByteEnd: int64(strings.Index(test.source, "\n"))}},
				{LocalSymbolKey: owner, Kind: "function", SymbolKey: "javascript:" + owner, Span: uci.IndexSpan{ByteStart: int64(callerStart), ByteEnd: int64(len(test.source))}},
			}
			if test.ambiguous {
				definitions = append(definitions, uci.IndexAdmissionDefinition{LocalSymbolKey: "const:helper", Kind: "const", SymbolKey: "javascript:const:helper"})
			}
			files := []uciPreparedAdmissionFile{{
				path: "caller.js", membership: uci.IndexAdmissionMembership{PathKey: "caller.js", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &artifactID},
				artifact: &uci.IndexAdmissionArtifact{
					ArtifactID: artifactID, Body: []byte(test.source), Profile: uci.IndexAdmissionArtifactProfile{Language: uci.IndexAdmissionLanguageJavaScript}, Definitions: definitions,
					References: []uci.IndexAdmissionReference{{Kind: test.kind, SiteKey: test.site + "@" + strconv.Itoa(callStart) + ":" + strconv.Itoa(callEnd), OwnerSymbolKey: &owner, RawTarget: test.raw, Relation: uci.IndexRelation("calls"), Span: uci.IndexSpan{ByteStart: int64(callStart), ByteEnd: int64(callEnd)}}},
				},
			}}
			unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			if test.kind == "call" {
				require.Equal(t, uint64(1), unresolved)
			}
			require.Empty(t, files[0].edges)
		})
	}
}
