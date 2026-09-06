package uci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	uciTreeSitterWireVersion = TreeSitterWorkerProtocolVersion
	uciTreeSitterHelperTest  = "^TestUCITreeSitterWorkerProcessHelper$"
)

var (
	uciTreeSitterHelperMode               = flag.String("uci-tree-sitter-helper-mode", "", "Tree-sitter worker fixture-child mode")
	uciTreeSitterHelperBundleDigest       = flag.String("uci-tree-sitter-helper-bundle-digest", "", "Tree-sitter worker fixture-child bundle digest")
	uciTreeSitterHelperAuditFile          = flag.String("uci-tree-sitter-helper-audit-file", "", "Tree-sitter worker fixture-child audit file")
	uciTreeSitterHelperAllowedEnvironment = flag.String("uci-tree-sitter-helper-allowed-environment", "", "comma-separated allowed child environment names")
	uciTreeSitterHelperProjectRoot        = flag.String("uci-tree-sitter-helper-project-root", "", "untrusted project root that must not become the child working directory")
)

type uciTreeSitterTestField struct {
	name string
	typ  reflect.Type
}

type uciTreeSitterFixture struct {
	language    TreeSitterLanguage
	profileKey  string
	source      string
	definitions []uciTreeSitterExpectedDefinition
	references  []uciTreeSitterExpectedReference
}

type uciTreeSitterExpectedDefinition struct {
	kind       string
	name       string
	symbolKey  string
	localKey   string
	fragment   string
	occurrence int
}

type uciTreeSitterExpectedReference struct {
	kind          string
	symbolKey     string
	localKey      string
	ownerLocalKey string
	rawTarget     string
	resolution    IndexResolutionState
	fragment      string
	occurrence    int
}

type uciTreeSitterWorkerOptions struct {
	mode                 string
	expectedBundleDigest IndexDigest
	reportedBundleDigest IndexDigest
	maxInputBytes        int
	maxOutputBytes       int
	timeout              time.Duration
	environment          []string
	auditFile            string
	projectRoot          string
}

type uciTreeSitterWireRequest struct {
	Version    string `json:"version"`
	Language   string `json:"language"`
	ProfileKey string `json:"profile_key"`
	Source     []byte `json:"source"`
}

type uciTreeSitterWireResponse struct {
	Version      string                        `json:"version"`
	BundleDigest IndexDigest                   `json:"bundle_digest"`
	Coverage     IndexCoverageState            `json:"coverage"`
	Text         string                        `json:"text"`
	Definitions  []uciTreeSitterWireDefinition `json:"definitions"`
	References   []uciTreeSitterWireReference  `json:"references"`
	Chunks       []uciTreeSitterWireChunk      `json:"chunks"`
	Diagnostics  []uciTreeSitterWireDiagnostic `json:"diagnostics"`
}

type uciTreeSitterWireDefinition struct {
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	SymbolKey string    `json:"symbol_key"`
	LocalKey  string    `json:"local_key"`
	Span      IndexSpan `json:"span"`
}

type uciTreeSitterWireReference struct {
	Kind          string               `json:"kind"`
	SymbolKey     string               `json:"symbol_key"`
	LocalKey      string               `json:"local_key"`
	OwnerLocalKey string               `json:"owner_local_key"`
	RawTarget     string               `json:"raw_target"`
	TargetKey     string               `json:"target_key"`
	Resolution    IndexResolutionState `json:"resolution"`
	Span          IndexSpan            `json:"span"`
}

type uciTreeSitterWireChunk struct {
	Span          IndexSpan   `json:"span"`
	Text          string      `json:"text"`
	ContentDigest IndexDigest `json:"content_digest"`
}

type uciTreeSitterWireDiagnostic struct {
	Code    string    `json:"code"`
	Span    IndexSpan `json:"span"`
	Message string    `json:"message"`
}

type uciTreeSitterWorkerAudit struct {
	PID int    `json:"pid"`
	CWD string `json:"cwd"`
}

var uciTreeSitterFixtures = []uciTreeSitterFixture{
	{
		language:   TreeSitterLanguageJavaScript,
		profileKey: "javascript-tree-sitter-v1",
		source: "// Café\r\n" +
			"import { shared as localShared } from \"./shared.js\";\r\n" +
			"export { shared as publicShared } from \"./shared.js\";\r\n" +
			"export function greet(name) {\r\n" +
			"  return localShared(name);\r\n" +
			"}\r\n",
		definitions: []uciTreeSitterExpectedDefinition{
			{
				kind:       "function",
				name:       "greet",
				symbolKey:  "javascript:function:greet",
				localKey:   "function:greet",
				fragment:   "export function greet(name) {\r\n  return localShared(name);\r\n}",
				occurrence: 0,
			},
		},
		references: []uciTreeSitterExpectedReference{
			{
				kind:       "import_alias",
				symbolKey:  "javascript:import:./shared.js#shared:localShared",
				localKey:   "import:./shared.js#shared:localShared",
				rawTarget:  "./shared.js#shared",
				resolution: TreeSitterResolutionSyntaxOnly,
				fragment:   "shared as localShared",
				occurrence: 0,
			},
			{
				kind:       "reexport_alias",
				symbolKey:  "javascript:reexport:./shared.js#shared:publicShared",
				localKey:   "reexport:./shared.js#shared:publicShared",
				rawTarget:  "./shared.js#shared",
				resolution: TreeSitterResolutionPartial,
				fragment:   "shared as publicShared",
				occurrence: 0,
			},
			{
				kind:          "call",
				symbolKey:     "javascript:call:localShared",
				localKey:      "call:localShared",
				ownerLocalKey: "function:greet",
				rawTarget:     "localShared",
				resolution:    TreeSitterResolutionSyntaxOnly,
				fragment:      "localShared(name)",
				occurrence:    0,
			},
		},
	},
	{
		language:   TreeSitterLanguageTypeScript,
		profileKey: "typescript-tree-sitter-v1",
		source: "import { build as localBuild } from \"./remote\";\n" +
			"export { build as publicBuild } from \"./remote\";\n" +
			"export interface Worker { run(input: string): string; }\n" +
			"export function start(input: string): string {\n" +
			"  return localBuild(input);\n" +
			"}\n",
		definitions: []uciTreeSitterExpectedDefinition{
			{
				kind:       "interface",
				name:       "Worker",
				symbolKey:  "typescript:interface:Worker",
				localKey:   "interface:Worker",
				fragment:   "export interface Worker { run(input: string): string; }",
				occurrence: 0,
			},
			{
				kind:       "function",
				name:       "start",
				symbolKey:  "typescript:function:start",
				localKey:   "function:start",
				fragment:   "export function start(input: string): string {\n  return localBuild(input);\n}",
				occurrence: 0,
			},
		},
		references: []uciTreeSitterExpectedReference{
			{
				kind:       "import_alias",
				symbolKey:  "typescript:import:./remote#build:localBuild",
				localKey:   "import:./remote#build:localBuild",
				rawTarget:  "./remote#build",
				resolution: TreeSitterResolutionSyntaxOnly,
				fragment:   "build as localBuild",
				occurrence: 0,
			},
			{
				kind:       "reexport_alias",
				symbolKey:  "typescript:reexport:./remote#build:publicBuild",
				localKey:   "reexport:./remote#build:publicBuild",
				rawTarget:  "./remote#build",
				resolution: TreeSitterResolutionPartial,
				fragment:   "build as publicBuild",
				occurrence: 0,
			},
			{
				kind:          "call",
				symbolKey:     "typescript:call:localBuild",
				localKey:      "call:localBuild",
				ownerLocalKey: "function:start",
				rawTarget:     "localBuild",
				resolution:    TreeSitterResolutionSyntaxOnly,
				fragment:      "localBuild(input)",
				occurrence:    0,
			},
		},
	},
	{
		language:   TreeSitterLanguageTSX,
		profileKey: "tsx-tree-sitter-v1",
		source: "import { Widget as RemoteWidget } from \"./widget\";\n" +
			"export { Widget as PublicWidget } from \"./widget\";\n" +
			"export function Screen({ title }: { title: string }) {\n" +
			"  return <RemoteWidget title={title} />;\n" +
			"}\n",
		definitions: []uciTreeSitterExpectedDefinition{
			{
				kind:       "function",
				name:       "Screen",
				symbolKey:  "tsx:function:Screen",
				localKey:   "function:Screen",
				fragment:   "export function Screen({ title }: { title: string }) {\n  return <RemoteWidget title={title} />;\n}",
				occurrence: 0,
			},
		},
		references: []uciTreeSitterExpectedReference{
			{
				kind:       "import_alias",
				symbolKey:  "tsx:import:./widget#Widget:RemoteWidget",
				localKey:   "import:./widget#Widget:RemoteWidget",
				rawTarget:  "./widget#Widget",
				resolution: TreeSitterResolutionSyntaxOnly,
				fragment:   "Widget as RemoteWidget",
				occurrence: 0,
			},
			{
				kind:       "reexport_alias",
				symbolKey:  "tsx:reexport:./widget#Widget:PublicWidget",
				localKey:   "reexport:./widget#Widget:PublicWidget",
				rawTarget:  "./widget#Widget",
				resolution: TreeSitterResolutionPartial,
				fragment:   "Widget as PublicWidget",
				occurrence: 0,
			},
			{
				kind:          "jsx_reference",
				symbolKey:     "tsx:jsx_reference:RemoteWidget",
				localKey:      "jsx_reference:RemoteWidget",
				ownerLocalKey: "function:Screen",
				rawTarget:     "RemoteWidget",
				resolution:    TreeSitterResolutionSyntaxOnly,
				fragment:      "RemoteWidget",
				occurrence:    1,
			},
		},
	},
}

func TestUCITreeSitterWorkerHasBoundedProcessAPI(t *testing.T) {
	var _ func(TreeSitterWorkerConfig) (*TreeSitterWorker, error) = NewTreeSitterWorker
	var _ func(*TreeSitterWorker, context.Context, TreeSitterParseRequest) (TreeSitterArtifact, error) = (*TreeSitterWorker).Parse
	var _ func(TreeSitterParseRequest) (IndexDigest, error) = TreeSitterWireRequestDigest

	uciRequireTreeSitterStructFields(t, TreeSitterWorkerConfig{}, []uciTreeSitterTestField{
		{name: "ExecutablePath", typ: reflect.TypeOf("")},
		{name: "Arguments", typ: reflect.TypeOf([]string(nil))},
		{name: "ExpectedBundleDigest", typ: reflect.TypeOf(IndexDigest(""))},
		{name: "MaxInputBytes", typ: reflect.TypeOf(0)},
		{name: "MaxOutputBytes", typ: reflect.TypeOf(0)},
		{name: "Timeout", typ: reflect.TypeOf(time.Duration(0))},
		{name: "Environment", typ: reflect.TypeOf([]string(nil))},
	})
	uciRequireTreeSitterStructFields(t, TreeSitterParseRequest{}, []uciTreeSitterTestField{
		{name: "Language", typ: reflect.TypeOf(TreeSitterLanguage(""))},
		{name: "ProfileKey", typ: reflect.TypeOf("")},
		{name: "Source", typ: reflect.TypeOf([]byte(nil))},
	})
	uciRequireTreeSitterStructFields(t, TreeSitterArtifact{}, []uciTreeSitterTestField{
		{name: "Proof", typ: reflect.TypeOf(IndexArtifactProof{})},
		{name: "Coverage", typ: reflect.TypeOf(IndexCoverageComplete)},
		{name: "Language", typ: reflect.TypeOf(TreeSitterLanguage(""))},
		{name: "BundleDigest", typ: reflect.TypeOf(IndexDigest(""))},
		{name: "Text", typ: reflect.TypeOf("")},
		{name: "Definitions", typ: reflect.TypeOf([]TreeSitterDefinition(nil))},
		{name: "References", typ: reflect.TypeOf([]TreeSitterReferenceSite(nil))},
		{name: "Chunks", typ: reflect.TypeOf([]TreeSitterChunk(nil))},
		{name: "Diagnostics", typ: reflect.TypeOf([]TreeSitterDiagnostic(nil))},
	})
	uciRequireTreeSitterStructFields(t, TreeSitterDefinition{}, []uciTreeSitterTestField{
		{name: "Kind", typ: reflect.TypeOf("")},
		{name: "Name", typ: reflect.TypeOf("")},
		{name: "SymbolKey", typ: reflect.TypeOf("")},
		{name: "LocalKey", typ: reflect.TypeOf("")},
		{name: "Span", typ: reflect.TypeOf(IndexSpan{})},
	})
	uciRequireTreeSitterStructFields(t, TreeSitterReferenceSite{}, []uciTreeSitterTestField{
		{name: "Kind", typ: reflect.TypeOf("")},
		{name: "SymbolKey", typ: reflect.TypeOf("")},
		{name: "LocalKey", typ: reflect.TypeOf("")},
		{name: "OwnerLocalKey", typ: reflect.TypeOf("")},
		{name: "RawTarget", typ: reflect.TypeOf("")},
		{name: "TargetKey", typ: reflect.TypeOf("")},
		{name: "Resolution", typ: reflect.TypeOf(IndexResolutionState(""))},
		{name: "Span", typ: reflect.TypeOf(IndexSpan{})},
	})
	uciRequireTreeSitterStructFields(t, TreeSitterChunk{}, []uciTreeSitterTestField{
		{name: "Span", typ: reflect.TypeOf(IndexSpan{})},
		{name: "Text", typ: reflect.TypeOf("")},
		{name: "ContentDigest", typ: reflect.TypeOf(IndexDigest(""))},
	})
	uciRequireTreeSitterStructFields(t, TreeSitterDiagnostic{}, []uciTreeSitterTestField{
		{name: "Code", typ: reflect.TypeOf("")},
		{name: "Span", typ: reflect.TypeOf(IndexSpan{})},
		{name: "Message", typ: reflect.TypeOf("")},
	})

	for _, resolution := range []IndexResolutionState{
		TreeSitterResolutionSyntaxOnly,
		TreeSitterResolutionPartial,
		TreeSitterResolutionUnresolved,
	} {
		if resolution == "" {
			t.Fatal("Tree-sitter resolution states must be explicit nonempty values")
		}
	}
}

func TestTreeSitterWireRequestDigestMatchesPreparedJSONLine(t *testing.T) {
	request := TreeSitterParseRequest{
		Language:   TreeSitterLanguageJavaScript,
		ProfileKey: "javascript-tree-sitter-v1<&>\"",
		Source:     []byte("// Café <&>\r\nconst quote = \"✓\";\n"),
	}

	first, err := TreeSitterWireRequestDigest(request)
	if err != nil {
		t.Fatalf("TreeSitterWireRequestDigest() error = %v", err)
	}
	second, err := TreeSitterWireRequestDigest(request)
	if err != nil {
		t.Fatalf("second TreeSitterWireRequestDigest() error = %v", err)
	}
	if first != second {
		t.Fatalf("TreeSitterWireRequestDigest() is nondeterministic: first=%q second=%q", first, second)
	}

	requestLine, err := treeSitterPrepareWireRequest(request, treeSitterWorkerHardMaxInputBytes)
	if err != nil {
		t.Fatalf("treeSitterPrepareWireRequest() error = %v", err)
	}
	if !bytes.HasSuffix(requestLine, []byte{'\n'}) {
		t.Fatalf("prepared request is not a JSON line: %q", requestLine)
	}
	sum := sha256.Sum256(requestLine)
	want := IndexDigest("sha256:" + hex.EncodeToString(sum[:]))
	if first != want {
		t.Fatalf("TreeSitterWireRequestDigest() = %q, want SHA-256 of child stdin line %q", first, want)
	}

	for name, changed := range map[string]TreeSitterParseRequest{
		"language": {Language: TreeSitterLanguageTypeScript, ProfileKey: request.ProfileKey, Source: request.Source},
		"profile":  {Language: request.Language, ProfileKey: "javascript-tree-sitter-v2<&>\"", Source: request.Source},
		"source":   {Language: request.Language, ProfileKey: request.ProfileKey, Source: []byte("// Café <&>\r\nconst quote = \"different\";\n")},
	} {
		got, err := TreeSitterWireRequestDigest(changed)
		if err != nil {
			t.Fatalf("%s TreeSitterWireRequestDigest() error = %v", name, err)
		}
		if got == first {
			t.Fatalf("%s change did not alter digest %q", name, got)
		}
	}
}

func TestTreeSitterWireRequestDigestRejectsInvalidRequests(t *testing.T) {
	valid := TreeSitterParseRequest{
		Language:   TreeSitterLanguageJavaScript,
		ProfileKey: "javascript-tree-sitter-v1",
		Source:     []byte("const stable = true;\n"),
	}
	for name, request := range map[string]TreeSitterParseRequest{
		"empty language":    {Language: "", ProfileKey: valid.ProfileKey, Source: valid.Source},
		"invalid language":  {Language: TreeSitterLanguage("javascript\x00"), ProfileKey: valid.ProfileKey, Source: valid.Source},
		"invalid profile":   {Language: valid.Language, ProfileKey: " profile", Source: valid.Source},
		"malformed profile": {Language: valid.Language, ProfileKey: string([]byte{0xff}), Source: valid.Source},
		"oversize source":   {Language: valid.Language, ProfileKey: valid.ProfileKey, Source: make([]byte, treeSitterWorkerHardMaxInputBytes+1)},
	} {
		_, err := TreeSitterWireRequestDigest(request)
		if err == nil {
			t.Fatalf("%s TreeSitterWireRequestDigest() error = nil", name)
		}
		if name == "oversize source" {
			if !errors.Is(err, ErrTreeSitterInputLimit) {
				t.Fatalf("%s error = %v, want ErrTreeSitterInputLimit", name, err)
			}
		} else if !errors.Is(err, ErrTreeSitterProtocol) {
			t.Fatalf("%s error = %v, want ErrTreeSitterProtocol", name, err)
		}
	}
}

func TestUCITreeSitterWorkerFramesJavaScriptFacts(t *testing.T) {
	fixture := uciTreeSitterFixtureForLanguage(t, TreeSitterLanguageJavaScript)
	artifact := uciParseTreeSitterFixture(t, fixture)
	uciRequireTreeSitterFixtureArtifact(t, fixture, artifact)
}

func TestUCITypeScriptWorkerFramesTypeScriptFacts(t *testing.T) {
	fixture := uciTreeSitterFixtureForLanguage(t, TreeSitterLanguageTypeScript)
	artifact := uciParseTreeSitterFixture(t, fixture)
	uciRequireTreeSitterFixtureArtifact(t, fixture, artifact)
}

func TestUCITSXWorkerFramesTSXFacts(t *testing.T) {
	fixture := uciTreeSitterFixtureForLanguage(t, TreeSitterLanguageTSX)
	artifact := uciParseTreeSitterFixture(t, fixture)
	uciRequireTreeSitterFixtureArtifact(t, fixture, artifact)
}

func TestUCITreeSitterWorkerFramesBuiltParserFacts(t *testing.T) {
	executable := uciBuildTreeSitterParser(t)
	worker := uciNewBuiltTreeSitterWorker(t, executable, uciBuiltTreeSitterBundleDigest(t, executable))
	cases := []struct {
		name                 string
		request              TreeSitterParseRequest
		definitionNames      []string
		referenceKinds       []string
		defaultImportTarget  string
		commonJSImportTarget string
		selfClosingComponent string
	}{
		{
			name: "javascript",
			request: TreeSitterParseRequest{
				Language:   TreeSitterLanguageJavaScript,
				ProfileKey: "javascript-tree-sitter-v2",
				Source: []byte("import DefaultValue, { named as localNamed } from \"./dep.js\";\n" +
					"import * as Namespace from \"./namespace.js\";\n" +
					"export { named as Reexported } from \"./dep.js\";\n" +
					"export { localNamed as PublicNamed };\n" +
					"const { original: localValue, plain, ...restValues } = sourceValue;\n" +
					"export function run() {\n" +
					"  DefaultValue();\n" +
					"  DefaultValue();\n" +
					"  return Namespace.member;\n" +
					"}\n"),
			},
			definitionNames:     []string{"localValue", "plain", "restValues", "run"},
			referenceKinds:      []string{"import", "import_alias", "reexport", "reexport_alias", "export_alias", "call", "reference"},
			defaultImportTarget: "./dep.js#default",
		},
		{
			name: "typescript",
			request: TreeSitterParseRequest{
				Language:   TreeSitterLanguageTypeScript,
				ProfileKey: "typescript-tree-sitter-v2",
				Source: []byte("import DefaultValue, { named as localNamed } from \"./dep\";\n" +
					"import CommonValue = require(\"./common\");\n" +
					"export { named as Reexported } from \"./dep\";\n" +
					"export { localNamed as PublicNamed };\n" +
					"const { original: localValue, plain, ...restValues } = sourceValue;\n" +
					"export function run(): void {\n" +
					"  DefaultValue();\n" +
					"  DefaultValue();\n" +
					"  CommonValue.member;\n" +
					"}\n"),
			},
			definitionNames:      []string{"localValue", "plain", "restValues", "run"},
			referenceKinds:       []string{"import", "import_alias", "reexport", "reexport_alias", "export_alias", "call", "reference"},
			defaultImportTarget:  "./dep#default",
			commonJSImportTarget: "./common#commonjs",
		},
		{
			name: "tsx",
			request: TreeSitterParseRequest{
				Language:   TreeSitterLanguageTSX,
				ProfileKey: "tsx-tree-sitter-v2",
				Source: []byte("import DefaultValue, { Widget as LocalWidget } from \"./dep\";\n" +
					"export { Widget as PublicWidget } from \"./dep\";\n" +
					"export { LocalWidget as LocalAlias };\n" +
					"export function Screen() {\n" +
					"  DefaultValue();\n" +
					"  DefaultValue();\n" +
					"  LocalWidget.displayName;\n" +
					"  return <LocalWidget />;\n" +
					"}\n"),
			},
			definitionNames:      []string{"Screen"},
			referenceKinds:       []string{"import", "import_alias", "reexport", "reexport_alias", "export_alias", "call", "reference", "jsx_reference"},
			defaultImportTarget:  "./dep#default",
			selfClosingComponent: "LocalWidget",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			artifact, err := worker.Parse(context.Background(), testCase.request)
			if err != nil {
				t.Fatalf("built parser Parse(%q): %v", testCase.request.Language, err)
			}
			uciRequireBuiltTreeSitterFacts(t, artifact, testCase.definitionNames, testCase.referenceKinds, testCase.defaultImportTarget, testCase.commonJSImportTarget, testCase.selfClosingComponent)
		})
	}

	t.Run("dynamic import remains partial", func(t *testing.T) {
		artifact, err := worker.Parse(context.Background(), TreeSitterParseRequest{
			Language:   TreeSitterLanguageJavaScript,
			ProfileKey: "javascript-tree-sitter-v2",
			Source:     []byte("const target = chooseTarget();\nconst loaded = import(target);\n"),
		})
		if err != nil {
			t.Fatalf("built parser dynamic import: %v", err)
		}
		if artifact.Coverage != IndexCoveragePartial {
			t.Fatalf("dynamic import coverage = %q, want %q", artifact.Coverage, IndexCoveragePartial)
		}
		uciRequireTreeSitterDiagnostic(t, artifact.Diagnostics, "DYNAMIC_IMPORT")
		uciRequireTreeSitterUnresolvedReference(t, artifact.References, "call", "import", TreeSitterResolutionPartial)
	})

	t.Run("malformed source remains partial", func(t *testing.T) {
		artifact, err := worker.Parse(context.Background(), TreeSitterParseRequest{
			Language:   TreeSitterLanguageTypeScript,
			ProfileKey: "typescript-tree-sitter-v2",
			Source:     []byte("export const stable = 1;\nexport function broken(\n"),
		})
		if err != nil {
			t.Fatalf("built parser malformed source: %v", err)
		}
		if artifact.Coverage != IndexCoveragePartial {
			t.Fatalf("malformed source coverage = %q, want %q", artifact.Coverage, IndexCoveragePartial)
		}
		uciRequireTreeSitterDiagnostic(t, artifact.Diagnostics, "PARSE_ERROR")
		uciRequireBuiltDefinitionName(t, artifact.Definitions, "stable")
	})
}

func TestUCITreeSitterWorkerKeepsImmutableCheckoutIndependentIdentities(t *testing.T) {
	fixture := uciTreeSitterFixtureForLanguage(t, TreeSitterLanguageJavaScript)
	source := []byte(fixture.source)
	original := append([]byte(nil), source...)
	request := TreeSitterParseRequest{
		Language:   fixture.language,
		ProfileKey: fixture.profileKey,
		Source:     source,
	}

	first := uciParseTreeSitter(t, request, uciTreeSitterWorkerOptions{})
	source[0] = '!'
	second := uciParseTreeSitter(t, TreeSitterParseRequest{
		Language:   fixture.language,
		ProfileKey: fixture.profileKey,
		Source:     append([]byte(nil), original...),
	}, uciTreeSitterWorkerOptions{})

	if first.Text != fixture.source {
		t.Fatalf("Parse() retained mutable caller source: text = %q, want %q", first.Text, fixture.source)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("identical JS source/profile/bundle produced different immutable artifacts:\nfirst: %#v\nsecond: %#v", first, second)
	}
	uciRequireTreeSitterArtifactProof(t, first)

	changedProfile := uciParseTreeSitter(t, TreeSitterParseRequest{
		Language:   fixture.language,
		ProfileKey: "javascript-tree-sitter-v2",
		Source:     append([]byte(nil), original...),
	}, uciTreeSitterWorkerOptions{})
	if changedProfile.Proof.ArtifactID == first.Proof.ArtifactID {
		t.Fatalf("profile change reused artifact identity %q", first.Proof.ArtifactID)
	}

	changedBundleDigest := uciTreeSitterBundleDigest('b')
	changedBundle := uciParseTreeSitter(t, TreeSitterParseRequest{
		Language:   fixture.language,
		ProfileKey: fixture.profileKey,
		Source:     append([]byte(nil), original...),
	}, uciTreeSitterWorkerOptions{
		expectedBundleDigest: changedBundleDigest,
		reportedBundleDigest: changedBundleDigest,
	})
	if changedBundle.Proof.ArtifactID == first.Proof.ArtifactID {
		t.Fatalf("parser bundle change reused artifact identity %q", first.Proof.ArtifactID)
	}

	changedSource := uciParseTreeSitter(t, TreeSitterParseRequest{
		Language:   fixture.language,
		ProfileKey: fixture.profileKey,
		Source:     append(append([]byte(nil), original...), []byte("// changed bytes\r\n")...),
	}, uciTreeSitterWorkerOptions{})
	if changedSource.Proof.ArtifactID == first.Proof.ArtifactID {
		t.Fatalf("changed source bytes reused artifact identity %q", first.Proof.ArtifactID)
	}
}

func TestUCITreeSitterWorkerVerifiesPinnedBundleDigest(t *testing.T) {
	fixture := uciTreeSitterFixtureForLanguage(t, TreeSitterLanguageTypeScript)
	request := TreeSitterParseRequest{
		Language:   fixture.language,
		ProfileKey: fixture.profileKey,
		Source:     []byte(fixture.source),
	}

	expected := uciTreeSitterBundleDigest('a')
	artifact := uciParseTreeSitter(t, request, uciTreeSitterWorkerOptions{
		expectedBundleDigest: expected,
		reportedBundleDigest: expected,
	})
	if artifact.BundleDigest != expected {
		t.Fatalf("Parse() bundle digest = %q, want pinned %q", artifact.BundleDigest, expected)
	}

	worker := uciNewTreeSitterTestWorker(t, uciTreeSitterWorkerOptions{
		expectedBundleDigest: uciTreeSitterBundleDigest('b'),
		reportedBundleDigest: expected,
	})
	if _, err := worker.Parse(context.Background(), request); !errors.Is(err, ErrTreeSitterBundleMismatch) {
		t.Fatalf("Parse() bundle mismatch error = %v, want ErrTreeSitterBundleMismatch", err)
	}
}

func TestUCITreeSitterWorkerReportsPartialAndUnsupportedCoverage(t *testing.T) {
	malformedSource := []byte("export const stable = 1;\nexport function broken(\n")
	malformed := uciParseTreeSitter(t, TreeSitterParseRequest{
		Language:   TreeSitterLanguageTypeScript,
		ProfileKey: "typescript-tree-sitter-v1",
		Source:     malformedSource,
	}, uciTreeSitterWorkerOptions{})
	if malformed.Coverage != IndexCoveragePartial {
		t.Fatalf("malformed TypeScript coverage = %q, want %q", malformed.Coverage, IndexCoveragePartial)
	}
	if malformed.Text != string(malformedSource) {
		t.Fatalf("malformed TypeScript text = %q, want exact source", malformed.Text)
	}
	uciRequireTreeSitterArtifactProof(t, malformed)
	uciRequireTreeSitterDiagnostic(t, malformed.Diagnostics, "PARSE_ERROR")
	uciRequireTreeSitterDefinition(t, malformed.Definitions, uciTreeSitterExpectedDefinition{
		kind:       "const",
		name:       "stable",
		symbolKey:  "typescript:const:stable",
		localKey:   "const:stable",
		fragment:   "export const stable = 1;",
		occurrence: 0,
	}, malformedSource)

	unsupportedSource := []byte("unavailable language syntax\n")
	unsupported := uciParseTreeSitter(t, TreeSitterParseRequest{
		Language:   TreeSitterLanguage("unsupported"),
		ProfileKey: "unsupported-tree-sitter-v1",
		Source:     unsupportedSource,
	}, uciTreeSitterWorkerOptions{})
	if unsupported.Coverage != IndexCoverageUnavailable {
		t.Fatalf("unsupported language coverage = %q, want %q", unsupported.Coverage, IndexCoverageUnavailable)
	}
	if unsupported.Text != string(unsupportedSource) {
		t.Fatalf("unsupported language text = %q, want exact source", unsupported.Text)
	}
	uciRequireTreeSitterArtifactProof(t, unsupported)
	uciRequireTreeSitterDiagnostic(t, unsupported.Diagnostics, "UNSUPPORTED_LANGUAGE")
	if len(unsupported.Definitions) != 0 || len(unsupported.References) != 0 {
		t.Fatalf("unsupported language emitted syntax facts: definitions=%#v references=%#v", unsupported.Definitions, unsupported.References)
	}
}

func TestUCITreeSitterWorkerBoundsInputOutputDeadlineAndCancellation(t *testing.T) {
	fixture := uciTreeSitterFixtureForLanguage(t, TreeSitterLanguageJavaScript)
	request := TreeSitterParseRequest{
		Language:   fixture.language,
		ProfileKey: fixture.profileKey,
		Source:     []byte(fixture.source),
	}

	t.Run("input before child launch", func(t *testing.T) {
		auditFile := filepath.Join(t.TempDir(), "child.json")
		worker := uciNewTreeSitterTestWorker(t, uciTreeSitterWorkerOptions{
			mode:          "record",
			maxInputBytes: len(request.Source) - 1,
			auditFile:     auditFile,
		})
		if _, err := worker.Parse(context.Background(), request); !errors.Is(err, ErrTreeSitterInputLimit) {
			t.Fatalf("input-bound Parse() error = %v, want ErrTreeSitterInputLimit", err)
		}
		if _, err := os.Stat(auditFile); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("input-bound Parse() started an untrusted child: stat %q err = %v", auditFile, err)
		}
	})

	t.Run("output", func(t *testing.T) {
		worker := uciNewTreeSitterTestWorker(t, uciTreeSitterWorkerOptions{
			mode:           "oversized",
			maxOutputBytes: 64,
		})
		if _, err := worker.Parse(context.Background(), request); !errors.Is(err, ErrTreeSitterOutputLimit) {
			t.Fatalf("output-bound Parse() error = %v, want ErrTreeSitterOutputLimit", err)
		}
	})

	t.Run("deadline", func(t *testing.T) {
		worker := uciNewTreeSitterTestWorker(t, uciTreeSitterWorkerOptions{
			mode:    "stall",
			timeout: 100 * time.Millisecond,
		})
		if _, err := worker.Parse(context.Background(), request); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline-bound Parse() error = %v, want context.DeadlineExceeded", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		auditFile := filepath.Join(t.TempDir(), "child.json")
		worker := uciNewTreeSitterTestWorker(t, uciTreeSitterWorkerOptions{
			mode:      "stall",
			timeout:   3 * time.Second,
			auditFile: auditFile,
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := worker.Parse(ctx, request)
			done <- err
		}()

		uciRequireTreeSitterChildAudit(t, auditFile)
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled Parse() error = %v, want context.Canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancelled Parse() did not stop its child process")
		}
	})
}

func TestUCITreeSitterWorkerCleansEnvironmentAndAvoidsProjectExecution(t *testing.T) {
	projectRoot := t.TempDir()
	trapDirectory := filepath.Join(projectRoot, "project-bin")
	marker := filepath.Join(projectRoot, "project-command-executed")
	if err := os.MkdirAll(filepath.Join(projectRoot, "node_modules", "project-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(trapDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "package.json"), []byte(`{"scripts":{"preinstall":"npm run dangerous","prepare":"node project-plugin"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, ".uci-tree-sitter-project-plugin"), []byte("must never load"), 0o600); err != nil {
		t.Fatal(err)
	}
	uciWriteTreeSitterProjectCommandTraps(t, trapDirectory, marker)

	originalDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if restoreErr := os.Chdir(originalDirectory); restoreErr != nil {
			t.Errorf("restore working directory: %v", restoreErr)
		}
	})
	if err := os.Chdir(projectRoot); err != nil {
		t.Fatal(err)
	}

	t.Setenv("ENGRAM_API_KEY", "parent-credential-must-not-reach-child")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "parent-credential-must-not-reach-child")
	t.Setenv("GITHUB_TOKEN", "parent-credential-must-not-reach-child")
	t.Setenv("NODE_OPTIONS", "--require project-plugin")
	t.Setenv("PATH", trapDirectory)

	fixture := uciTreeSitterFixtureForLanguage(t, TreeSitterLanguageTSX)
	environment := append(uciTreeSitterMinimalEnvironment(),
		"PATH="+trapDirectory,
		"UCI_TREE_SITTER_TEST_ALLOWED=one",
	)
	artifact := uciParseTreeSitter(t, TreeSitterParseRequest{
		Language:   fixture.language,
		ProfileKey: fixture.profileKey,
		Source:     []byte(fixture.source),
	}, uciTreeSitterWorkerOptions{
		mode:        "environment",
		environment: environment,
		projectRoot: projectRoot,
	})
	uciRequireTreeSitterFixtureArtifact(t, fixture, artifact)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parser worker executed a project-controlled package command: stat %q err = %v", marker, err)
	}
}

// TestUCITreeSitterWorkerProcessHelper is an external protocol fixture. The
// parent must send one bounded JSON line to this independently launched process
// and accept only one bounded JSON response; no in-process parser substitute can
// satisfy its PID and framing checks.
func TestUCITreeSitterWorkerProcessHelper(t *testing.T) {
	if *uciTreeSitterHelperMode == "" {
		return
	}

	request := uciReadTreeSitterWireRequest(t)
	uciWriteTreeSitterChildAudit(t)
	switch *uciTreeSitterHelperMode {
	case "oversized":
		_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 4096))
		return
	case "stall":
		for {
			time.Sleep(time.Hour)
		}
	case "protocol", "record", "environment":
		response := uciTreeSitterWireResponseFor(t, request)
		if *uciTreeSitterHelperMode == "environment" && uciTreeSitterHelperEnvironmentUnsafe() {
			response.BundleDigest = uciTreeSitterBundleDigest('f')
		}
		if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
			t.Fatalf("write Tree-sitter fixture response: %v", err)
		}
	default:
		t.Fatalf("unknown Tree-sitter fixture-child mode %q", *uciTreeSitterHelperMode)
	}
	os.Exit(0)
}

func uciParseTreeSitterFixture(t *testing.T, fixture uciTreeSitterFixture) TreeSitterArtifact {
	t.Helper()
	return uciParseTreeSitter(t, TreeSitterParseRequest{
		Language:   fixture.language,
		ProfileKey: fixture.profileKey,
		Source:     []byte(fixture.source),
	}, uciTreeSitterWorkerOptions{})
}

func uciParseTreeSitter(t *testing.T, request TreeSitterParseRequest, options uciTreeSitterWorkerOptions) TreeSitterArtifact {
	t.Helper()
	worker := uciNewTreeSitterTestWorker(t, options)
	artifact, err := worker.Parse(context.Background(), request)
	if err != nil {
		t.Fatalf("Parse(%q): %v", request.Language, err)
	}
	if options.auditFile != "" {
		uciRequireTreeSitterChildAudit(t, options.auditFile)
	}
	return artifact
}

func uciNewTreeSitterTestWorker(t *testing.T, options uciTreeSitterWorkerOptions) *TreeSitterWorker {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if options.mode == "" {
		options.mode = "protocol"
	}
	if options.expectedBundleDigest == "" {
		options.expectedBundleDigest = uciTreeSitterBundleDigest('a')
	}
	if options.reportedBundleDigest == "" {
		options.reportedBundleDigest = options.expectedBundleDigest
	}
	if options.maxInputBytes == 0 {
		options.maxInputBytes = 1 << 20
	}
	if options.maxOutputBytes == 0 {
		options.maxOutputBytes = 1 << 20
	}
	if options.timeout == 0 {
		options.timeout = 2 * time.Second
	}
	if options.environment == nil {
		options.environment = uciTreeSitterMinimalEnvironment()
	}

	arguments := []string{
		"-test.run=" + uciTreeSitterHelperTest,
		"-test.count=1",
		"-test.v=false",
		"-uci-tree-sitter-helper-mode=" + options.mode,
		"-uci-tree-sitter-helper-bundle-digest=" + string(options.reportedBundleDigest),
		"-uci-tree-sitter-helper-allowed-environment=" + strings.Join(uciTreeSitterEnvironmentNames(options.environment), ","),
	}
	if options.auditFile != "" {
		arguments = append(arguments, "-uci-tree-sitter-helper-audit-file="+options.auditFile)
	}
	if options.projectRoot != "" {
		arguments = append(arguments, "-uci-tree-sitter-helper-project-root="+options.projectRoot)
	}

	worker, err := NewTreeSitterWorker(TreeSitterWorkerConfig{
		ExecutablePath:       executable,
		Arguments:            arguments,
		ExpectedBundleDigest: options.expectedBundleDigest,
		MaxInputBytes:        options.maxInputBytes,
		MaxOutputBytes:       options.maxOutputBytes,
		Timeout:              options.timeout,
		Environment:          append([]string(nil), options.environment...),
	})
	if err != nil {
		t.Fatalf("NewTreeSitterWorker(): %v", err)
	}
	return worker
}

func uciBuildTreeSitterParser(t *testing.T) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve Tree-sitter worker test source path")
	}
	executable := filepath.Join(t.TempDir(), "uci-parser")
	if runtime.GOOS == "windows" {
		executable += ".exe"
	}
	command := exec.Command("go", "build", "-o", executable, "./tools/uci-parser")
	command.Dir = filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", ".."))
	command.Env = append(os.Environ(), "CGO_ENABLED=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build bundled Tree-sitter parser: %v\n%s", err, output)
	}
	return executable
}

func uciBuiltTreeSitterBundleDigest(t *testing.T, executable string) IndexDigest {
	t.Helper()
	request := TreeSitterWorkerWireRequest{
		Version:    TreeSitterWorkerProtocolVersion,
		Language:   TreeSitterLanguageJavaScript,
		ProfileKey: "javascript-tree-sitter-v2",
		Source:     []byte("export const parserBundleProbe = true;\n"),
	}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("encode built parser probe request: %v", err)
	}
	command := exec.Command(executable)
	command.Dir = t.TempDir()
	command.Env = uciTreeSitterMinimalEnvironment()
	command.Stdin = bytes.NewReader(append(encoded, '\n'))
	output, err := command.Output()
	if err != nil {
		t.Fatalf("run built parser bundle probe: %v", err)
	}
	response, err := treeSitterDecodeWireResponse(output)
	if err != nil {
		t.Fatalf("decode built parser bundle probe: %v", err)
	}
	if response.Version != TreeSitterWorkerProtocolVersion || !treeSitterDigestValid(response.BundleDigest) {
		t.Fatalf("built parser probe returned invalid protocol identity: %#v", response)
	}
	return response.BundleDigest
}

func uciNewBuiltTreeSitterWorker(t *testing.T, executable string, bundleDigest IndexDigest) *TreeSitterWorker {
	t.Helper()
	worker, err := NewTreeSitterWorker(TreeSitterWorkerConfig{
		ExecutablePath:       executable,
		ExpectedBundleDigest: bundleDigest,
		MaxInputBytes:        1 << 20,
		MaxOutputBytes:       1 << 20,
		Timeout:              5 * time.Second,
		Environment:          uciTreeSitterMinimalEnvironment(),
	})
	if err != nil {
		t.Fatalf("NewTreeSitterWorker(built parser): %v", err)
	}
	return worker
}

func uciRequireBuiltTreeSitterFacts(t *testing.T, artifact TreeSitterArtifact, definitionNames, referenceKinds []string, defaultImportTarget, commonJSImportTarget, selfClosingComponent string) {
	t.Helper()
	if artifact.Coverage != IndexCoverageComplete {
		t.Fatalf("built parser coverage = %q, want %q; diagnostics=%#v", artifact.Coverage, IndexCoverageComplete, artifact.Diagnostics)
	}
	uciRequireTreeSitterArtifactProof(t, artifact)
	for _, name := range definitionNames {
		uciRequireBuiltDefinitionName(t, artifact.Definitions, name)
	}

	seenKinds := make(map[string]struct{}, len(artifact.References))
	seenSiteKeys := make(map[string]struct{}, len(artifact.References))
	seenSymbolKeys := make(map[string]struct{}, len(artifact.References))
	defaultImportSeen := false
	commonJSImportSeen := commonJSImportTarget == ""
	selfClosingSeen := selfClosingComponent == ""
	repeatedCalls := make(map[string]struct{})
	for _, reference := range artifact.References {
		if reference.TargetKey != "" {
			t.Fatalf("built parser fabricated target %q for %#v", reference.TargetKey, reference)
		}
		suffix := TreeSitterReferenceSiteKey("", reference.Span)
		if !strings.HasSuffix(reference.LocalKey, suffix) || !strings.HasSuffix(reference.SymbolKey, suffix) {
			t.Fatalf("built parser reference site is not span-bound: %#v", reference)
		}
		if _, exists := seenSiteKeys[reference.LocalKey]; exists {
			t.Fatalf("built parser collapsed repeated site key %q", reference.LocalKey)
		}
		if _, exists := seenSymbolKeys[reference.SymbolKey]; exists {
			t.Fatalf("built parser collapsed repeated symbol key %q", reference.SymbolKey)
		}
		seenSiteKeys[reference.LocalKey] = struct{}{}
		seenSymbolKeys[reference.SymbolKey] = struct{}{}
		seenKinds[reference.Kind] = struct{}{}
		if reference.Kind == "import_alias" && reference.RawTarget == defaultImportTarget {
			defaultImportSeen = true
		}
		if reference.Kind == "import_alias" && reference.RawTarget == commonJSImportTarget {
			commonJSImportSeen = true
		}
		if reference.Kind == "call" && reference.RawTarget == "DefaultValue" {
			repeatedCalls[reference.LocalKey] = struct{}{}
		}
		if reference.Kind == "jsx_reference" && reference.RawTarget == selfClosingComponent {
			start, end := int(reference.Span.ByteStart), int(reference.Span.ByteEnd)
			if start == 0 || end > len(artifact.Text) || artifact.Text[start-1] != '<' || !strings.HasPrefix(artifact.Text[end:], " />") {
				t.Fatalf("built parser did not retain self-closing JSX name span: %#v", reference)
			}
			selfClosingSeen = true
		}
	}
	for _, kind := range referenceKinds {
		if _, found := seenKinds[kind]; !found {
			t.Fatalf("built parser omitted %q from its closed vocabulary: %#v", kind, artifact.References)
		}
	}
	if !defaultImportSeen {
		t.Fatalf("built parser omitted default import binding %q: %#v", defaultImportTarget, artifact.References)
	}
	if !commonJSImportSeen {
		t.Fatalf("built parser omitted CommonJS import binding %q: %#v", commonJSImportTarget, artifact.References)
	}
	if !selfClosingSeen {
		t.Fatalf("built parser omitted self-closing JSX component %q: %#v", selfClosingComponent, artifact.References)
	}
	if len(repeatedCalls) != 2 {
		t.Fatalf("built parser collapsed repeated DefaultValue calls: %#v", artifact.References)
	}
}

func uciRequireBuiltDefinitionName(t *testing.T, definitions []TreeSitterDefinition, want string) {
	t.Helper()
	for _, definition := range definitions {
		if definition.Name != want {
			continue
		}
		if strings.ContainsAny(definition.Name, "{}[]") {
			t.Fatalf("definition name is a raw binding pattern: %#v", definition)
		}
		return
	}
	t.Fatalf("built parser omitted definition name %q: %#v", want, definitions)
}

func uciRequireTreeSitterUnresolvedReference(t *testing.T, references []TreeSitterReferenceSite, kind, rawTarget string, resolution IndexResolutionState) {
	t.Helper()
	for _, reference := range references {
		if reference.Kind != kind || reference.RawTarget != rawTarget {
			continue
		}
		if reference.TargetKey != "" || reference.Resolution != resolution {
			t.Fatalf("reference %q/%q = %#v, want unresolved %q", kind, rawTarget, reference, resolution)
		}
		return
	}
	t.Fatalf("missing %q/%q reference: %#v", kind, rawTarget, references)
}

func uciRequireTreeSitterFixtureArtifact(t *testing.T, fixture uciTreeSitterFixture, artifact TreeSitterArtifact) {
	t.Helper()
	if artifact.Language != fixture.language {
		t.Fatalf("Parse() language = %q, want %q", artifact.Language, fixture.language)
	}
	if artifact.Coverage != IndexCoverageComplete {
		t.Fatalf("Parse(%q) coverage = %q, want %q", fixture.language, artifact.Coverage, IndexCoverageComplete)
	}
	if artifact.Text != fixture.source {
		t.Fatalf("Parse(%q) text = %q, want exact fixture source", fixture.language, artifact.Text)
	}
	uciRequireTreeSitterArtifactProof(t, artifact)
	for _, expected := range fixture.definitions {
		uciRequireTreeSitterDefinition(t, artifact.Definitions, expected, []byte(fixture.source))
	}
	for _, expected := range fixture.references {
		uciRequireTreeSitterReference(t, artifact.References, expected, []byte(fixture.source))
	}
}

func uciRequireTreeSitterArtifactProof(t *testing.T, artifact TreeSitterArtifact) {
	t.Helper()
	if !canonicalContextUUID(artifact.Proof.ArtifactID) {
		t.Fatalf("artifact ID %q is not a canonical UUID", artifact.Proof.ArtifactID)
	}
	if !isIndexDigest(artifact.Proof.ContentDigest) {
		t.Fatalf("content digest %q is not stable", artifact.Proof.ContentDigest)
	}
	if !isIndexDigest(artifact.Proof.FactsDigest) {
		t.Fatalf("facts digest %q is not stable", artifact.Proof.FactsDigest)
	}
	if !isIndexDigest(artifact.BundleDigest) {
		t.Fatalf("bundle digest %q is not stable", artifact.BundleDigest)
	}
	if artifact.Proof.DefinitionCount != uint64(len(artifact.Definitions)) {
		t.Fatalf("definition count = %d, want %d", artifact.Proof.DefinitionCount, len(artifact.Definitions))
	}
	if artifact.Proof.ReferenceSiteCount != uint64(len(artifact.References)) {
		t.Fatalf("reference count = %d, want %d", artifact.Proof.ReferenceSiteCount, len(artifact.References))
	}
	if artifact.Proof.ChunkCount != uint64(len(artifact.Chunks)) {
		t.Fatalf("chunk count = %d, want %d", artifact.Proof.ChunkCount, len(artifact.Chunks))
	}
	if len(artifact.Chunks) == 0 {
		t.Fatal("Parse() returned no source chunks")
	}
	for index, chunk := range artifact.Chunks {
		if !isIndexDigest(chunk.ContentDigest) {
			t.Fatalf("chunk %d content digest %q is not stable", index, chunk.ContentDigest)
		}
		if chunk.Span.ByteStart < 0 || chunk.Span.ByteEnd <= chunk.Span.ByteStart || chunk.Span.ByteEnd > int64(len(artifact.Text)) {
			t.Fatalf("chunk %d has invalid byte span %#v for %d-byte text", index, chunk.Span, len(artifact.Text))
		}
		if chunk.Span.LineStart < 1 || chunk.Span.LineEnd < chunk.Span.LineStart {
			t.Fatalf("chunk %d has invalid line span %#v", index, chunk.Span)
		}
		if want := artifact.Text[chunk.Span.ByteStart:chunk.Span.ByteEnd]; chunk.Text != want {
			t.Fatalf("chunk %d text = %q, want source span %q", index, chunk.Text, want)
		}
	}
}

func uciRequireTreeSitterDefinition(t *testing.T, definitions []TreeSitterDefinition, expected uciTreeSitterExpectedDefinition, source []byte) {
	t.Helper()
	for _, definition := range definitions {
		if definition.Kind != expected.kind || definition.SymbolKey != expected.symbolKey {
			continue
		}
		if definition.Name != expected.name {
			t.Fatalf("definition %q name = %q, want %q", expected.symbolKey, definition.Name, expected.name)
		}
		if definition.LocalKey != expected.localKey {
			t.Fatalf("definition %q local key = %q, want %q", expected.symbolKey, definition.LocalKey, expected.localKey)
		}
		uciRequireTreeSitterSpan(t, definition.Span, uciTreeSitterSpan(t, source, expected.fragment, expected.occurrence), "definition "+expected.symbolKey)
		return
	}
	t.Fatalf("missing %s definition %q; got %#v", expected.kind, expected.symbolKey, definitions)
}

func uciRequireTreeSitterReference(t *testing.T, references []TreeSitterReferenceSite, expected uciTreeSitterExpectedReference, source []byte) {
	t.Helper()
	span := uciTreeSitterSpan(t, source, expected.fragment, expected.occurrence)
	wantLocalKey := TreeSitterReferenceSiteKey(expected.localKey, span)
	wantSymbolKey := TreeSitterReferenceSiteKey(expected.symbolKey, span)
	for _, reference := range references {
		if reference.Kind != expected.kind || reference.SymbolKey != wantSymbolKey {
			continue
		}
		if reference.LocalKey != wantLocalKey {
			t.Fatalf("reference %q local key = %q, want %q", wantSymbolKey, reference.LocalKey, wantLocalKey)
		}
		if reference.OwnerLocalKey != expected.ownerLocalKey {
			t.Fatalf("reference %q owner = %q, want %q", wantSymbolKey, reference.OwnerLocalKey, expected.ownerLocalKey)
		}
		if reference.RawTarget != expected.rawTarget {
			t.Fatalf("reference %q raw target = %q, want %q", wantSymbolKey, reference.RawTarget, expected.rawTarget)
		}
		if reference.TargetKey != "" {
			t.Fatalf("reference %q fabricated resolved target %q", wantSymbolKey, reference.TargetKey)
		}
		if reference.Resolution != expected.resolution {
			t.Fatalf("reference %q resolution = %q, want syntax-level %q", wantSymbolKey, reference.Resolution, expected.resolution)
		}
		uciRequireTreeSitterSpan(t, reference.Span, span, "reference "+wantSymbolKey)
		return
	}
	t.Fatalf("missing %s reference %q; got %#v", expected.kind, wantSymbolKey, references)
}

func uciRequireTreeSitterDiagnostic(t *testing.T, diagnostics []TreeSitterDiagnostic, code string) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			return
		}
	}
	t.Fatalf("missing diagnostic %q; got %#v", code, diagnostics)
}

func uciRequireTreeSitterStructFields(t *testing.T, value any, expected []uciTreeSitterTestField) {
	t.Helper()
	typ := reflect.TypeOf(value)
	if typ.NumField() != len(expected) {
		t.Fatalf("%s field count = %d, want %d", typ, typ.NumField(), len(expected))
	}
	for index, field := range expected {
		actual := typ.Field(index)
		if actual.Name != field.name || actual.Type != field.typ {
			t.Fatalf("%s field %d = %s %s, want %s %s", typ, index, actual.Name, actual.Type, field.name, field.typ)
		}
	}
}

func uciTreeSitterFixtureForLanguage(t *testing.T, language TreeSitterLanguage) uciTreeSitterFixture {
	t.Helper()
	for _, fixture := range uciTreeSitterFixtures {
		if fixture.language == language {
			return fixture
		}
	}
	t.Fatalf("missing Tree-sitter fixture for %q", language)
	return uciTreeSitterFixture{}
}

func uciTreeSitterBundleDigest(character rune) IndexDigest {
	return IndexDigest("sha256:" + strings.Repeat(string(character), 64))
}

func uciTreeSitterMinimalEnvironment() []string {
	environment := make([]string, 0, 3)
	for _, name := range []string{"SYSTEMROOT", "WINDIR", "COMSPEC"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func uciTreeSitterEnvironmentNames(environment []string) []string {
	names := make([]string, 0, len(environment))
	seen := make(map[string]struct{}, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" {
			continue
		}
		upper := strings.ToUpper(name)
		if _, exists := seen[upper]; exists {
			continue
		}
		seen[upper] = struct{}{}
		names = append(names, upper)
	}
	return names
}

func uciTreeSitterSpan(t *testing.T, source []byte, fragment string, occurrence int) IndexSpan {
	t.Helper()
	span, found := uciTreeSitterSpanFor(source, fragment, occurrence)
	if !found {
		t.Fatalf("fragment %q occurrence %d not found in fixture", fragment, occurrence)
	}
	return span
}

func uciTreeSitterSpanFor(source []byte, fragment string, occurrence int) (IndexSpan, bool) {
	searchFrom := 0
	for found := 0; ; found++ {
		offset := bytes.Index(source[searchFrom:], []byte(fragment))
		if offset < 0 {
			return IndexSpan{}, false
		}
		offset += searchFrom
		if found == occurrence {
			end := offset + len(fragment)
			lineEndOffset := offset
			if end > offset {
				lineEndOffset = end - 1
			}
			return IndexSpan{
				ByteStart: int64(offset),
				ByteEnd:   int64(end),
				LineStart: bytes.Count(source[:offset], []byte("\n")) + 1,
				LineEnd:   bytes.Count(source[:lineEndOffset], []byte("\n")) + 1,
			}, true
		}
		searchFrom = offset + len(fragment)
	}
}

func uciRequireTreeSitterSpan(t *testing.T, got, want IndexSpan, subject string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s span = %#v, want %#v", subject, got, want)
	}
}

func uciReadTreeSitterWireRequest(t *testing.T) uciTreeSitterWireRequest {
	t.Helper()
	var request uciTreeSitterWireRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		t.Fatalf("decode Tree-sitter fixture request: %v", err)
	}
	if request.Version != uciTreeSitterWireVersion {
		t.Fatalf("Tree-sitter request version = %q, want %q", request.Version, uciTreeSitterWireVersion)
	}
	if request.Language == "" || request.ProfileKey == "" || len(request.Source) == 0 {
		t.Fatalf("Tree-sitter request is not a bounded language/profile/source frame: %#v", request)
	}
	return request
}

func uciTreeSitterWireResponseFor(t *testing.T, request uciTreeSitterWireRequest) uciTreeSitterWireResponse {
	t.Helper()
	bundleDigest := IndexDigest(*uciTreeSitterHelperBundleDigest)
	if !isIndexDigest(bundleDigest) {
		t.Fatalf("fixture child bundle digest %q is invalid", bundleDigest)
	}
	for _, fixture := range uciTreeSitterFixtures {
		if string(fixture.language) != request.Language || fixture.source != string(request.Source) {
			continue
		}
		return uciTreeSitterWireResponseFromFixture(t, fixture, bundleDigest)
	}

	if request.Language == string(TreeSitterLanguageTypeScript) && string(request.Source) == "export const stable = 1;\nexport function broken(\n" {
		span, found := uciTreeSitterSpanFor(request.Source, "export const stable = 1;", 0)
		if !found {
			t.Fatal("malformed TypeScript fixture lost its stable declaration span")
		}
		return uciTreeSitterWireResponse{
			Version:      uciTreeSitterWireVersion,
			BundleDigest: bundleDigest,
			Coverage:     IndexCoveragePartial,
			Text:         string(request.Source),
			Definitions: []uciTreeSitterWireDefinition{{
				Kind:      "const",
				Name:      "stable",
				SymbolKey: "typescript:const:stable",
				LocalKey:  "const:stable",
				Span:      span,
			}},
			Chunks: uciTreeSitterWireChunks(request.Source),
			Diagnostics: []uciTreeSitterWireDiagnostic{{
				Code:    "PARSE_ERROR",
				Message: "source could not be parsed completely as TypeScript",
			}},
		}
	}
	if request.Language == "unsupported" {
		return uciTreeSitterWireResponse{
			Version:      uciTreeSitterWireVersion,
			BundleDigest: bundleDigest,
			Coverage:     IndexCoverageUnavailable,
			Text:         string(request.Source),
			Chunks:       uciTreeSitterWireChunks(request.Source),
			Diagnostics: []uciTreeSitterWireDiagnostic{{
				Code:    "UNSUPPORTED_LANGUAGE",
				Message: "no parser grammar is bundled for the requested language",
			}},
		}
	}

	return uciTreeSitterWireResponse{
		Version:      uciTreeSitterWireVersion,
		BundleDigest: bundleDigest,
		Coverage:     IndexCoverageComplete,
		Text:         string(request.Source),
		Chunks:       uciTreeSitterWireChunks(request.Source),
	}
}

func uciTreeSitterWireResponseFromFixture(t *testing.T, fixture uciTreeSitterFixture, bundleDigest IndexDigest) uciTreeSitterWireResponse {
	t.Helper()
	source := []byte(fixture.source)
	response := uciTreeSitterWireResponse{
		Version:      uciTreeSitterWireVersion,
		BundleDigest: bundleDigest,
		Coverage:     IndexCoverageComplete,
		Text:         fixture.source,
		Definitions:  make([]uciTreeSitterWireDefinition, 0, len(fixture.definitions)),
		References:   make([]uciTreeSitterWireReference, 0, len(fixture.references)),
		Chunks:       uciTreeSitterWireChunks(source),
	}
	for _, definition := range fixture.definitions {
		span, found := uciTreeSitterSpanFor(source, definition.fragment, definition.occurrence)
		if !found {
			t.Fatalf("missing definition fixture span for %q", definition.symbolKey)
		}
		response.Definitions = append(response.Definitions, uciTreeSitterWireDefinition{
			Kind:      definition.kind,
			Name:      definition.name,
			SymbolKey: definition.symbolKey,
			LocalKey:  definition.localKey,
			Span:      span,
		})
	}
	for _, reference := range fixture.references {
		span, found := uciTreeSitterSpanFor(source, reference.fragment, reference.occurrence)
		if !found {
			t.Fatalf("missing reference fixture span for %q", reference.symbolKey)
		}
		response.References = append(response.References, uciTreeSitterWireReference{
			Kind:          reference.kind,
			SymbolKey:     TreeSitterReferenceSiteKey(reference.symbolKey, span),
			LocalKey:      TreeSitterReferenceSiteKey(reference.localKey, span),
			OwnerLocalKey: reference.ownerLocalKey,
			RawTarget:     reference.rawTarget,
			Resolution:    reference.resolution,
			Span:          span,
		})
	}
	return response
}

func uciTreeSitterWireChunks(source []byte) []uciTreeSitterWireChunk {
	if len(source) == 0 {
		return nil
	}
	span, valid := goSpanFromOffsets(goLineStarts(source), len(source), 0, len(source))
	if !valid {
		return nil
	}
	return []uciTreeSitterWireChunk{{
		Span:          span,
		Text:          string(source),
		ContentDigest: goSourceDigest(source),
	}}
}

func uciWriteTreeSitterChildAudit(t *testing.T) {
	t.Helper()
	if *uciTreeSitterHelperAuditFile == "" {
		return
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(uciTreeSitterWorkerAudit{PID: os.Getpid(), CWD: workingDirectory})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(*uciTreeSitterHelperAuditFile, encoded, 0o600); err != nil {
		t.Fatalf("write Tree-sitter child audit: %v", err)
	}
}

func uciRequireTreeSitterChildAudit(t *testing.T, auditFile string) uciTreeSitterWorkerAudit {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		encoded, err := os.ReadFile(auditFile)
		if err == nil {
			var audit uciTreeSitterWorkerAudit
			if decodeErr := json.Unmarshal(encoded, &audit); decodeErr != nil {
				t.Fatalf("decode Tree-sitter child audit: %v", decodeErr)
			}
			if audit.PID <= 0 || audit.PID == os.Getpid() {
				t.Fatalf("parser worker PID = %d; expected an external child process", audit.PID)
			}
			return audit
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read Tree-sitter child audit %q: %v", auditFile, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("Tree-sitter child did not create audit %q", auditFile)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func uciTreeSitterHelperEnvironmentUnsafe() bool {
	allowed := make(map[string]struct{})
	for _, name := range strings.Split(*uciTreeSitterHelperAllowedEnvironment, ",") {
		if name != "" {
			allowed[strings.ToUpper(name)] = struct{}{}
		}
	}
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if !found || name == "" {
			return true
		}
		if _, allowedName := allowed[strings.ToUpper(name)]; !allowedName {
			return true
		}
	}
	for _, credentialName := range []string{
		"ENGRAM_API_KEY",
		"AWS_SECRET_ACCESS_KEY",
		"GITHUB_TOKEN",
		"NODE_OPTIONS",
		"NPM_CONFIG_USERCONFIG",
		"npm_config_userconfig",
	} {
		if value := os.Getenv(credentialName); value != "" {
			return true
		}
	}
	return uciTreeSitterHelperRunsWithinProject()
}

func uciTreeSitterHelperRunsWithinProject() bool {
	if *uciTreeSitterHelperProjectRoot == "" {
		return false
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return true
	}
	projectRoot := filepath.Clean(*uciTreeSitterHelperProjectRoot)
	workingDirectory = filepath.Clean(workingDirectory)
	if runtime.GOOS == "windows" {
		projectRoot = strings.ToLower(projectRoot)
		workingDirectory = strings.ToLower(workingDirectory)
	}
	relative, err := filepath.Rel(projectRoot, workingDirectory)
	if err != nil {
		return true
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func uciWriteTreeSitterProjectCommandTraps(t *testing.T, directory, marker string) {
	t.Helper()
	for _, command := range []string{"npm", "npx", "pnpm", "yarn", "bun", "node"} {
		path := filepath.Join(directory, command)
		contents := "#!/bin/sh\nprintf project-command-executed > " + fmt.Sprintf("%q", marker) + "\nexit 97\n"
		mode := os.FileMode(0o700)
		if runtime.GOOS == "windows" {
			path += ".cmd"
			contents = "@echo off\r\necho project-command-executed>\"" + marker + "\"\r\nexit /b 97\r\n"
			mode = 0o600
		}
		if err := os.WriteFile(path, []byte(contents), mode); err != nil {
			t.Fatalf("write project-controlled %s command trap: %v", command, err)
		}
	}
}
