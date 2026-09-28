package codeintel

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

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
					ArtifactID: artifactID, Body: []byte(test.body), Status: uci.IndexAdmissionArtifactComplete, Profile: uci.IndexAdmissionArtifactProfile{Language: test.language},
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
			files[0].edges = nil
			files[0].artifact.Status = uci.IndexAdmissionArtifactPartial
			unresolved, err = uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			require.Equal(t, uint64(1), unresolved)
			require.Empty(t, files[0].edges)
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
					ArtifactID: artifactID, Body: []byte(test.source), Status: uci.IndexAdmissionArtifactComplete, Profile: uci.IndexAdmissionArtifactProfile{Language: uci.IndexAdmissionLanguageJavaScript}, Definitions: definitions,
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

func TestUCIPreparedIndexLeavesReassignedSameFileCallsUnresolved(t *testing.T) {
	for _, test := range []struct {
		name, path, source, write string
		language                  uci.IndexAdmissionLanguage
		resolved                  bool
	}{
		{"javascript", "calls.js", "export function helper(x) { return x; }\nexport function caller(x) { helper = x; return helper(1); }", "helper = x", uci.IndexAdmissionLanguageJavaScript, false},
		{"typescript", "calls.ts", "export function helper(x: number): number { return x; }\nexport function caller(x: any) { helper = x; return helper(1); }", "helper = x", uci.IndexAdmissionLanguageTypeScript, false},
		{"tsx", "calls.tsx", "export function helper(x: number): number { return x; }\nexport function caller(x: any) { helper = x; return <span>{helper(1)}</span>; }", "helper = x", uci.IndexAdmissionLanguageTSX, false},
		{"compound write", "calls.js", "export function helper(x) { return x; }\nexport function caller(x) { helper += x; return helper(1); }", "helper += x", uci.IndexAdmissionLanguageJavaScript, false},
		{"destructuring write", "calls.js", "export function helper(x) { return x; }\nexport function caller(x) { ({helper} = x); return helper(1); }", "{helper} = x", uci.IndexAdmissionLanguageJavaScript, false},
		{"comment is not write", "calls.js", "export function helper(x) { return x; }\nexport function caller(x) { /* helper = x */ return helper(1); }", "", uci.IndexAdmissionLanguageJavaScript, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			artifactID := "11111111-1111-4111-8111-111111111111"
			owner := "function:caller"
			callerStart := strings.Index(test.source, "export function caller")
			callStart := strings.LastIndex(test.source, "helper(")
			callEnd := callStart + len("helper(")
			references := []uci.IndexAdmissionReference{{Kind: "call", SiteKey: "call:helper@" + strconv.Itoa(callStart) + ":" + strconv.Itoa(callEnd), OwnerSymbolKey: &owner, Relation: uci.IndexRelation("calls"), Span: uci.IndexSpan{ByteStart: int64(callStart), ByteEnd: int64(callEnd)}}}
			if test.write != "" {
				writeStart := strings.Index(test.source, test.write) + strings.Index(test.write, "helper")
				references = append(references, uci.IndexAdmissionReference{Kind: "binding_write", SiteKey: "binding_write:helper@" + strconv.Itoa(writeStart) + ":" + strconv.Itoa(writeStart+len("helper")), RawTarget: "helper", Relation: uci.IndexRelation("references"), Span: uci.IndexSpan{ByteStart: int64(writeStart), ByteEnd: int64(writeStart + len("helper"))}})
			}
			files := []uciPreparedAdmissionFile{{
				path: test.path, membership: uci.IndexAdmissionMembership{PathKey: test.path, State: uci.IndexAdmissionMembershipPresent, ArtifactID: &artifactID},
				artifact: &uci.IndexAdmissionArtifact{
					ArtifactID: artifactID, Body: []byte(test.source), Status: uci.IndexAdmissionArtifactComplete, Profile: uci.IndexAdmissionArtifactProfile{Language: test.language},
					Definitions: []uci.IndexAdmissionDefinition{
						{LocalSymbolKey: "function:helper", Kind: "function", Span: uci.IndexSpan{ByteStart: 0, ByteEnd: int64(callerStart - 1)}},
						{LocalSymbolKey: owner, Kind: "function", Span: uci.IndexSpan{ByteStart: int64(callerStart), ByteEnd: int64(len(test.source))}},
					},
					References: references,
				},
			}}
			unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			if test.resolved {
				require.Zero(t, unresolved)
				require.Len(t, files[0].edges, 1)
			} else {
				require.Equal(t, uint64(1), unresolved)
				require.Empty(t, files[0].edges)
			}
		})
	}
}

func TestUCIPreparedIndexBuiltParserDoesNotResolveReboundLocalCalls(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "uci-parser")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	command := exec.Command("go", "build", "-o", executable, "./tools/uci-parser")
	command.Dir = filepath.Clean(filepath.Join("..", "..", ".."))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build isolated parser child: %v: %s", err, output)
	}
	parser, err := uci.NewTreeSitterWorker(uci.TreeSitterWorkerConfig{
		ExecutablePath: executable, ExpectedBundleDigest: uci.TreeSitterBundleDigest(),
		MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	for _, test := range []struct {
		name, source string
		language     uci.TreeSitterLanguage
		resolved     bool
	}{
		{"simple javascript", "export function calibrateInfraredPrism(pulseCount) { return pulseCount; }\nexport function guideCometOptics(pulseCount) { return calibrateInfraredPrism(pulseCount); }", uci.TreeSitterLanguageJavaScript, true},
		{"rebound javascript", "export function calibrateInfraredPrism(pulseCount) { return pulseCount; }\nexport function guideCometOptics(pulseCount) { calibrateInfraredPrism = pulseCount; return calibrateInfraredPrism(pulseCount); }", uci.TreeSitterLanguageJavaScript, false},
		{"rebound typescript", "export function calibrateInfraredPrism(pulseCount: number) { return pulseCount; }\nexport function guideCometOptics(pulseCount: any) { calibrateInfraredPrism += pulseCount; return calibrateInfraredPrism(pulseCount); }", uci.TreeSitterLanguageTypeScript, false},
		{"rebound tsx", "export function calibrateInfraredPrism(pulseCount: number) { return pulseCount; }\nexport function guideCometOptics(pulseCount: any) { ({calibrateInfraredPrism} = pulseCount); return <span>{calibrateInfraredPrism(pulseCount)}</span>; }", uci.TreeSitterLanguageTSX, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.source)
			parsed, err := parser.Parse(context.Background(), uci.TreeSitterParseRequest{Language: test.language, ProfileKey: "binding-write-integration/v3", Source: body})
			require.NoError(t, err)
			require.Equal(t, uci.IndexCoverageComplete, parsed.Coverage, "%+v", parsed.Diagnostics)
			profile, err := uci.TreeSitterIndexAdmissionArtifactProfile(test.language, uci.TreeSitterBundleDigest())
			require.NoError(t, err)
			artifact, err := uci.NewIndexAdmissionArtifactFromTreeSitter("11111111-1111-4111-8111-111111111111", profile, body, parsed)
			require.NoError(t, err)
			id := artifact.ArtifactID
			files := []uciPreparedAdmissionFile{{path: "comet.js", membership: uci.IndexAdmissionMembership{PathKey: "comet.js", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &id}, artifact: &artifact}}
			unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			if test.resolved {
				require.Zero(t, unresolved)
				require.Len(t, files[0].edges, 1)
				require.Equal(t, uci.IndexResolutionState("resolved"), files[0].edges[0].ResolutionState)
				require.Equal(t, "function:calibrateInfraredPrism", *files[0].edges[0].Target.SymbolKey)
			} else {
				require.Equal(t, uint64(1), unresolved)
				require.Empty(t, files[0].edges)
			}
		})
	}
}
