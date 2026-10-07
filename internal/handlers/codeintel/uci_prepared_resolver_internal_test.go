package codeintel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
		{"spaced exported javascript", "export function calibrateInfraredPrism (pulseCount) { return pulseCount; }\nexport function guideCometOptics(pulseCount) { return calibrateInfraredPrism(pulseCount); }", uci.TreeSitterLanguageJavaScript, true},
		{"async exported javascript", "export function calibrateInfraredPrism(pulseCount) { return pulseCount; }\nexport async function guideCometOptics(pulseCount) { return calibrateInfraredPrism(pulseCount); }", uci.TreeSitterLanguageJavaScript, true},
		{"spaced exported typescript", "export function calibrateInfraredPrism (pulseCount: number) { return pulseCount; }\nexport function guideCometOptics(pulseCount: number) { return calibrateInfraredPrism(pulseCount); }", uci.TreeSitterLanguageTypeScript, true},
		{"async exported tsx", "export function calibrateInfraredPrism(pulseCount: number) { return pulseCount; }\nexport async function guideCometOptics(pulseCount: number) { return <span>{calibrateInfraredPrism(pulseCount)}</span>; }", uci.TreeSitterLanguageTSX, true},
		{"for-of binding javascript", "export function calibrateInfraredPrism(pulseCount) { return pulseCount; }\nexport function guideCometOptics(pulseCount) { for (calibrateInfraredPrism of pulseCount) {} return calibrateInfraredPrism(pulseCount); }", uci.TreeSitterLanguageJavaScript, false},
		{"for-in binding typescript", "export function calibrateInfraredPrism(pulseCount: number) { return pulseCount; }\nexport function guideCometOptics(pulseCount: any) { for (calibrateInfraredPrism in pulseCount) {} return calibrateInfraredPrism(pulseCount); }", uci.TreeSitterLanguageTypeScript, false},
		{"for-of binding tsx", "export function calibrateInfraredPrism(pulseCount: number) { return pulseCount; }\nexport function guideCometOptics(pulseCount: any) { for (calibrateInfraredPrism of pulseCount) {} return <span>{calibrateInfraredPrism(pulseCount)}</span>; }", uci.TreeSitterLanguageTSX, false},
		{"rebound javascript", "export function calibrateInfraredPrism(pulseCount) { return pulseCount; }\nexport function guideCometOptics(pulseCount) { calibrateInfraredPrism = pulseCount; return calibrateInfraredPrism(pulseCount); }", uci.TreeSitterLanguageJavaScript, false},
		{"rebound typescript", "export function calibrateInfraredPrism(pulseCount: number) { return pulseCount; }\nexport function guideCometOptics(pulseCount: any) { calibrateInfraredPrism += pulseCount; return calibrateInfraredPrism(pulseCount); }", uci.TreeSitterLanguageTypeScript, false},
		{"rebound tsx", "export function calibrateInfraredPrism(pulseCount: number) { return pulseCount; }\nexport function guideCometOptics(pulseCount: any) { ({calibrateInfraredPrism} = pulseCount); return <span>{calibrateInfraredPrism(pulseCount)}</span>; }", uci.TreeSitterLanguageTSX, false},
		{"rebound local javascript", "export function calibrateInfraredPrism(n) { return n; }\nexport function guideCometOptics(n) { let helper = calibrateInfraredPrism; helper = n; return helper(n); }", uci.TreeSitterLanguageJavaScript, false},
		{"rebound local typescript", "export function calibrateInfraredPrism(n: number) { return n; }\nexport function guideCometOptics(n: any) { let helper = calibrateInfraredPrism; helper! = n; return helper(n); }", uci.TreeSitterLanguageTypeScript, false},
		{"rebound local tsx", "export function calibrateInfraredPrism(n: number) { return n; }\nexport function guideCometOptics(n: any) { let helper = calibrateInfraredPrism; ({helper} = n); return helper(n); }", uci.TreeSitterLanguageTSX, false},
		{"shadowed sibling write", "export function calibrateInfraredPrism(n) { return n; }\nexport function guideCometOptics(n) { return calibrateInfraredPrism(n); }\nfunction mutate(n) { let calibrateInfraredPrism; calibrateInfraredPrism = n; }", uci.TreeSitterLanguageJavaScript, true},
		{"generator sibling local shadow javascript", "export function helper() { return 1; }\nexport function caller(){return helper();}\nfunction* mutate(x){let helper;helper=x;}", uci.TreeSitterLanguageJavaScript, true},
		{"generator sibling local shadow typescript", "export function helper() { return 1; }\nexport function caller(){return helper();}\nfunction* mutate(x: unknown){let helper;helper=x;}", uci.TreeSitterLanguageTypeScript, true},
		{"generator sibling parameter shadow", "export function helper() { return 1; }\nexport function caller(){return helper();}\nfunction* mutate(helper){helper=1;}", uci.TreeSitterLanguageJavaScript, true},
		{"generator sibling unshadowed write", "export function helper() { return 1; }\nexport function caller(){return helper();}\nfunction* mutate(x){helper=x;}", uci.TreeSitterLanguageJavaScript, false},
		{"generator comment is not a declaration", "export function helper() { return 1; }\nexport function caller(){return helper();}\nfunction* mutate(x){/* let helper; */ helper=x;}", uci.TreeSitterLanguageJavaScript, false},
		{"later caller write can affect subsequent invocation", "export function calibrateInfraredPrism(n) { return n; }\nexport function guideCometOptics(n) { const result = calibrateInfraredPrism(n); calibrateInfraredPrism = n; return result; }", uci.TreeSitterLanguageJavaScript, false},
		{"unshadowed sibling write", "export function calibrateInfraredPrism(n) { return n; }\nexport function guideCometOptics(n) { return calibrateInfraredPrism(n); }\nfunction mutate(n) { calibrateInfraredPrism = n; }", uci.TreeSitterLanguageJavaScript, false},
		{"nested block declaration cannot shadow sibling write", "export function calibrateInfraredPrism(n) { return n; }\nexport function guideCometOptics(n) { return calibrateInfraredPrism(n); }\nfunction mutate(n) { if (n) { let calibrateInfraredPrism; } calibrateInfraredPrism = n; }", uci.TreeSitterLanguageJavaScript, false},
		{"nested block local shadow", "export function helper() {}; export function caller(){return helper()}; function mutate(x){ if(x){let helper; helper=x;} }", uci.TreeSitterLanguageJavaScript, true},
		{"default object property is not a parameter shadow", "export function helper() {}; export function caller(){return helper()}; function mutate(x = {a:0, helper:1}){helper=x;}", uci.TreeSitterLanguageJavaScript, false},
		{"parameter shadow in sibling", "export function calibrateInfraredPrism(n) { return n; }\nexport function guideCometOptics(n) { return calibrateInfraredPrism(n); }\nfunction mutate(calibrateInfraredPrism) { calibrateInfraredPrism = null; }", uci.TreeSitterLanguageJavaScript, true},
		{"destructured sibling parameter shadow", "export function helper() { return 1; }\nexport function caller(){return helper();}\nfunction mutate({helper}){helper=1;}", uci.TreeSitterLanguageJavaScript, true},
		{"arrow sibling parameter shadow", "export function helper() { return 1; }\nexport function caller(){return helper();}\nconst mutate = helper => {helper=1;};", uci.TreeSitterLanguageJavaScript, true},
		{"arrow sibling unshadowed write", "export function helper() { return 1; }\nexport function caller(){return helper();}\nconst mutate = x => {helper=x;};", uci.TreeSitterLanguageJavaScript, false},
		{"block write does not affect module call in same owner", "export function helper() { return 1; }\nexport function caller(n){ if(n){let helper; helper=n;} return helper();}", uci.TreeSitterLanguageJavaScript, true},
		{"var write escapes block to function scope", "export function helper() { return 1; }\nexport function caller(){return helper();}\nfunction mutate(x){if(x){var helper;}helper=x;}", uci.TreeSitterLanguageJavaScript, true},
		{"loop local write does not rebind module", "export function helper() { return 1; }\nexport function caller(){for(let helper of []){helper=1;}return helper();}", uci.TreeSitterLanguageJavaScript, true},
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
			files := []uciPreparedAdmissionFile{{path: "comet.js", membership: uci.IndexAdmissionMembership{PathKey: "comet.js", DisplayPath: "comet.js", Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &id}, artifact: &artifact}}
			unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			if test.resolved {
				require.Zero(t, unresolved)
				require.Len(t, files[0].edges, 1)
				require.Equal(t, uci.IndexResolutionState("resolved"), files[0].edges[0].ResolutionState)
				target := "calibrateInfraredPrism"
				if strings.Contains(test.source, "export function helper()") {
					target = "helper"
				}
				require.Equal(t, "function:"+target, *files[0].edges[0].Target.SymbolKey)
			} else {
				require.Equal(t, uint64(1), unresolved)
				require.Len(t, files[0].edges, 1)
				require.Nil(t, files[0].edges[0].Target)
				require.Equal(t, uci.IndexEvidenceKind("unresolved"), files[0].edges[0].EvidenceKind)
				require.Equal(t, uci.IndexResolutionState("unresolved"), files[0].edges[0].ResolutionState)
				require.Equal(t, uci.IndexRelation("calls"), files[0].edges[0].Relation)
			}
			frames, _, err := uciPreparedPackFrames("44444444-4444-4444-8444-444444444444", files)
			require.NoError(t, err)
			require.NoError(t, uci.ValidateIndexAdmissionFrames(frames))
			require.Len(t, frames, 1)
			part, err := frames[0].PublicationPart()
			require.NoError(t, err)
			require.Len(t, part.EdgeReplacements, 1)
			require.Len(t, part.EdgeReplacements[0].Edges, 1)
			require.Equal(t, files[0].edges[0].ResolutionState, part.EdgeReplacements[0].Edges[0].ResolutionState)
			require.NotNil(t, part.EdgeReplacements[0].Edges[0].Evidence.ReferenceSiteID)
		})
	}
}

func TestUCIPreparedIndexScopedDeclarationsPreserveDirectModuleCalls(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	body, err := os.ReadFile(filepath.Join(repoRoot, "ui", "src", "utils", "formatters.ts"))
	require.NoError(t, err)
	executable := filepath.Join(t.TempDir(), "uci-parser")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	command := exec.Command("go", "build", "-p=1", "-o", executable, "./tools/uci-parser")
	command.Dir = repoRoot
	command.Env = append(os.Environ(), "CGO_ENABLED=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build current isolated parser: %v: %s", err, output)
	}
	parser, err := uci.NewTreeSitterWorker(uci.TreeSitterWorkerConfig{
		ExecutablePath: executable, ExpectedBundleDigest: uci.TreeSitterBundleDigest(),
		MaxInputBytes: 1 << 20, MaxOutputBytes: 1 << 20, Timeout: 5 * time.Second,
	})
	require.NoError(t, err)
	profile, err := uci.TreeSitterIndexAdmissionArtifactProfile(uci.TreeSitterLanguageTypeScript, uci.TreeSitterBundleDigest())
	require.NoError(t, err)
	for _, test := range []struct {
		name     string
		body     []byte
		caller   string
		callee   string
		coverage uci.IndexCoverageState
		resolved bool
	}{
		{name: "real formatter repeated function locals", body: body, caller: "safeDateFormat", callee: "formatRelativeTime", coverage: uci.IndexCoverageComplete, resolved: true},
		{name: "independent sibling block locals", body: []byte("export function helper(){return 1;} export function caller(){if(true){const value=1;}if(false){const value=2;}return helper();}"), caller: "caller", callee: "helper", coverage: uci.IndexCoverageComplete, resolved: true},
		{name: "nested sibling declarations", body: []byte("export function helper(){return 1;} export function caller(){function nested(){const value=1;return value;}return helper();} function sibling(){function nested(){const value=2;return value;}return nested();}"), caller: "caller", callee: "helper", coverage: uci.IndexCoverageComplete, resolved: true},
		{name: "actual module collision stays partial", body: []byte("export function helper(){return 1;} export function helper(){return 2;} export function caller(){return helper();}"), caller: "caller", callee: "helper", coverage: uci.IndexCoveragePartial},
		{name: "local shadow cannot become module call", body: []byte("export function helper(){return 1;} export function caller(){const helper=()=>2;return helper();}"), caller: "caller", callee: "helper", coverage: uci.IndexCoverageComplete},
	} {
		t.Run(test.name, func(t *testing.T) {
			parsed, err := parser.Parse(context.Background(), uci.TreeSitterParseRequest{
				Language: uci.TreeSitterLanguageTypeScript, ProfileKey: "scoped-declaration-call-regression/v1", Source: test.body,
			})
			require.NoError(t, err)
			require.Equal(t, test.coverage, parsed.Coverage, "%+v", parsed.Diagnostics)
			if test.name == "real formatter repeated function locals" {
				locals := make(map[string]struct{})
				for _, definition := range parsed.Definitions {
					if definition.Name == "date" {
						require.Contains(t, definition.LocalKey, "#scope:")
						locals[definition.LocalKey] = struct{}{}
					}
				}
				require.Len(t, locals, 2, "sibling local declarations must not collide or disappear")
			}
			artifact, err := uci.NewIndexAdmissionArtifactFromTreeSitter("11111111-1111-4111-8111-111111111111", profile, test.body, parsed)
			require.NoError(t, err)
			id := artifact.ArtifactID
			files := []uciPreparedAdmissionFile{{path: "formatter.ts", membership: uci.IndexAdmissionMembership{PathKey: "formatter.ts", DisplayPath: "formatter.ts", Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &id}, artifact: &artifact}}
			_, err = uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			frames, _, err := uciPreparedPackFrames("44444444-4444-4444-8444-444444444444", files)
			require.NoError(t, err)
			require.NoError(t, uci.ValidateIndexAdmissionFrames(frames))
			require.Len(t, frames, 1)
			part, err := frames[0].PublicationPart()
			require.NoError(t, err)
			var matches []uci.IndexEdge
			for _, replacement := range part.EdgeReplacements {
				for _, edge := range replacement.Edges {
					if edge.Relation == uci.IndexRelation("calls") && edge.SourceSymbolKey != nil && *edge.SourceSymbolKey == "function:"+test.caller && edge.Target != nil && edge.Target.SymbolKey != nil && *edge.Target.SymbolKey == "function:"+test.callee && edge.ResolutionState == uci.IndexResolutionState("resolved") {
						matches = append(matches, edge)
					}
				}
			}
			if test.resolved {
				require.Len(t, matches, 1, "supported call must survive actual parsing, resolution and publication")
				require.Equal(t, uci.IndexEvidenceKind("resolved"), matches[0].EvidenceKind)
				require.NotNil(t, matches[0].Evidence.ReferenceSiteID)
				t.Logf("source_bytes=%d coverage=%s caller=%s callee=%s published_resolved_calls=%d bundle=%s", len(test.body), parsed.Coverage, test.caller, test.callee, len(matches), parsed.BundleDigest)
			} else {
				require.Empty(t, matches, "partial or shadowed bindings must not emit a guessed module call")
			}
		})
	}
}

func TestUCIPreparedIndexBuiltParserResolvesExportListCallsInPackedGraph(t *testing.T) {
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
		name, path, source string
		language           uci.TreeSitterLanguage
		resolved           bool
	}{
		{"javascript", "calls.js", "function helper() { return 1; }\nfunction caller() { return helper(); }\nexport { helper, caller };", uci.TreeSitterLanguageJavaScript, true},
		{"typescript", "calls.ts", "function helper(n: number) { return n; }\nfunction caller() { return helper(1); }\nexport { helper as exposedHelper, caller as exposedCaller };", uci.TreeSitterLanguageTypeScript, true},
		{"tsx", "calls.tsx", "function helper() { return 1; }\nfunction caller() { return <span>{helper()}</span>; }\nexport { helper, caller };", uci.TreeSitterLanguageTSX, true},
		{"exports before definitions", "calls.js", "export { helper, caller };\nfunction helper() { return 1; }\nfunction caller() { return helper(); }", uci.TreeSitterLanguageJavaScript, true},
		{"direct and list exports", "calls.js", "export function helper() { return 1; }\nfunction caller() { return helper(); }\nexport { caller };", uci.TreeSitterLanguageJavaScript, true},
		{"default-only target", "calls.js", "export default function helper() { return 1; }\nfunction caller() { return helper(); }\nexport { caller };", uci.TreeSitterLanguageJavaScript, true},
		{"named default javascript", "calls.js", "export default function helper(){}; export function caller(){helper()}", uci.TreeSitterLanguageJavaScript, true},
		{"named default typescript", "calls.ts", "export default function helper(){}; export function caller(){helper()}", uci.TreeSitterLanguageTypeScript, true},
		{"named default tsx", "calls.tsx", "export default function helper(){}; export function caller(){return <span>{helper()}</span>;}", uci.TreeSitterLanguageTSX, true},
		{"default caller javascript", "calls.js", "export function helper(){}; export default function caller(){helper()}", uci.TreeSitterLanguageJavaScript, true},
		{"default caller typescript", "calls.ts", "export function helper(){}; export default function caller(){helper()}", uci.TreeSitterLanguageTypeScript, true},
		{"default caller tsx", "calls.tsx", "export function helper(){}; export default function caller(){return <span>{helper()}</span>;}", uci.TreeSitterLanguageTSX, true},
		{"identifier default javascript", "calls.js", "function helper(){}; export default helper; export function caller(){helper()}", uci.TreeSitterLanguageJavaScript, true},
		{"identifier default typescript", "calls.ts", "function helper(){}; export default helper; export function caller(){helper()}", uci.TreeSitterLanguageTypeScript, true},
		{"identifier default tsx", "calls.tsx", "function helper(){}; export default helper; export function caller(){return <span>{helper()}</span>;}", uci.TreeSitterLanguageTSX, true},
		{"identifier default caller javascript", "calls.js", "export function helper(){}; function caller(){helper()}; export default caller", uci.TreeSitterLanguageJavaScript, true},
		{"identifier default caller typescript", "calls.ts", "export function helper(){}; function caller(){helper()}; export default caller", uci.TreeSitterLanguageTypeScript, true},
		{"identifier default caller tsx", "calls.tsx", "export function helper(){}; function caller(){return <span>{helper()}</span>;}; export default caller", uci.TreeSitterLanguageTSX, true},
		{"forward identifier default", "calls.ts", "export default helper; function helper(){}; export function caller(){helper()}", uci.TreeSitterLanguageTypeScript, true},
		{"default call expression", "calls.js", "function helper(){}; export default helper(); export function caller(){helper()}", uci.TreeSitterLanguageJavaScript, false},
		{"default object expression", "calls.tsx", "function helper(){}; export default {helper}; export function caller(){helper()}", uci.TreeSitterLanguageTSX, false},
		{"anonymous default target", "calls.js", "export default function(){}; export function caller(){helper()}", uci.TreeSitterLanguageJavaScript, false},
		{"default function expression", "calls.js", "export default (function helper(){}); export function caller(){helper()}", uci.TreeSitterLanguageJavaScript, false},
		{"default member call", "calls.ts", "export default function helper(){}; export function caller(n: any){n.helper()}", uci.TreeSitterLanguageTypeScript, false},
		{"default ambiguous binding", "calls.js", "export default function helper(){}; const helper = 2; export function caller(){helper()}", uci.TreeSitterLanguageJavaScript, false},
		{"default rebound callee", "calls.tsx", "export default function helper(){}; export function caller(n: any){helper=n; return <span>{helper()}</span>;}", uci.TreeSitterLanguageTSX, false},
		{"default shadowed callee", "calls.ts", "export default function helper(){}; export function caller(helper: () => void){helper()}", uci.TreeSitterLanguageTypeScript, false},
		{"non-exported callee", "calls.js", "function helper() { return 1; }\nfunction caller() { return helper(); }\nexport { caller };", uci.TreeSitterLanguageJavaScript, false},
		{"non-exported caller", "calls.js", "function helper() { return 1; }\nfunction caller() { return helper(); }\nexport { helper };", uci.TreeSitterLanguageJavaScript, false},
		{"type-only export", "calls.ts", "function helper() { return 1; }\nfunction caller() { return helper(); }\nexport type { helper, caller };", uci.TreeSitterLanguageTypeScript, false},
		{"type-only specifier", "calls.ts", "function helper() { return 1; }\nfunction caller() { return helper(); }\nexport { type helper, caller };", uci.TreeSitterLanguageTypeScript, false},
		{"ambiguous binding", "calls.js", "function helper() { return 1; }\nconst helper = 2;\nfunction caller() { return helper(); }\nexport { helper, caller };", uci.TreeSitterLanguageJavaScript, false},
		{"reassigned callee", "calls.js", "function helper() { return 1; }\nfunction caller(n) { helper = n; return helper(); }\nexport { helper, caller };", uci.TreeSitterLanguageJavaScript, false},
		{"dynamic eval", "calls.js", "function helper() { return 1; }\nfunction caller() { eval('helper = null'); return helper(); }\nexport { helper, caller };", uci.TreeSitterLanguageJavaScript, false},
		{"nested caller", "calls.js", "function helper() { return 1; }\nfunction caller() { function nested() { return helper(); } return nested(); }\nexport { helper, caller };", uci.TreeSitterLanguageJavaScript, false},
		{"nested target", "calls.js", "function outer() { function helper() { return 1; } }\nfunction caller() { return helper(); }\nexport { helper, caller };", uci.TreeSitterLanguageJavaScript, false},
		{"generator", "calls.js", "function* helper() { yield 1; }\nfunction* caller() { yield helper(); }\nexport { helper, caller };", uci.TreeSitterLanguageJavaScript, true},
		{"using resource shadows module", "calls.js", "export function helper(){}; export function caller(){ using helper = resource; return helper(); }", uci.TreeSitterLanguageJavaScript, false},
		{"using resource outside its block", "calls.js", "export function helper(){}; export function caller(){ {using helper = resource;} return helper(); }", uci.TreeSitterLanguageJavaScript, true},
		{"await using shadows module", "calls.js", "export function helper(){}; export async function caller(){ await using helper = resource; return helper(); }", uci.TreeSitterLanguageJavaScript, false},
		{"using resource in sibling", "calls.js", "export function helper(){}; export function caller(){return helper();} function other(){using helper = resource;}", uci.TreeSitterLanguageJavaScript, true},
		{"parameter binding javascript", "calls.js", "export function helper(){}; export function caller(helper){ return helper(); }", uci.TreeSitterLanguageJavaScript, false},
		{"parameter binding typescript", "calls.ts", "export function helper(){}; export function caller(helper: () => void){ return helper(); }", uci.TreeSitterLanguageTypeScript, false},
		{"parameter binding tsx", "calls.tsx", "export function helper(){}; export function caller(helper: () => void){ return <span>{helper()}</span>; }", uci.TreeSitterLanguageTSX, false},
		{"destructured parameter binding", "calls.js", "export function helper(){}; export function caller({helper}){ return helper(); }", uci.TreeSitterLanguageJavaScript, false},
		{"nested arrow call", "calls.js", "export function helper(){}; export function caller(){ const nested = () => helper(); return nested; }", uci.TreeSitterLanguageJavaScript, false},
		{"arrow outside call scope", "calls.js", "export function helper(){}; export function caller(){ const nested = () => 1; return helper(); }", uci.TreeSitterLanguageJavaScript, true},
		{"catch parameter shadows helper", "calls.js", "export function helper(){}; export function caller(){ try {} catch(helper){ return helper(); } }", uci.TreeSitterLanguageJavaScript, false},
		{"optional call", "calls.js", "export function helper(){}; export function caller(){ return helper?.(); }", uci.TreeSitterLanguageJavaScript, false},
		{"optional typescript call", "calls.ts", "export function helper(){}; export function caller(){ return helper?.(); }", uci.TreeSitterLanguageTypeScript, false},
		{"optional tsx call", "calls.tsx", "export function helper(){}; export function caller(){ return <span>{helper?.()}</span>; }", uci.TreeSitterLanguageTSX, false},
		{"dynamic member call", "calls.ts", "export function helper(){}; export function caller(n: any){ return n.helper(); }", uci.TreeSitterLanguageTypeScript, false},
		{"dynamic constructor", "calls.tsx", "export function helper(){}; export function caller(n: any){ return new n.helper(); }", uci.TreeSitterLanguageTSX, false},
		{"unknown call", "calls.js", "export function helper(){}; export function caller(){ return missing(); }", uci.TreeSitterLanguageJavaScript, false},
		{"non-call reference", "calls.js", "export function helper(){}; export function caller(){ return helper; }", uci.TreeSitterLanguageJavaScript, false},
		{"sibling eval with local binding javascript", "calls.js", "export function helper(){}; export function caller(){return helper();} function dynamic(s){let helper;eval(s);}", uci.TreeSitterLanguageJavaScript, true},
		{"sibling eval with local binding typescript", "calls.ts", "export function helper(){}; export function caller(){return helper();} function dynamic(s: string){let helper;eval(s);}", uci.TreeSitterLanguageTypeScript, true},
		{"sibling eval with local binding tsx", "calls.tsx", "export function helper(){}; export function caller(){return <span>{helper()}</span>;} function dynamic(s: string){let helper;eval(s);}", uci.TreeSitterLanguageTSX, true},
		{"sibling eval can rebind module", "calls.js", "export function helper(){}; export function caller(){return helper();} function dynamic(s){eval(s);}", uci.TreeSitterLanguageJavaScript, false},
		{"with scope is dynamic", "calls.js", "export function helper(){}; export function caller(n){ with(n){ return helper(); } }", uci.TreeSitterLanguageJavaScript, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.source)
			parsed, err := parser.Parse(context.Background(), uci.TreeSitterParseRequest{Language: test.language, ProfileKey: "export-list-graph/v1", Source: body})
			require.NoError(t, err)
			if !strings.Contains(test.name, "ambiguous binding") {
				require.Equal(t, uci.IndexCoverageComplete, parsed.Coverage, "%+v", parsed.Diagnostics)
			}
			profile, err := uci.TreeSitterIndexAdmissionArtifactProfile(test.language, uci.TreeSitterBundleDigest())
			require.NoError(t, err)
			artifact, err := uci.NewIndexAdmissionArtifactFromTreeSitter("11111111-1111-4111-8111-111111111111", profile, body, parsed)
			require.NoError(t, err)
			id := artifact.ArtifactID
			files := []uciPreparedAdmissionFile{{path: test.path, membership: uci.IndexAdmissionMembership{PathKey: test.path, DisplayPath: test.path, Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &id}, artifact: &artifact}}
			_, err = uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			frames, _, err := uciPreparedPackFrames("44444444-4444-4444-8444-444444444444", files)
			require.NoError(t, err)
			require.NoError(t, uci.ValidateIndexAdmissionFrames(frames))
			var resolvedCalls int
			for _, frame := range frames {
				part, err := frame.PublicationPart()
				require.NoError(t, err)
				for _, replacement := range part.EdgeReplacements {
					for _, edge := range replacement.Edges {
						if edge.Relation == uci.IndexRelation("calls") && edge.ResolutionState == uci.IndexResolutionState("resolved") {
							resolvedCalls++
							require.Equal(t, "function:caller", *edge.SourceSymbolKey)
							require.Equal(t, "function:helper", *edge.Target.SymbolKey)
							require.Equal(t, "tree-sitter-direct-local-call/v1", edge.Evidence.RuleKey)
						}
					}
				}
			}
			if test.resolved {
				require.Equal(t, 1, resolvedCalls)
			} else {
				require.Zero(t, resolvedCalls)
			}
		})
	}
}

func TestUCIPreparedIndexBuiltParserHandlesCallTriviaAndAssertionWrite(t *testing.T) {
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
		written      bool
	}{
		{"javascript whitespace", "export function helper(n) { return n; }\nexport function caller(n) { return helper (n); }", uci.TreeSitterLanguageJavaScript, true, false},
		{"javascript comment", "export function helper(n) { return n; }\nexport function caller(n) { return helper /* trivia */ (n); }", uci.TreeSitterLanguageJavaScript, true, false},
		{"typescript whitespace", "export function helper(n: number) { return n; }\nexport function caller(n: number) { return helper (n); }", uci.TreeSitterLanguageTypeScript, true, false},
		{"typescript comment", "export function helper(n: number) { return n; }\nexport function caller(n: number) { return helper /* trivia */ (n); }", uci.TreeSitterLanguageTypeScript, true, false},
		{"generator javascript", "export function* helper(n) { yield n; }\nexport function* caller(n) { yield helper(n); }", uci.TreeSitterLanguageJavaScript, true, false},
		{"generator typescript", "export function* helper(n: number) { yield n; }\nexport function* caller(n: number) { yield helper(n); }", uci.TreeSitterLanguageTypeScript, true, false},
		{"generator tsx", "export function* helper(n: number) { yield n; }\nexport function* caller(n: number) { yield helper(n); }", uci.TreeSitterLanguageTSX, true, false},
		{"generator assignment", "export function* helper(n) { yield n; }\nexport function* caller(n) { helper = n; yield helper(n); }", uci.TreeSitterLanguageJavaScript, false, true},
		{"typescript assertion write", "export function helper(n: number) { return n; }\nexport function caller(n: any) { (helper as any) = n; return helper(n); }", uci.TreeSitterLanguageTypeScript, false, true},
		{"typescript angle assertion write", "export function helper(n: number) { return n; }\nexport function caller(n: any) { (<any>helper) = n; return helper(n); }", uci.TreeSitterLanguageTypeScript, false, true},
		{"typescript satisfies write", "export function helper(n: number) { return n; }\nexport function caller(n: any) { (helper satisfies any) = n; return helper(n); }", uci.TreeSitterLanguageTypeScript, false, true},
		{"javascript parameter string", "export function helper(){}; export function caller(n = \"helper\"){ return helper(); }", uci.TreeSitterLanguageJavaScript, true, false},
		{"javascript parameter comment", "export function helper(){}; export function caller(/* helper */ n){ return helper(); }", uci.TreeSitterLanguageJavaScript, true, false},
		{"typescript parameter literal type", "export function helper(){}; export function caller(n: \"helper\" = \"helper\"){ return helper(); }", uci.TreeSitterLanguageTypeScript, true, false},
		{"tsx parameter comment and default", "export function helper(){}; export function caller(n: string /* helper */ = \"helper\"){ return <span>{helper()}</span>; }", uci.TreeSitterLanguageTSX, true, false},
		{"javascript body string and comment", "export function helper(){}; export function caller(){const note = \"function => catch with (\"; /* function */ return helper();}", uci.TreeSitterLanguageJavaScript, true, false},
		{"typescript generic functions", "export function helper<T>(n: T){return n;} export function caller<T>(n: T){return helper<T>(n);}", uci.TreeSitterLanguageTypeScript, true, false},
		{"tsx generic functions", "export function helper<T>(n: T){return n;} export function caller<T>(n: T){return <span>{helper<T>(n)}</span>;}", uci.TreeSitterLanguageTSX, true, false},
		{"javascript constructor", "export function helper(){}; export function caller(){ return new helper(); }", uci.TreeSitterLanguageJavaScript, true, false},
		{"typescript constructor", "export function helper(){}; export function caller(){ return new helper(); }", uci.TreeSitterLanguageTypeScript, true, false},
		{"tsx constructor", "export function helper(){}; export function caller(){ return new helper(); }", uci.TreeSitterLanguageTSX, true, false},
		{"typescript assertion write with trivia", "export function helper(){}; export function caller(n: any){ (/* trivia */ helper /* trivia */ as any) = n; return helper(); }", uci.TreeSitterLanguageTypeScript, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.source)
			parsed, err := parser.Parse(context.Background(), uci.TreeSitterParseRequest{Language: test.language, ProfileKey: "call-trivia-assertion/v1", Source: body})
			require.NoError(t, err)
			require.Equal(t, uci.IndexCoverageComplete, parsed.Coverage, "%+v", parsed.Diagnostics)
			profile, err := uci.TreeSitterIndexAdmissionArtifactProfile(test.language, uci.TreeSitterBundleDigest())
			require.NoError(t, err)
			artifact, err := uci.NewIndexAdmissionArtifactFromTreeSitter("11111111-1111-4111-8111-111111111111", profile, body, parsed)
			require.NoError(t, err)
			var callFound, writeFound bool
			for _, reference := range artifact.References {
				if reference.Kind == "call" && strings.HasPrefix(reference.SiteKey, "call:helper@") {
					callFound = true
					if test.resolved {
						require.Equal(t, "helper", reference.RawTarget, "direct callee must have parser-backed span")
						require.Equal(t, "helper", string(body[reference.Span.ByteStart:reference.Span.ByteEnd]), "graph evidence must identify only the callee, including constructors")
					}
				}
				if reference.Kind == "binding_write" && reference.RawTarget == "helper" {
					writeFound = true
				}
			}
			require.True(t, callFound)
			require.Equal(t, test.written, writeFound)
			id := artifact.ArtifactID
			files := []uciPreparedAdmissionFile{{path: "calls.ts", membership: uci.IndexAdmissionMembership{PathKey: "calls.ts", DisplayPath: "calls.ts", Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &id}, artifact: &artifact}}
			unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			require.Len(t, files[0].edges, 1)
			edge := files[0].edges[0]
			if test.resolved {
				require.Zero(t, unresolved)
				require.Equal(t, uci.IndexResolutionState("resolved"), edge.ResolutionState)
				require.Equal(t, "function:helper", *edge.Target.SymbolKey)
				require.Equal(t, "helper", string(body[edge.Evidence.Span.ByteStart:edge.Evidence.Span.ByteEnd]), "resolved graph evidence must not include the constructor expression")
				require.Equal(t, "tree-sitter-direct-local-call/v1", edge.Evidence.RuleKey)
			} else {
				require.Equal(t, uint64(1), unresolved)
				require.Equal(t, uci.IndexResolutionState("unresolved"), edge.ResolutionState)
				require.Nil(t, edge.Target)
				require.Equal(t, "tree-sitter-rebound-local-call/v1", edge.Evidence.RuleKey)
			}
			frames, _, err := uciPreparedPackFrames("44444444-4444-4444-8444-444444444444", files)
			require.NoError(t, err)
			require.NoError(t, uci.ValidateIndexAdmissionFrames(frames))
			require.Len(t, frames, 1)
			part, err := frames[0].PublicationPart()
			require.NoError(t, err)
			require.Len(t, part.EdgeReplacements, 1)
			require.Len(t, part.EdgeReplacements[0].Edges, 1)
			require.Equal(t, edge.ResolutionState, part.EdgeReplacements[0].Edges[0].ResolutionState)
			published := part.EdgeReplacements[0].Edges[0]
			require.Equal(t, edge.Evidence.RuleKey, published.Evidence.RuleKey)
			if test.resolved {
				require.NotNil(t, published.Target)
				require.Equal(t, "function:helper", *published.Target.SymbolKey)
			} else {
				require.Nil(t, published.Target)
			}
			if test.resolved {
				files[0].edges = nil
				files[0].artifact.Status = uci.IndexAdmissionArtifactPartial
				_, err = uciPreparedAddResolvedTreeSitterEdges(files)
				require.NoError(t, err)
				require.Empty(t, files[0].edges, "incomplete parser facts cannot resolve a same-file call")
			}
		})
	}
}

func TestUCIPreparedIndexBuiltParserDoesNotResolveDirectEvalCalls(t *testing.T) {
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
		unsafe       bool
		direct       bool
	}{
		{"commented javascript eval", "export function helper(n) { return n; }\nexport function caller(n) { eval /* comment */ (\"helper = n\"); return helper(n); }", uci.TreeSitterLanguageJavaScript, true, true},
		{"spaced typescript eval", "export function helper(n: number) { return n; }\nexport function caller(n: any) { eval  (\"helper = n\"); return helper(n); }", uci.TreeSitterLanguageTypeScript, true, true},
		{"commented typescript eval", "export function helper(n: number) { return n; }\nexport function caller(n: any) { eval /* comment */ (\"helper = n\"); return helper(n); }", uci.TreeSitterLanguageTypeScript, true, true},
		{"string is not eval", "export function helper(n) { return n; }\nexport function caller(n) { const note = 'eval('; return helper(n); }", uci.TreeSitterLanguageJavaScript, false, false},
		{"parenthesized javascript eval", "export function helper(){}; export function caller(s){ (eval)(s); return helper(); }", uci.TreeSitterLanguageJavaScript, true, true},
		{"nested parenthesized typescript eval", "export function helper(){}; export function caller(s: string){ ((/* trivia */ eval /* trivia */))(s); return helper(); }", uci.TreeSitterLanguageTypeScript, true, true},
		{"parenthesized tsx eval", "export function helper(){}; export function caller(s: string){ (eval)(s); return <span>{helper()}</span>; }", uci.TreeSitterLanguageTSX, true, true},
		{"optional eval is indirect", "export function helper(){}; export function caller(s){ eval?.(s); return helper(); }", uci.TreeSitterLanguageJavaScript, false, false},
		{"sequence eval is indirect", "export function helper(){}; export function caller(s){ (0, eval)(s); return helper(); }", uci.TreeSitterLanguageJavaScript, false, false},
		{"eval constructor is not direct eval", "export function helper(){}; export function caller(s){ new eval(s); return helper(); }", uci.TreeSitterLanguageJavaScript, false, false},
		{"known function eval shadow", "export function helper(){}; export function caller(s){ function eval(){}; eval(s); return helper(); }", uci.TreeSitterLanguageJavaScript, false, true},
		{"known arrow eval shadow", "export function helper(){}; export function caller(s){ const eval = () => {}; (eval)(s); return helper(); }", uci.TreeSitterLanguageJavaScript, false, true},
		{"parameter eval may be intrinsic", "export function helper(){}; export function caller(eval, s){ eval(s); return helper(); }", uci.TreeSitterLanguageJavaScript, true, true},
		{"intrinsic eval alias", "export function helper(){}; export function caller(s){ const eval = globalThis.eval; (eval)(s); return helper(); }", uci.TreeSitterLanguageJavaScript, true, true},
		{"function eval shadow rebound to intrinsic", "export function helper(){}; export function caller(s){ let eval = () => {}; eval = globalThis.eval; eval(s); return helper(); }", uci.TreeSitterLanguageJavaScript, true, true},
		{"destructuring does not establish nonintrinsic eval", "export function helper(){}; export function caller(s){ const {eval} = function(){}; eval(s); return helper(); }", uci.TreeSitterLanguageJavaScript, true, true},
		{"asserted typescript eval", "export function helper(){}; export function caller(s: string){ (eval as any)(s); return helper(); }", uci.TreeSitterLanguageTypeScript, true, true},
		{"angle asserted typescript eval", "export function helper(){}; export function caller(s: string){ (<any>eval)(s); return helper(); }", uci.TreeSitterLanguageTypeScript, true, true},
		{"satisfies typescript eval", "export function helper(){}; export function caller(s: string){ (eval satisfies any)(s); return helper(); }", uci.TreeSitterLanguageTypeScript, true, true},
		{"non-null typescript eval", "export function helper(){}; export function caller(s: string){ eval!(s); return helper(); }", uci.TreeSitterLanguageTypeScript, true, true},
		{"nested erased typescript eval", "export function helper(){}; export function caller(s: string){ ((/* trivia */ eval as any)!) (s); return helper(); }", uci.TreeSitterLanguageTypeScript, true, true},
		{"asserted tsx eval", "export function helper(){}; export function caller(s: string){ (eval as any)(s); return <span>{helper()}</span>; }", uci.TreeSitterLanguageTSX, true, true},
		{"satisfies tsx eval", "export function helper(){}; export function caller(s: string){ (eval satisfies any)(s); return <span>{helper()}</span>; }", uci.TreeSitterLanguageTSX, true, true},
		{"non-null tsx eval", "export function helper(){}; export function caller(s: string){ eval!(s); return <span>{helper()}</span>; }", uci.TreeSitterLanguageTSX, true, true},
		{"erased optional eval is indirect", "export function helper(){}; export function caller(s: string){ (eval as any)?.(s); return helper(); }", uci.TreeSitterLanguageTypeScript, false, false},
		{"erased sequence eval is indirect", "export function helper(){}; export function caller(s: string){ ((0, eval) as any)(s); return helper(); }", uci.TreeSitterLanguageTypeScript, false, false},
		{"erased member eval is indirect", "export function helper(){}; export function caller(s: string){ (globalThis.eval as any)(s); return helper(); }", uci.TreeSitterLanguageTypeScript, false, false},
		{"erased constructor is not direct eval", "export function helper(){}; export function caller(s: string){ new (eval as any)(s); return helper(); }", uci.TreeSitterLanguageTypeScript, false, false},
		{"erased eval can mutate sibling callee", "export function helper(){}; export function caller(){ return helper(); } function dynamic(s: string){ (eval as any)(s); }", uci.TreeSitterLanguageTypeScript, true, true},
		{"erased eval shadow cannot mutate module callee", "export function helper(){}; export function caller(){ return helper(); } function dynamic(s: string){ let helper; (eval as any)(s); }", uci.TreeSitterLanguageTypeScript, false, true},
		{"known function erased eval shadow", "export function helper(){}; export function caller(s: string){ function eval(){}; (eval as any)(s); return helper(); }", uci.TreeSitterLanguageTypeScript, false, true},
		{"erased eval shadow rebound to intrinsic", "export function helper(){}; export function caller(s: string){ let eval = () => {}; eval = globalThis.eval; eval!(s); return helper(); }", uci.TreeSitterLanguageTypeScript, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.source)
			parsed, err := parser.Parse(context.Background(), uci.TreeSitterParseRequest{Language: test.language, ProfileKey: "direct-eval-integration/v1", Source: body})
			require.NoError(t, err)
			require.Equal(t, uci.IndexCoverageComplete, parsed.Coverage, "%+v", parsed.Diagnostics)
			profile, err := uci.TreeSitterIndexAdmissionArtifactProfile(test.language, uci.TreeSitterBundleDigest())
			require.NoError(t, err)
			artifact, err := uci.NewIndexAdmissionArtifactFromTreeSitter("11111111-1111-4111-8111-111111111111", profile, body, parsed)
			require.NoError(t, err)
			id := artifact.ArtifactID
			files := []uciPreparedAdmissionFile{{path: "calls.js", membership: uci.IndexAdmissionMembership{PathKey: "calls.js", DisplayPath: "calls.js", Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &id}, artifact: &artifact}}
			unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			var parsedEval bool
			for _, fact := range artifact.TreeSitterLexicalFacts {
				if strings.HasPrefix(fact.SiteKey, "lexical_eval@") {
					parsedEval = true
				}
			}
			var resolvedHelperCalls int
			for _, edge := range files[0].edges {
				if edge.Relation == uci.IndexRelation("calls") && edge.ResolutionState == uci.IndexResolutionState("resolved") && edge.Target != nil && edge.Target.SymbolKey != nil && *edge.Target.SymbolKey == "function:helper" {
					resolvedHelperCalls++
				}
			}
			frames, _, err := uciPreparedPackFrames("44444444-4444-4444-8444-444444444444", files)
			require.NoError(t, err)
			require.NoError(t, uci.ValidateIndexAdmissionFrames(frames))
			for _, frame := range frames {
				part, err := frame.PublicationPart()
				require.NoError(t, err)
				for _, replacement := range part.EdgeReplacements {
					for _, edge := range replacement.Edges {
						if edge.Relation == uci.IndexRelation("calls") && edge.ResolutionState == uci.IndexResolutionState("resolved") && edge.Target != nil && edge.Target.SymbolKey != nil && *edge.Target.SymbolKey == "function:helper" {
							require.False(t, test.unsafe, "publication must not revive an eval-affected resolved edge")
							require.Equal(t, "function:caller", *edge.SourceSymbolKey)
						}
					}
				}
			}
			if test.unsafe {
				require.NotZero(t, unresolved)
				require.Zero(t, resolvedHelperCalls, "potential intrinsic eval must not produce a resolved edge to helper")
			} else {
				require.Equal(t, 1, resolvedHelperCalls)
			}
			require.Equal(t, test.direct, parsedEval, "direct eval classification must follow the actual callee AST")
		})
	}
}

func TestUCIPreparedIndexBuiltParserIgnoresDeclarationTextInTrivia(t *testing.T) {
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
		ambiguous    bool
		duplicate    bool
		language     uci.TreeSitterLanguage
	}{
		{"comment", "export function helper(n) { return n; }\n// function helper(\nexport function caller(n) { return helper(n); }", false, false, uci.TreeSitterLanguageJavaScript},
		{"string", "export function helper(n) { return n; }\nconst note = 'function helper(';\nexport function caller(n) { return helper(n); }", false, false, uci.TreeSitterLanguageJavaScript},
		{"comment before shadowing parameter", "export function helper(n) { return n; }\nexport function caller /* (x) */(helper) { return helper(1); }", true, false, uci.TreeSitterLanguageJavaScript},
		{"generic parameter shadows helper", "export function helper(n: number) { return n; }\nexport function caller<T extends { method(x: number): void }>(helper: T) { return helper(1); }", true, false, uci.TreeSitterLanguageTypeScript},
		{"multiple bindings", "export function helper(n) { return n; }\nconst helper = 1;\nexport function caller(n) { return helper(n); }", true, false, uci.TreeSitterLanguageJavaScript},
		{"duplicate declaration", "export function helper(n) { return n; }\nexport function helper(n) { return n + 1; }\nexport function caller(n) { return helper(n); }", true, true, uci.TreeSitterLanguageJavaScript},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.source)
			parsed, err := parser.Parse(context.Background(), uci.TreeSitterParseRequest{Language: test.language, ProfileKey: "local-call-declaration-trivia/v1", Source: body})
			require.NoError(t, err)
			if test.duplicate {
				require.Equal(t, uci.IndexCoveragePartial, parsed.Coverage)
				require.NotEmpty(t, parsed.Diagnostics)
			} else {
				require.Equal(t, uci.IndexCoverageComplete, parsed.Coverage, "%+v", parsed.Diagnostics)
			}
			profile, err := uci.TreeSitterIndexAdmissionArtifactProfile(test.language, uci.TreeSitterBundleDigest())
			require.NoError(t, err)
			artifact, err := uci.NewIndexAdmissionArtifactFromTreeSitter("11111111-1111-4111-8111-111111111111", profile, body, parsed)
			require.NoError(t, err)
			var declarations, calls int
			for _, definition := range artifact.Definitions {
				if definition.Kind == "function" && definition.LocalSymbolKey == "function:helper" {
					declarations++
				}
			}
			for _, reference := range artifact.References {
				if reference.Kind == "call" && strings.HasPrefix(reference.SiteKey, "call:helper@") {
					calls++
					require.Equal(t, "function:caller", *reference.OwnerSymbolKey)
				}
			}
			require.Equal(t, 1, declarations)
			require.Equal(t, 1, calls)
			id := artifact.ArtifactID
			files := []uciPreparedAdmissionFile{{path: "calls.js", membership: uci.IndexAdmissionMembership{PathKey: "calls.js", DisplayPath: "calls.js", Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &id}, artifact: &artifact}}
			unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			if test.ambiguous {
				require.Equal(t, uint64(1), unresolved)
				require.Empty(t, files[0].edges)
				return
			}
			require.Zero(t, unresolved)
			require.Len(t, files[0].edges, 1)
			frames, _, err := uciPreparedPackFrames("44444444-4444-4444-8444-444444444444", files)
			require.NoError(t, err)
			require.NoError(t, uci.ValidateIndexAdmissionFrames(frames))
			require.Len(t, frames, 1)
			part, err := frames[0].PublicationPart()
			require.NoError(t, err)
			require.Len(t, part.EdgeReplacements, 1)
			require.Len(t, part.EdgeReplacements[0].Edges, 1)
			edge := part.EdgeReplacements[0].Edges[0]
			require.Equal(t, uci.IndexResolutionState("resolved"), edge.ResolutionState)
			require.Equal(t, "function:caller", *edge.SourceSymbolKey)
			require.NotNil(t, edge.Target)
			require.Equal(t, "function:helper", *edge.Target.SymbolKey)
		})
	}
}

func TestUCIPreparedIndexBuiltParserRejectsNestedSameKeyCalls(t *testing.T) {
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
	body := []byte("export function helper(){helper();function helper(){}}")
	parsed, err := parser.Parse(context.Background(), uci.TreeSitterParseRequest{Language: uci.TreeSitterLanguageJavaScript, ProfileKey: "nested-same-key/v1", Source: body})
	require.NoError(t, err)
	require.Equal(t, uci.IndexCoveragePartial, parsed.Coverage, "%+v", parsed.Diagnostics)
	require.Contains(t, parsed.Diagnostics, uci.TreeSitterDiagnostic{Code: "DUPLICATE_DEFINITION", Message: "multiple declarations share a parser symbol key"})
	require.Len(t, parsed.Definitions, 1)
	require.Equal(t, "function:helper", parsed.Definitions[0].LocalKey)
	var callCount int
	for _, reference := range parsed.References {
		if reference.Kind == "call" && reference.OwnerLocalKey == "function:helper" {
			callCount++
		}
	}
	require.Equal(t, 1, callCount)
	profile, err := uci.TreeSitterIndexAdmissionArtifactProfile(uci.TreeSitterLanguageJavaScript, uci.TreeSitterBundleDigest())
	require.NoError(t, err)
	artifact, err := uci.NewIndexAdmissionArtifactFromTreeSitter("11111111-1111-4111-8111-111111111111", profile, body, parsed)
	require.NoError(t, err)
	require.Equal(t, uci.IndexAdmissionArtifactPartial, artifact.Status)
	id := artifact.ArtifactID
	files := []uciPreparedAdmissionFile{{path: "calls.js", membership: uci.IndexAdmissionMembership{PathKey: "calls.js", DisplayPath: "calls.js", Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &id}, artifact: &artifact}}
	unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
	require.NoError(t, err)
	require.Equal(t, uint64(1), unresolved)
	require.Empty(t, files[0].edges, "ambiguous nested declaration cannot publish a resolved self-edge")
	frames, _, err := uciPreparedPackFrames("44444444-4444-4444-8444-444444444444", files)
	require.NoError(t, err)
	require.NoError(t, uci.ValidateIndexAdmissionFrames(frames))
	require.Len(t, frames, 1)
	part, err := frames[0].PublicationPart()
	require.NoError(t, err)
	require.Len(t, part.EdgeReplacements, 1)
	require.Empty(t, part.EdgeReplacements[0].Edges)
}

func TestUCIPreparedIndexBuiltParserRetainsLegalRepeatedDeclarations(t *testing.T) {
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
		name, source, kind, binding string
		language                    uci.TreeSitterLanguage
		occurrences                 int
	}{
		{"javascript accessors", "class A { get x(){return 1;} set x(v){} }", "method", "A.x", uci.TreeSitterLanguageJavaScript, 2},
		{"typescript overloads", "class A { x(v: string): string; x(v: number): number; x(v: any){return v;} }", "method", "A.x", uci.TreeSitterLanguageTypeScript, 3},
		{"merged interfaces", "interface Merged { a: string; } interface Merged { b: number; }", "interface", "Merged", uci.TreeSitterLanguageTypeScript, 2},
		{"merged namespaces", "namespace Merged { export const a = 1; } namespace Merged { export const b = 2; }", "namespace", "Merged", uci.TreeSitterLanguageTypeScript, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(test.source + "\nexport function helper(){return 1;} export function caller(){return helper();}")
			parsed, err := parser.Parse(context.Background(), uci.TreeSitterParseRequest{Language: test.language, ProfileKey: "legal-repeated-declarations/v6", Source: body})
			require.NoError(t, err)
			require.Equal(t, uci.IndexCoverageComplete, parsed.Coverage, "%+v", parsed.Diagnostics)
			var occurrences int
			keys := make(map[string]struct{})
			for _, definition := range parsed.Definitions {
				if definition.Kind != test.kind {
					continue
				}
				occurrences++
				require.NotContains(t, keys, definition.LocalKey)
				keys[definition.LocalKey] = struct{}{}
				require.Contains(t, string(body[definition.Span.ByteStart:definition.Span.ByteEnd]), definition.Name)
			}
			require.Equal(t, test.occurrences, occurrences, "all legal declaration occurrences must remain navigable")
			profile, err := uci.TreeSitterIndexAdmissionArtifactProfile(test.language, uci.TreeSitterBundleDigest())
			require.NoError(t, err)
			artifact, err := uci.NewIndexAdmissionArtifactFromTreeSitter("11111111-1111-4111-8111-111111111111", profile, body, parsed)
			require.NoError(t, err)
			id := artifact.ArtifactID
			files := []uciPreparedAdmissionFile{{path: "decl.ts", membership: uci.IndexAdmissionMembership{PathKey: "decl.ts", DisplayPath: "decl.ts", Mode: "100644", State: uci.IndexAdmissionMembershipPresent, ArtifactID: &id}, artifact: &artifact}}
			byPath, definitions := uciPreparedTreeSitterSources(files)
			require.Len(t, definitions["decl.ts"][test.binding], test.occurrences)
			_, resolved := uciPreparedTreeSitterModuleTarget(byPath, definitions, "consumer.ts", "./decl.ts", test.binding)
			require.False(t, resolved, "occurrence identity must not invent a unique graph binding")
			unresolved, err := uciPreparedAddResolvedTreeSitterEdges(files)
			require.NoError(t, err)
			require.Zero(t, unresolved)
			require.Len(t, files[0].edges, 1)
			require.Equal(t, "function:helper", *files[0].edges[0].Target.SymbolKey)
			frames, _, err := uciPreparedPackFrames("44444444-4444-4444-8444-444444444444", files)
			require.NoError(t, err)
			require.NoError(t, uci.ValidateIndexAdmissionFrames(frames))
		})
	}
}
