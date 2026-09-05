package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"testing"

	ucidomain "github.com/thebtf/engram/internal/uci"
	"golang.org/x/mod/modfile"
)

type parserManifest struct {
	SchemaVersion          int                    `json:"schema_version"`
	Component              string                 `json:"component"`
	ParserProtocolRevision string                 `json:"parser_protocol_revision"`
	BundleSchemaRevision   string                 `json:"bundle_schema_revision"`
	Toolchain              parserToolchain        `json:"toolchain"`
	Dependencies           []parserDependency     `json:"dependencies"`
	CompiledGrammars       []compiledGrammar      `json:"compiled_grammars"`
	BundleDigest           bundleDigestManifest   `json:"bundle_digest"`
	Targets                []parserTargetEvidence `json:"targets"`
}

type parserToolchain struct {
	ModuleGoVersion string                    `json:"module_go_version"`
	RequiresCGO     bool                      `json:"requires_cgo"`
	CGOSources      []string                  `json:"cgo_sources"`
	ObservedHost    observedToolchainEvidence `json:"observed_host"`
}

type observedToolchainEvidence struct {
	GoVersion          string `json:"go_version"`
	GOOS               string `json:"goos"`
	GOARCH             string `json:"goarch"`
	CGOEnabled         bool   `json:"cgo_enabled"`
	BuildEvidenceState string `json:"build_evidence_state"`
	Note               string `json:"note"`
}

type parserDependency struct {
	Module    string             `json:"module"`
	Version   string             `json:"version"`
	Checksums moduleChecksums    `json:"checksums"`
	Source    parserModuleSource `json:"source"`
	License   licenseEvidence    `json:"license"`
}

type moduleChecksums struct {
	Module string `json:"module"`
	GoMod  string `json:"go_mod"`
}

type parserModuleSource struct {
	RepositoryURL string `json:"repository_url"`
	SourceURL     string `json:"source_url"`
	Tag           string `json:"tag"`
	Commit        string `json:"commit"`
	ModuleInfoURL string `json:"module_info_url"`
}

type licenseEvidence struct {
	SPDX   string `json:"spdx"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type compiledGrammar struct {
	Language        string `json:"language"`
	Module          string `json:"module"`
	GoBindingImport string `json:"go_binding_import"`
	BindingSymbol   string `json:"binding_symbol"`
}

type bundleDigestManifest struct {
	Algorithm          string   `json:"algorithm"`
	Prefix             string   `json:"prefix"`
	InputEncoding      string   `json:"input_encoding"`
	StaticInputs       []string `json:"static_inputs"`
	StaticInputDigest  string   `json:"static_input_digest"`
	RuntimeInputRecipe []string `json:"runtime_input_recipe"`
}

type parserTargetEvidence struct {
	GOOS          string `json:"goos"`
	GOARCH        string `json:"goarch"`
	EvidenceState string `json:"evidence_state"`
	RequiredGate  string `json:"required_gate"`
}

var (
	exactVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	exactCommit  = regexp.MustCompile(`^[0-9a-f]{40}$`)

	expectedDependencies = []parserDependency{
		{
			Module:  "github.com/tree-sitter/go-tree-sitter",
			Version: "v0.25.0",
			Checksums: moduleChecksums{
				Module: "h1:sx6kcg8raRFCvc9BnXglke6axya12krCJF5xJ2sftRU=",
				GoMod:  "h1:r77ig7BikoZhHrrsjAnv8RqGti5rtSyvDHPzgTPsUuU=",
			},
			Source: parserModuleSource{
				RepositoryURL: "https://github.com/tree-sitter/go-tree-sitter",
				SourceURL:     "https://github.com/tree-sitter/go-tree-sitter/tree/adc13ffd8b2c0b01b878fda9f7c422ce0df5fad3",
				Tag:           "v0.25.0",
				Commit:        "adc13ffd8b2c0b01b878fda9f7c422ce0df5fad3",
				ModuleInfoURL: "https://proxy.golang.org/github.com/tree-sitter/go-tree-sitter/@v/v0.25.0.info",
			},
			License: licenseEvidence{
				SPDX:   "MIT",
				URL:    "https://raw.githubusercontent.com/tree-sitter/go-tree-sitter/adc13ffd8b2c0b01b878fda9f7c422ce0df5fad3/LICENSE",
				SHA256: "sha256:0eea8dc45e89deeb03c7799bbbc7b4688f365fb274562f4540ecfebdea82e727",
			},
		},
		{
			Module:  "github.com/tree-sitter/tree-sitter-javascript",
			Version: "v0.25.0",
			Checksums: moduleChecksums{
				Module: "h1:ZkWETb66/w8cc13yhfnNuHOLDQWl3BnKlH6f9AdR88c=",
				GoMod:  "h1:lmGD1EJdCA+v0S1u2fFgepMg/opzSg/4pgFym2FPGAs=",
			},
			Source: parserModuleSource{
				RepositoryURL: "https://github.com/tree-sitter/tree-sitter-javascript",
				SourceURL:     "https://github.com/tree-sitter/tree-sitter-javascript/tree/44c892e0be055ac465d5eeddae6d3e194424e7de",
				Tag:           "v0.25.0",
				Commit:        "44c892e0be055ac465d5eeddae6d3e194424e7de",
				ModuleInfoURL: "https://proxy.golang.org/github.com/tree-sitter/tree-sitter-javascript/@v/v0.25.0.info",
			},
			License: licenseEvidence{
				SPDX:   "MIT",
				URL:    "https://raw.githubusercontent.com/tree-sitter/tree-sitter-javascript/44c892e0be055ac465d5eeddae6d3e194424e7de/LICENSE",
				SHA256: "sha256:2e0110e07abef7c2548b26ec9d6969775617ca539a0dc8dbeeb14d6452c711d1",
			},
		},
		{
			Module:  "github.com/tree-sitter/tree-sitter-typescript",
			Version: "v0.23.2",
			Checksums: moduleChecksums{
				Module: "h1:/Odvphn18PniVixb9e97X0DbNVsU6Qocv9mfkyzdXwU=",
				GoMod:  "h1:zjzMXT/Ulffel2xfOcAkQQkiAkmgnbtPGlFQw/5X4xA=",
			},
			Source: parserModuleSource{
				RepositoryURL: "https://github.com/tree-sitter/tree-sitter-typescript",
				SourceURL:     "https://github.com/tree-sitter/tree-sitter-typescript/tree/f975a621f4e7f532fe322e13c4f79495e0a7b2e7",
				Tag:           "v0.23.2",
				Commit:        "f975a621f4e7f532fe322e13c4f79495e0a7b2e7",
				ModuleInfoURL: "https://proxy.golang.org/github.com/tree-sitter/tree-sitter-typescript/@v/v0.23.2.info",
			},
			License: licenseEvidence{
				SPDX:   "MIT",
				URL:    "https://raw.githubusercontent.com/tree-sitter/tree-sitter-typescript/f975a621f4e7f532fe322e13c4f79495e0a7b2e7/LICENSE",
				SHA256: "sha256:49bf33cf78ef5897e4e161ce1517df7de1ae5042a65b6bcfd44401e0fc606559",
			},
		},
	}

	expectedGrammars = []compiledGrammar{
		{
			Language:        "javascript",
			Module:          "github.com/tree-sitter/tree-sitter-javascript",
			GoBindingImport: "github.com/tree-sitter/tree-sitter-javascript/bindings/go",
			BindingSymbol:   "Language",
		},
		{
			Language:        "typescript",
			Module:          "github.com/tree-sitter/tree-sitter-typescript",
			GoBindingImport: "github.com/tree-sitter/tree-sitter-typescript/bindings/go",
			BindingSymbol:   "LanguageTypescript",
		},
		{
			Language:        "tsx",
			Module:          "github.com/tree-sitter/tree-sitter-typescript",
			GoBindingImport: "github.com/tree-sitter/tree-sitter-typescript/bindings/go",
			BindingSymbol:   "LanguageTSX",
		},
	}

	expectedStaticInputs = []string{
		"uci-tree-sitter-bundle/v1",
		"github.com/tree-sitter/go-tree-sitter@v0.25.0",
		"github.com/tree-sitter/tree-sitter-javascript@v0.25.0",
		"github.com/tree-sitter/tree-sitter-typescript@v0.23.2",
	}

	expectedTargets = []parserTargetEvidence{
		{
			GOOS:          "windows",
			GOARCH:        "amd64",
			EvidenceState: "not_run",
			RequiredGate:  "CGO_ENABLED=1 go build ./tools/uci-parser",
		},
		{
			GOOS:          "linux",
			GOARCH:        "amd64",
			EvidenceState: "not_run",
			RequiredGate:  "GOOS=linux GOARCH=amd64 CGO_ENABLED=1 CC=<linux-amd64-c-compiler> go build ./tools/uci-parser",
		},
		{
			GOOS:          "darwin",
			GOARCH:        "amd64",
			EvidenceState: "not_run",
			RequiredGate:  "GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 CC=<darwin-amd64-c-compiler> go build ./tools/uci-parser",
		},
		{
			GOOS:          "darwin",
			GOARCH:        "arm64",
			EvidenceState: "not_run",
			RequiredGate:  "GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 CC=<darwin-arm64-c-compiler> go build ./tools/uci-parser",
		},
	}
)

func TestParserManifestProvenance(t *testing.T) {
	repoRoot := parserRepositoryRoot(t)
	manifestPath := filepath.Join(repoRoot, "tools", "uci-parser", "manifest.json")
	manifestBytes := readParserFile(t, manifestPath)
	manifest := parseParserManifest(t, manifestBytes)

	if manifest.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", manifest.SchemaVersion)
	}
	if manifest.Component != "uci-parser" {
		t.Fatalf("component = %q, want uci-parser", manifest.Component)
	}
	if manifest.ParserProtocolRevision != "uci-tree-sitter/v1" {
		t.Fatalf("parser_protocol_revision = %q, want uci-tree-sitter/v1", manifest.ParserProtocolRevision)
	}
	if manifest.BundleSchemaRevision != "uci-tree-sitter-bundle/v1" {
		t.Fatalf("bundle_schema_revision = %q, want uci-tree-sitter-bundle/v1", manifest.BundleSchemaRevision)
	}
	assertNoFloatingProvenance(t, manifestBytes, manifest.Dependencies)
	assertToolchainProvenance(t, manifest.Toolchain)
	assertDependencyProvenance(t, repoRoot, manifest.Dependencies, manifest.Toolchain.ModuleGoVersion)
	assertCompiledGrammars(t, manifest.CompiledGrammars)
	assertBundleDigestProvenance(t, manifest.BundleDigest)
	assertTargetEvidence(t, manifest.Targets)
	assertParserSourceIdentity(t, repoRoot, manifest)
	assertParentProtocolIdentity(t, repoRoot, manifest)
}

func parseParserManifest(t *testing.T, data []byte) parserManifest {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest parserManifest
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatalf("decode parser manifest: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		t.Fatalf("parser manifest contains trailing JSON: %v", err)
	}
	return manifest
}

func assertNoFloatingProvenance(t *testing.T, rawManifest []byte, dependencies []parserDependency) {
	t.Helper()

	for _, forbidden := range []string{"latest", "refs/heads/", "/tree/main", "/tree/master", "@main", "@master"} {
		if strings.Contains(strings.ToLower(string(rawManifest)), forbidden) {
			t.Fatalf("manifest contains forbidden floating reference %q", forbidden)
		}
	}
	for _, dependency := range dependencies {
		if !exactVersion.MatchString(dependency.Version) {
			t.Fatalf("%s version %q is not an exact release version", dependency.Module, dependency.Version)
		}
		if dependency.Source.Tag != dependency.Version {
			t.Fatalf("%s source tag %q does not match version %q", dependency.Module, dependency.Source.Tag, dependency.Version)
		}
		if !exactCommit.MatchString(dependency.Source.Commit) {
			t.Fatalf("%s source commit %q is not a full immutable commit", dependency.Module, dependency.Source.Commit)
		}
	}
}

func assertToolchainProvenance(t *testing.T, toolchain parserToolchain) {
	t.Helper()

	expectedCGOSources := []string{
		"https://github.com/tree-sitter/go-tree-sitter/blob/adc13ffd8b2c0b01b878fda9f7c422ce0df5fad3/tree_sitter.go",
		"https://github.com/tree-sitter/tree-sitter-javascript/blob/44c892e0be055ac465d5eeddae6d3e194424e7de/bindings/go/binding.go",
		"https://github.com/tree-sitter/tree-sitter-typescript/blob/f975a621f4e7f532fe322e13c4f79495e0a7b2e7/bindings/go/typescript.go",
		"https://github.com/tree-sitter/tree-sitter-typescript/blob/f975a621f4e7f532fe322e13c4f79495e0a7b2e7/bindings/go/tsx.go",
	}
	if !toolchain.RequiresCGO {
		t.Fatal("parser manifest must declare the CGO requirement")
	}
	if !reflect.DeepEqual(toolchain.CGOSources, expectedCGOSources) {
		t.Fatalf("cgo_sources = %#v, want %#v", toolchain.CGOSources, expectedCGOSources)
	}
	if got, want := toolchain.ObservedHost, (observedToolchainEvidence{
		GoVersion:          "go1.26.6",
		GOOS:               "windows",
		GOARCH:             "amd64",
		CGOEnabled:         true,
		BuildEvidenceState: "not_run",
		Note:               "Toolchain facts were observed only; no uci-parser build or test was executed for this manifest.",
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("observed_host = %#v, want %#v", got, want)
	}
}

func assertDependencyProvenance(t *testing.T, repoRoot string, dependencies []parserDependency, manifestGoVersion string) {
	t.Helper()

	if !reflect.DeepEqual(dependencies, expectedDependencies) {
		t.Fatalf("dependencies = %#v, want %#v", dependencies, expectedDependencies)
	}

	goModPath := filepath.Join(repoRoot, "go.mod")
	goModBytes := readParserFile(t, goModPath)
	goMod, err := modfile.Parse(goModPath, goModBytes, nil)
	if err != nil {
		t.Fatalf("parse go.mod: %v", err)
	}
	if goMod.Go == nil {
		t.Fatal("go.mod does not declare a Go version")
	}
	if got, want := goMod.Go.Version, "1.26.6"; got != want {
		t.Fatalf("go.mod Go version = %q, want %q", got, want)
	}

	requirements := make(map[string]*modfile.Require)
	for _, requirement := range goMod.Require {
		requirements[requirement.Mod.Path] = requirement
	}
	checksums := parseGoSum(t, filepath.Join(repoRoot, "go.sum"))
	for _, dependency := range dependencies {
		requirement := requirements[dependency.Module]
		if requirement == nil {
			t.Fatalf("go.mod does not require %s", dependency.Module)
		}
		if requirement.Indirect {
			t.Fatalf("go.mod marks bundled dependency %s indirect", dependency.Module)
		}
		if got, want := requirement.Mod.Version, dependency.Version; got != want {
			t.Fatalf("go.mod %s version = %q, want %q", dependency.Module, got, want)
		}
		if got, want := checksums[dependency.Module+" "+dependency.Version], dependency.Checksums.Module; got != want {
			t.Fatalf("go.sum module checksum for %s = %q, want %q", dependency.Module, got, want)
		}
		if got, want := checksums[dependency.Module+" "+dependency.Version+"/go.mod"], dependency.Checksums.GoMod; got != want {
			t.Fatalf("go.sum go.mod checksum for %s = %q, want %q", dependency.Module, got, want)
		}
	}
	if got, want := manifestGoVersion, goMod.Go.Version; got != want {
		t.Fatalf("manifest toolchain Go version = %q, want go.mod Go version %q", got, want)
	}
}

func parseGoSum(t *testing.T, path string) map[string]string {
	t.Helper()

	checksums := make(map[string]string)
	for lineNumber, line := range strings.Split(string(readParserFile(t, path)), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			t.Fatalf("go.sum line %d has %d fields", lineNumber+1, len(fields))
		}
		key := fields[0] + " " + fields[1]
		if previous, exists := checksums[key]; exists && previous != fields[2] {
			t.Fatalf("go.sum has conflicting checksums for %s", key)
		}
		checksums[key] = fields[2]
	}
	return checksums
}

func assertCompiledGrammars(t *testing.T, grammars []compiledGrammar) {
	t.Helper()

	if !reflect.DeepEqual(grammars, expectedGrammars) {
		t.Fatalf("compiled_grammars = %#v, want %#v", grammars, expectedGrammars)
	}
}

func assertBundleDigestProvenance(t *testing.T, digest bundleDigestManifest) {
	t.Helper()

	if digest.Algorithm != "sha256" || digest.Prefix != "sha256:" {
		t.Fatalf("bundle digest algorithm/prefix = %q/%q, want sha256/sha256:", digest.Algorithm, digest.Prefix)
	}
	if digest.InputEncoding != "sort lexical; for each UTF-8 input, append its uint32 big-endian byte length then its bytes" {
		t.Fatalf("unexpected bundle input encoding %q", digest.InputEncoding)
	}
	if !reflect.DeepEqual(digest.StaticInputs, expectedStaticInputs) {
		t.Fatalf("bundle static inputs = %#v, want %#v", digest.StaticInputs, expectedStaticInputs)
	}
	if got, want := digest.RuntimeInputRecipe, []string{
		"go=runtime.Version()",
		"target=runtime.GOOS/runtime.GOARCH",
		"build-go=debug.ReadBuildInfo().GoVersion when nonempty",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bundle runtime input recipe = %#v, want %#v", got, want)
	}
	if got, want := staticBundleInputDigest(digest.StaticInputs), digest.StaticInputDigest; got != want {
		t.Fatalf("static bundle input digest = %q, want %q", got, want)
	}
	if digest.StaticInputDigest != "sha256:4c87311d322669ef5a02a6384d46caa30fcfca8f777e3aa81848c297147d2d13" {
		t.Fatalf("static bundle input digest = %q, want pinned digest", digest.StaticInputDigest)
	}
}

func staticBundleInputDigest(inputs []string) string {
	parts := append([]string(nil), inputs...)
	sort.Strings(parts)
	state := sha256.New()
	for _, part := range parts {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(part)))
		_, _ = state.Write(length[:])
		_, _ = state.Write([]byte(part))
	}
	return "sha256:" + hex.EncodeToString(state.Sum(nil))
}

func assertTargetEvidence(t *testing.T, targets []parserTargetEvidence) {
	t.Helper()

	if !reflect.DeepEqual(targets, expectedTargets) {
		t.Fatalf("target matrix = %#v, want %#v", targets, expectedTargets)
	}
	for _, target := range targets {
		if target.EvidenceState != "not_run" {
			t.Fatalf("target %s/%s claims %q without an executed build receipt", target.GOOS, target.GOARCH, target.EvidenceState)
		}
	}
}

func assertParserSourceIdentity(t *testing.T, _ string, manifest parserManifest) {
	t.Helper()

	wantDigest := runtimeBundleDigest(manifest.BundleDigest.StaticInputs)
	if got := bundleDigest(); got != wantDigest {
		t.Fatalf("runtime bundle digest = %q, want manifest-derived %q", got, wantDigest)
	}
	probeSources := map[string][]byte{
		"javascript": []byte("export const value = 1;\n"),
		"typescript": []byte("export interface Value { id: string }\n"),
		"tsx":        []byte("export const Value = () => <div />;\n"),
	}
	for _, grammar := range manifest.CompiledGrammars {
		language := ucidomain.TreeSitterLanguage(grammar.Language)
		loaded, supported := parserLanguage(language)
		if !supported || loaded == nil {
			t.Fatalf("compiled grammar %q is unavailable", grammar.Language)
		}
		response := extract(ucidomain.TreeSitterWorkerWireRequest{
			Version:    ucidomain.TreeSitterWorkerProtocolVersion,
			Language:   language,
			ProfileKey: "manifest-runtime-probe-v1",
			Source:     probeSources[grammar.Language],
		})
		if response.BundleDigest != wantDigest {
			t.Fatalf("%s response bundle digest = %q, want %q", grammar.Language, response.BundleDigest, wantDigest)
		}
		if response.Coverage == ucidomain.IndexCoverageUnavailable {
			t.Fatalf("compiled grammar %q returned unavailable coverage: %#v", grammar.Language, response.Diagnostics)
		}
	}
}

func runtimeBundleDigest(staticInputs []string) ucidomain.IndexDigest {
	parts := append([]string(nil), staticInputs...)
	parts = append(parts, "go="+runtime.Version(), "target="+runtime.GOOS+"/"+runtime.GOARCH)
	if build, ok := debug.ReadBuildInfo(); ok && build.GoVersion != "" {
		parts = append(parts, "build-go="+build.GoVersion)
	}
	sort.Strings(parts)
	state := sha256.New()
	for _, part := range parts {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(part)))
		_, _ = state.Write(length[:])
		_, _ = state.Write([]byte(part))
	}
	return ucidomain.IndexDigest("sha256:" + hex.EncodeToString(state.Sum(nil)))
}

func assertParentProtocolIdentity(t *testing.T, _ string, manifest parserManifest) {
	t.Helper()
	if got := ucidomain.TreeSitterWorkerProtocolVersion; got != manifest.ParserProtocolRevision {
		t.Fatalf("parent protocol revision = %q, want %q", got, manifest.ParserProtocolRevision)
	}
	wantLanguages := map[string]ucidomain.TreeSitterLanguage{
		"javascript": ucidomain.TreeSitterLanguageJavaScript,
		"typescript": ucidomain.TreeSitterLanguageTypeScript,
		"tsx":        ucidomain.TreeSitterLanguageTSX,
	}
	for _, grammar := range manifest.CompiledGrammars {
		if got, found := wantLanguages[grammar.Language]; !found || string(got) != grammar.Language {
			t.Fatalf("parent worker does not expose manifest language %q", grammar.Language)
		}
	}
}

func parserRepositoryRoot(t *testing.T) string {
	t.Helper()

	_, testPath, _, ok := runtimeCaller()
	if !ok {
		t.Fatal("resolve manifest test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(testPath), "..", ".."))
}

func runtimeCaller() (uintptr, string, int, bool) {
	return runtime.Caller(0)
}

func readParserFile(t *testing.T, path string) []byte {
	t.Helper()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return contents
}
