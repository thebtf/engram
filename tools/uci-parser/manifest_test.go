package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"testing"

	ucidomain "github.com/thebtf/engram/internal/uci"
	"golang.org/x/mod/modfile"
)

type parserManifest struct {
	SchemaVersion                   int                    `json:"schema_version"`
	Component                       string                 `json:"component"`
	ParserProtocolRevision          string                 `json:"parser_protocol_revision"`
	FactsExtractionContractRevision string                 `json:"facts_extraction_contract_revision"`
	BundleSchemaRevision            string                 `json:"bundle_schema_revision"`
	Toolchain                       parserToolchain        `json:"toolchain"`
	Dependencies                    []parserDependency     `json:"dependencies"`
	CompiledGrammars                []compiledGrammar      `json:"compiled_grammars"`
	BundleDigest                    bundleDigestManifest   `json:"bundle_digest"`
	Targets                         []parserTargetEvidence `json:"targets"`
}

type parserToolchain struct {
	ModuleGoVersion string   `json:"module_go_version"`
	RequiresCGO     bool     `json:"requires_cgo"`
	CGOSources      []string `json:"cgo_sources"`
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
	GOOS                    string                    `json:"goos"`
	GOARCH                  string                    `json:"goarch"`
	SupportStatus           string                    `json:"support_status"`
	EvidenceState           string                    `json:"evidence_state"`
	BuildCommand            string                    `json:"build_command"`
	CGOToolchainRequirement string                    `json:"cgo_toolchain_requirement"`
	SourceProbe             parserSourceProbeEvidence `json:"source_probe"`
}

type parserSourceProbeEvidence struct {
	Code                     string `json:"code"`
	BuildState               string `json:"build_state"`
	SmokeState               string `json:"smoke_state"`
	ObservedProtocolRevision string `json:"observed_protocol_revision"`
	ObservedBundleDigest     string `json:"observed_bundle_digest"`
	ObservedCoverage         string `json:"observed_coverage"`
	BlockerDetail            string `json:"blocker_detail"`
}

type parserSourceGrammar struct {
	GoBindingImport string
	BindingSymbol   string
}

type parserSourceContract struct {
	ImportPaths    map[string]string
	Grammars       map[string]parserSourceGrammar
	StaticInputs   []string
	BundleFunction *ast.FuncDecl
}

var (
	exactVersion = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)
	exactCommit  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	sha256Digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	goChecksum   = regexp.MustCompile(`^h1:[A-Za-z0-9+/]+={0,2}$`)

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

	expectedCGOSources = []string{
		"https://github.com/tree-sitter/go-tree-sitter/blob/adc13ffd8b2c0b01b878fda9f7c422ce0df5fad3/tree_sitter.go",
		"https://github.com/tree-sitter/tree-sitter-javascript/blob/44c892e0be055ac465d5eeddae6d3e194424e7de/bindings/go/binding.go",
		"https://github.com/tree-sitter/tree-sitter-typescript/blob/f975a621f4e7f532fe322e13c4f79495e0a7b2e7/bindings/go/typescript.go",
		"https://github.com/tree-sitter/tree-sitter-typescript/blob/f975a621f4e7f532fe322e13c4f79495e0a7b2e7/bindings/go/tsx.go",
	}

	expectedStaticInputs = []string{
		"uci-tree-sitter-bundle/v2",
		"uci-tree-sitter/v2",
		"uci-tree-sitter-facts/v2",
		"github.com/tree-sitter/go-tree-sitter@v0.25.0",
		"github.com/tree-sitter/tree-sitter-javascript@v0.25.0",
		"github.com/tree-sitter/tree-sitter-typescript@v0.23.2",
	}

	expectedTargets = []parserTargetEvidence{
		{
			GOOS:                    "windows",
			GOARCH:                  "amd64",
			SupportStatus:           "not_claimed",
			EvidenceState:           "not_reverified",
			BuildCommand:            "CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build ./tools/uci-parser",
			CGOToolchainRequirement: "a C compiler capable of GOOS/GOARCH configured as CC",
			SourceProbe: parserSourceProbeEvidence{
				Code:                     "not_reverified_after_facts_contract_revision",
				BuildState:               "not_run",
				SmokeState:               "not_run",
				ObservedProtocolRevision: "",
				ObservedBundleDigest:     "",
				ObservedCoverage:         "",
				BlockerDetail:            "The prior source probe covered uci-tree-sitter/v1 and does not cover facts extraction contract uci-tree-sitter-facts/v2.",
			},
		},
		{
			GOOS:                    "linux",
			GOARCH:                  "amd64",
			SupportStatus:           "not_claimed",
			EvidenceState:           "not_reverified",
			BuildCommand:            "CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build ./tools/uci-parser",
			CGOToolchainRequirement: "a C compiler capable of GOOS/GOARCH configured as CC",
			SourceProbe: parserSourceProbeEvidence{
				Code:                     "not_reverified_after_facts_contract_revision",
				BuildState:               "not_run",
				SmokeState:               "not_run",
				ObservedProtocolRevision: "",
				ObservedBundleDigest:     "",
				ObservedCoverage:         "",
				BlockerDetail:            "The prior source probe covered uci-tree-sitter/v1 and does not cover facts extraction contract uci-tree-sitter-facts/v2.",
			},
		},
		{
			GOOS:                    "darwin",
			GOARCH:                  "amd64",
			SupportStatus:           "not_claimed",
			EvidenceState:           "blocked",
			BuildCommand:            "CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 go build ./tools/uci-parser",
			CGOToolchainRequirement: "a C compiler capable of GOOS/GOARCH configured as CC",
			SourceProbe: parserSourceProbeEvidence{
				Code:                     "blocked_missing_cross_c_toolchain",
				BuildState:               "blocked",
				SmokeState:               "not_run",
				ObservedProtocolRevision: "",
				ObservedBundleDigest:     "",
				ObservedCoverage:         "",
				BlockerDetail:            "host clang target rejects -arch",
			},
		},
		{
			GOOS:                    "darwin",
			GOARCH:                  "arm64",
			SupportStatus:           "not_claimed",
			EvidenceState:           "blocked",
			BuildCommand:            "CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build ./tools/uci-parser",
			CGOToolchainRequirement: "a C compiler capable of GOOS/GOARCH configured as CC",
			SourceProbe: parserSourceProbeEvidence{
				Code:                     "blocked_missing_cross_c_toolchain",
				BuildState:               "blocked",
				SmokeState:               "not_run",
				ObservedProtocolRevision: "",
				ObservedBundleDigest:     "",
				ObservedCoverage:         "",
				BlockerDetail:            "host clang target rejects -arch",
			},
		},
	}
)

func TestParserManifestProvenance(t *testing.T) {
	repoRoot := parserRepositoryRoot(t)
	manifestPath := filepath.Join(repoRoot, "tools", "uci-parser", "manifest.json")
	manifestBytes := readParserFile(t, manifestPath)
	manifest := parseParserManifest(t, manifestBytes)

	assertManifestIdentity(t, manifest)
	assertNoFloatingProvenance(t, manifestBytes, manifest.Dependencies)
	assertToolchainProvenance(t, manifest.Toolchain)
	assertDependencyProvenance(t, repoRoot, manifest.Dependencies, manifest.Toolchain.ModuleGoVersion)
	assertCompiledGrammars(t, manifest.CompiledGrammars)

	source := inspectParserSource(t, repoRoot)
	assertParserSourceIdentity(t, manifest, source)
	assertBundleDigestProvenance(t, manifest, source)
	assertTargetEvidence(t, manifest)
	assertParentProtocolIdentity(t, manifest)
}

func assertManifestIdentity(t *testing.T, manifest parserManifest) {
	t.Helper()

	if manifest.SchemaVersion != 4 {
		t.Fatalf("schema_version = %d, want 4", manifest.SchemaVersion)
	}
	if manifest.Component != "uci-parser" {
		t.Fatalf("component = %q, want uci-parser", manifest.Component)
	}
	if manifest.ParserProtocolRevision != "uci-tree-sitter/v2" || manifest.FactsExtractionContractRevision != "uci-tree-sitter-facts/v2" || manifest.BundleSchemaRevision != "uci-tree-sitter-bundle/v2" {
		t.Fatalf("unexpected parser identity: protocol=%q facts=%q bundle=%q", manifest.ParserProtocolRevision, manifest.FactsExtractionContractRevision, manifest.BundleSchemaRevision)
	}
}

func parseParserManifest(t *testing.T, data []byte) parserManifest {
	t.Helper()

	assertManifestShape(t, data)
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

func assertManifestShape(t *testing.T, data []byte) {
	t.Helper()

	root := strictJSONObject(t, data, "manifest",
		"schema_version",
		"component",
		"parser_protocol_revision",
		"facts_extraction_contract_revision",
		"bundle_schema_revision",
		"toolchain",
		"dependencies",
		"compiled_grammars",
		"bundle_digest",
		"targets",
	)
	toolchain := strictJSONObject(t, root["toolchain"], "toolchain", "module_go_version", "requires_cgo", "cgo_sources")
	_ = strictJSONArray(t, toolchain["cgo_sources"], "toolchain.cgo_sources")

	for index, rawDependency := range strictJSONArray(t, root["dependencies"], "dependencies") {
		label := "dependencies[" + strconv.Itoa(index) + "]"
		dependency := strictJSONObject(t, rawDependency, label, "module", "version", "checksums", "source", "license")
		strictJSONObject(t, dependency["checksums"], label+".checksums", "module", "go_mod")
		strictJSONObject(t, dependency["source"], label+".source", "repository_url", "source_url", "tag", "commit", "module_info_url")
		strictJSONObject(t, dependency["license"], label+".license", "spdx", "url", "sha256")
	}

	for index, rawGrammar := range strictJSONArray(t, root["compiled_grammars"], "compiled_grammars") {
		strictJSONObject(t, rawGrammar, "compiled_grammars["+strconv.Itoa(index)+"]", "language", "module", "go_binding_import", "binding_symbol")
	}

	digest := strictJSONObject(t, root["bundle_digest"], "bundle_digest", "algorithm", "prefix", "input_encoding", "static_inputs", "static_input_digest", "runtime_input_recipe")
	_ = strictJSONArray(t, digest["static_inputs"], "bundle_digest.static_inputs")
	_ = strictJSONArray(t, digest["runtime_input_recipe"], "bundle_digest.runtime_input_recipe")

	for index, rawTarget := range strictJSONArray(t, root["targets"], "targets") {
		label := "targets[" + strconv.Itoa(index) + "]"
		target := strictJSONObject(t, rawTarget, label, "goos", "goarch", "support_status", "evidence_state", "build_command", "cgo_toolchain_requirement", "source_probe")
		strictJSONObject(t, target["source_probe"], label+".source_probe", "code", "build_state", "smoke_state", "observed_protocol_revision", "observed_bundle_digest", "observed_coverage", "blocker_detail")
	}
}

func strictJSONObject(t *testing.T, raw []byte, label string, requiredKeys ...string) map[string]json.RawMessage {
	t.Helper()

	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode %s as object: %v", label, err)
	}
	if object == nil {
		t.Fatalf("%s must be an object", label)
	}
	allowed := make(map[string]struct{}, len(requiredKeys))
	for _, key := range requiredKeys {
		allowed[key] = struct{}{}
		if _, found := object[key]; !found {
			t.Fatalf("%s is missing required field %q", label, key)
		}
	}
	if len(object) != len(allowed) {
		t.Fatalf("%s has %d fields, want exactly %d", label, len(object), len(allowed))
	}
	for key := range object {
		if _, allowed := allowed[key]; !allowed {
			t.Fatalf("%s contains unknown field %q", label, key)
		}
	}
	return object
}

func strictJSONArray(t *testing.T, raw []byte, label string) []json.RawMessage {
	t.Helper()

	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		t.Fatalf("decode %s as array: %v", label, err)
	}
	if values == nil {
		t.Fatalf("%s must be an array", label)
	}
	return values
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
			t.Fatalf("%s version %q is not a canonical exact release version", dependency.Module, dependency.Version)
		}
		if dependency.Source.Tag != dependency.Version {
			t.Fatalf("%s source tag %q does not match version %q", dependency.Module, dependency.Source.Tag, dependency.Version)
		}
		if !exactCommit.MatchString(dependency.Source.Commit) {
			t.Fatalf("%s source commit %q is not a full immutable commit", dependency.Module, dependency.Source.Commit)
		}
		if !goChecksum.MatchString(dependency.Checksums.Module) || !goChecksum.MatchString(dependency.Checksums.GoMod) {
			t.Fatalf("%s has a noncanonical Go module checksum", dependency.Module)
		}
		if dependency.License.SPDX != "MIT" || !sha256Digest.MatchString(dependency.License.SHA256) {
			t.Fatalf("%s has noncanonical MIT license evidence %#v", dependency.Module, dependency.License)
		}
	}
}

func assertToolchainProvenance(t *testing.T, toolchain parserToolchain) {
	t.Helper()

	if toolchain.ModuleGoVersion != "1.26.6" {
		t.Fatalf("module_go_version = %q, want 1.26.6", toolchain.ModuleGoVersion)
	}
	if !toolchain.RequiresCGO {
		t.Fatal("parser manifest must declare the CGO requirement")
	}
	assertNoDuplicateStrings(t, "cgo_sources", toolchain.CGOSources)
	if !reflect.DeepEqual(toolchain.CGOSources, expectedCGOSources) {
		t.Fatalf("cgo_sources = %#v, want %#v", toolchain.CGOSources, expectedCGOSources)
	}
}

func assertDependencyProvenance(t *testing.T, repoRoot string, dependencies []parserDependency, manifestGoVersion string) {
	t.Helper()

	assertNoDuplicateDependencies(t, dependencies)
	if !reflect.DeepEqual(dependencies, expectedDependencies) {
		t.Fatalf("dependencies = %#v, want %#v", dependencies, expectedDependencies)
	}
	for _, dependency := range dependencies {
		assertCanonicalDependency(t, dependency)
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

	requirements := make(map[string]*modfile.Require, len(goMod.Require))
	for _, requirement := range goMod.Require {
		if _, exists := requirements[requirement.Mod.Path]; exists {
			t.Fatalf("go.mod repeats requirement %s", requirement.Mod.Path)
		}
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

func assertCanonicalDependency(t *testing.T, dependency parserDependency) {
	t.Helper()

	repositoryName, found := strings.CutPrefix(dependency.Module, "github.com/tree-sitter/")
	if !found || repositoryName == "" {
		t.Fatalf("bundled dependency %q is not a tree-sitter module", dependency.Module)
	}
	if got, want := dependency.Source.RepositoryURL, "https://github.com/tree-sitter/"+repositoryName; got != want {
		t.Fatalf("%s repository_url = %q, want %q", dependency.Module, got, want)
	}
	if got, want := dependency.Source.SourceURL, dependency.Source.RepositoryURL+"/tree/"+dependency.Source.Commit; got != want {
		t.Fatalf("%s source_url = %q, want %q", dependency.Module, got, want)
	}
	if got, want := dependency.Source.ModuleInfoURL, "https://proxy.golang.org/"+dependency.Module+"/@v/"+dependency.Version+".info"; got != want {
		t.Fatalf("%s module_info_url = %q, want %q", dependency.Module, got, want)
	}
	if got, want := dependency.License.URL, "https://raw.githubusercontent.com/tree-sitter/"+repositoryName+"/"+dependency.Source.Commit+"/LICENSE"; got != want {
		t.Fatalf("%s license URL = %q, want %q", dependency.Module, got, want)
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

	assertNoDuplicateGrammars(t, grammars)
	if !reflect.DeepEqual(grammars, expectedGrammars) {
		t.Fatalf("compiled_grammars = %#v, want %#v", grammars, expectedGrammars)
	}
	for _, grammar := range grammars {
		if !strings.HasPrefix(grammar.GoBindingImport, grammar.Module+"/") {
			t.Fatalf("grammar %q binding import %q is outside module %q", grammar.Language, grammar.GoBindingImport, grammar.Module)
		}
	}
}

func inspectParserSource(t *testing.T, repoRoot string) parserSourceContract {
	t.Helper()

	parserPath := filepath.Join(repoRoot, "tools", "uci-parser", "main.go")
	parserFile := parseGoSource(t, parserPath)
	imports := parserImportPaths(t, parserFile)
	languages := uciLanguageConstants(t, filepath.Join(repoRoot, "internal", "uci", "treesitter_worker.go"))
	grammarFunction := sourceFunction(t, parserFile, "parserLanguage")
	bundleFunction := sourceFunction(t, parserFile, "bundleDigest")

	return parserSourceContract{
		ImportPaths:    imports,
		Grammars:       parserGrammarBindings(t, grammarFunction, imports, languages),
		StaticInputs:   bundleStaticInputs(t, bundleFunction),
		BundleFunction: bundleFunction,
	}
}

func parseGoSource(t *testing.T, path string) *ast.File {
	t.Helper()

	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return file
}

func parserImportPaths(t *testing.T, source *ast.File) map[string]string {
	t.Helper()

	imports := make(map[string]string, len(source.Imports))
	for _, spec := range source.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatalf("unquote parser import %q: %v", spec.Path.Value, err)
		}
		alias := importAlias(spec, importPath)
		if previous, exists := imports[alias]; exists {
			t.Fatalf("parser import alias %q maps to both %q and %q", alias, previous, importPath)
		}
		imports[alias] = importPath
	}
	return imports
}

func importAlias(spec *ast.ImportSpec, importPath string) string {
	if spec.Name != nil {
		return spec.Name.Name
	}
	if slash := strings.LastIndex(importPath, "/"); slash >= 0 {
		importPath = importPath[slash+1:]
	}
	return strings.ReplaceAll(importPath, "-", "_")
}

func sourceFunction(t *testing.T, source *ast.File, name string) *ast.FuncDecl {
	t.Helper()

	for _, declaration := range source.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == name {
			return function
		}
	}
	t.Fatalf("parser source does not define %s", name)
	return nil
}

func uciLanguageConstants(t *testing.T, path string) map[string]string {
	t.Helper()

	source := parseGoSource(t, path)
	languages := make(map[string]string)
	for _, declaration := range source.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, declarationSpec := range group.Specs {
			specification, ok := declarationSpec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range specification.Names {
				if !strings.HasPrefix(name.Name, "TreeSitterLanguage") {
					continue
				}
				if index >= len(specification.Values) {
					t.Fatalf("UCI language constant %s has no explicit string value", name.Name)
				}
				literal, ok := specification.Values[index].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					t.Fatalf("UCI language constant %s is not a string literal", name.Name)
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					t.Fatalf("unquote UCI language constant %s: %v", name.Name, err)
				}
				if previous, exists := languages[name.Name]; exists {
					t.Fatalf("UCI language constant %s repeats value %q", name.Name, previous)
				}
				languages[name.Name] = value
			}
		}
	}
	if len(languages) == 0 {
		t.Fatal("UCI source declares no TreeSitterLanguage constants")
	}
	return languages
}

func parserGrammarBindings(t *testing.T, function *ast.FuncDecl, imports map[string]string, languages map[string]string) map[string]parserSourceGrammar {
	t.Helper()

	var languageSwitch *ast.SwitchStmt
	for _, statement := range function.Body.List {
		candidate, ok := statement.(*ast.SwitchStmt)
		if !ok {
			continue
		}
		if languageSwitch != nil {
			t.Fatal("parserLanguage contains multiple switches")
		}
		languageSwitch = candidate
	}
	if languageSwitch == nil {
		t.Fatal("parserLanguage contains no language switch")
	}

	bindings := make(map[string]parserSourceGrammar)
	for _, statement := range languageSwitch.Body.List {
		clause, ok := statement.(*ast.CaseClause)
		if !ok {
			t.Fatal("parserLanguage switch contains a non-case clause")
		}
		if len(clause.List) == 0 {
			continue
		}
		if len(clause.List) != 1 {
			t.Fatal("parserLanguage switch case must name exactly one UCI language constant")
		}
		constant := uciLanguageSelector(t, clause.List[0], imports)
		language, found := languages[constant]
		if !found {
			t.Fatalf("parserLanguage references unknown UCI language constant %q", constant)
		}
		binding := grammarBindingCall(t, clause.Body, imports)
		if _, exists := bindings[language]; exists {
			t.Fatalf("parserLanguage repeats grammar binding for %q", language)
		}
		bindings[language] = binding
	}
	if len(bindings) == 0 {
		t.Fatal("parserLanguage declares no grammar bindings")
	}
	return bindings
}

func uciLanguageSelector(t *testing.T, expression ast.Expr, imports map[string]string) string {
	t.Helper()

	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		t.Fatalf("parserLanguage case %#v is not a UCI language selector", expression)
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok || imports[packageName.Name] != "github.com/thebtf/engram/internal/uci" {
		t.Fatalf("parserLanguage case %#v is not from the UCI package", expression)
	}
	return selector.Sel.Name
}

func grammarBindingCall(t *testing.T, statements []ast.Stmt, imports map[string]string) parserSourceGrammar {
	t.Helper()

	var bindings []parserSourceGrammar
	ast.Inspect(&ast.BlockStmt{List: statements}, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		packageName, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		importPath := imports[packageName.Name]
		if !strings.HasPrefix(importPath, "github.com/tree-sitter/") || importPath == "github.com/tree-sitter/go-tree-sitter" {
			return true
		}
		bindings = append(bindings, parserSourceGrammar{
			GoBindingImport: importPath,
			BindingSymbol:   selector.Sel.Name,
		})
		return true
	})
	if len(bindings) != 1 {
		t.Fatalf("parserLanguage case has %d grammar binding calls, want 1", len(bindings))
	}
	return bindings[0]
}

func bundleStaticInputs(t *testing.T, function *ast.FuncDecl) []string {
	t.Helper()

	for _, statement := range function.Body.List {
		assignment, ok := statement.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
			continue
		}
		name, ok := assignment.Lhs[0].(*ast.Ident)
		if !ok || name.Name != "parts" {
			continue
		}
		literal, ok := assignment.Rhs[0].(*ast.CompositeLit)
		if !ok {
			t.Fatal("bundleDigest initializes parts with a non-literal value")
		}
		var inputs []string
		dynamicInputSeen := false
		for _, element := range literal.Elts {
			input, static := bundleStaticInput(t, element)
			if !static {
				dynamicInputSeen = true
				continue
			}
			if dynamicInputSeen {
				t.Fatal("bundleDigest places a static input after a runtime input")
			}
			inputs = append(inputs, input)
		}
		if len(inputs) == 0 {
			t.Fatal("bundleDigest has no static provenance inputs")
		}
		return inputs
	}
	t.Fatal("bundleDigest does not initialize parts")
	return nil
}

func bundleStaticInput(t *testing.T, expression ast.Expr) (string, bool) {
	t.Helper()
	switch expression := expression.(type) {
	case *ast.BasicLit:
		if expression.Kind != token.STRING {
			return "", false
		}
		input, err := strconv.Unquote(expression.Value)
		if err != nil {
			t.Fatalf("unquote bundle static input %q: %v", expression.Value, err)
		}
		return input, true
	case *ast.SelectorExpr:
		selector, found := qualifiedSelector(expression)
		if !found {
			return "", false
		}
		switch selector {
		case "uci.TreeSitterWorkerProtocolVersion":
			return ucidomain.TreeSitterWorkerProtocolVersion, true
		case "uci.TreeSitterFactsExtractionContractRevision":
			return ucidomain.TreeSitterFactsExtractionContractRevision, true
		default:
			return "", false
		}
	default:
		return "", false
	}
}

func assertParserSourceIdentity(t *testing.T, manifest parserManifest, source parserSourceContract) {
	t.Helper()

	assertParserTreeSitterImports(t, source.ImportPaths, manifest.Dependencies)
	if len(source.Grammars) != len(manifest.CompiledGrammars) {
		t.Fatalf("parser source has %d grammar bindings, manifest has %d", len(source.Grammars), len(manifest.CompiledGrammars))
	}
	for _, grammar := range manifest.CompiledGrammars {
		binding, found := source.Grammars[grammar.Language]
		if !found {
			t.Fatalf("parser source does not bind manifest grammar %q", grammar.Language)
		}
		if binding.GoBindingImport != grammar.GoBindingImport || binding.BindingSymbol != grammar.BindingSymbol {
			t.Fatalf("parser source grammar %q = %#v, want binding import/symbol %q/%q", grammar.Language, binding, grammar.GoBindingImport, grammar.BindingSymbol)
		}
		if got := dependencyModuleForImport(t, binding.GoBindingImport, manifest.Dependencies); got != grammar.Module {
			t.Fatalf("parser source grammar %q resolves to module %q, want %q", grammar.Language, got, grammar.Module)
		}
	}
}

func assertParserTreeSitterImports(t *testing.T, imports map[string]string, dependencies []parserDependency) {
	t.Helper()

	actualModules := make([]string, 0, len(dependencies))
	for _, importPath := range imports {
		if !strings.HasPrefix(importPath, "github.com/tree-sitter/") {
			continue
		}
		actualModules = append(actualModules, dependencyModuleForImport(t, importPath, dependencies))
	}
	sort.Strings(actualModules)
	assertNoDuplicateStrings(t, "tree-sitter parser imports", actualModules)

	expectedModules := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		expectedModules = append(expectedModules, dependency.Module)
	}
	sort.Strings(expectedModules)
	if !reflect.DeepEqual(actualModules, expectedModules) {
		t.Fatalf("tree-sitter parser import modules = %#v, want %#v", actualModules, expectedModules)
	}
}

func dependencyModuleForImport(t *testing.T, importPath string, dependencies []parserDependency) string {
	t.Helper()

	var matches []string
	for _, dependency := range dependencies {
		if importPath == dependency.Module || strings.HasPrefix(importPath, dependency.Module+"/") {
			matches = append(matches, dependency.Module)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("parser import %q maps to %d manifest modules, want 1", importPath, len(matches))
	}
	return matches[0]
}

func assertBundleDigestProvenance(t *testing.T, manifest parserManifest, source parserSourceContract) {
	t.Helper()

	digest := manifest.BundleDigest
	if digest.Algorithm != "sha256" || digest.Prefix != "sha256:" {
		t.Fatalf("bundle digest algorithm/prefix = %q/%q, want sha256/sha256:", digest.Algorithm, digest.Prefix)
	}
	if digest.InputEncoding != "sort lexical; for each UTF-8 input, append its uint32 big-endian byte length then its bytes" {
		t.Fatalf("unexpected bundle input encoding %q", digest.InputEncoding)
	}
	assertNoDuplicateStrings(t, "bundle static inputs", digest.StaticInputs)
	if !reflect.DeepEqual(digest.StaticInputs, expectedStaticInputs) {
		t.Fatalf("bundle static inputs = %#v, want %#v", digest.StaticInputs, expectedStaticInputs)
	}
	if !reflect.DeepEqual(digest.StaticInputs, source.StaticInputs) {
		t.Fatalf("bundle source static inputs = %#v, want manifest %#v", source.StaticInputs, digest.StaticInputs)
	}
	if len(digest.StaticInputs) < 3 || digest.StaticInputs[0] != manifest.BundleSchemaRevision || digest.StaticInputs[1] != manifest.ParserProtocolRevision || digest.StaticInputs[2] != manifest.FactsExtractionContractRevision {
		t.Fatalf("bundle static inputs must bind bundle, protocol, and facts revisions: %#v", digest.StaticInputs)
	}
	if got, want := digest.RuntimeInputRecipe, []string{
		"go=runtime.Version()",
		"target=runtime.GOOS/runtime.GOARCH",
		"build-go=debug.ReadBuildInfo().GoVersion when nonempty",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("bundle runtime input recipe = %#v, want %#v", got, want)
	}
	assertBundleRuntimeInputs(t, source.BundleFunction)
	if got, want := staticBundleInputDigest(digest.StaticInputs), digest.StaticInputDigest; got != want {
		t.Fatalf("static bundle input digest = %q, want %q", got, want)
	}
	if !sha256Digest.MatchString(digest.StaticInputDigest) {
		t.Fatalf("static bundle input digest %q is noncanonical", digest.StaticInputDigest)
	}
	if got, want := bundleDigest(), runtimeBundleDigest(digest.StaticInputs); got != want {
		t.Fatalf("runtime bundle digest = %q, want manifest-derived %q", got, want)
	}
}

func assertBundleRuntimeInputs(t *testing.T, function *ast.FuncDecl) {
	t.Helper()

	requiredCalls := map[string]bool{
		"runtime.Version":     false,
		"debug.ReadBuildInfo": false,
	}
	requiredSelectors := map[string]bool{
		"runtime.GOOS":   false,
		"runtime.GOARCH": false,
	}
	requiredLiterals := map[string]bool{
		"go=":       false,
		"target=":   false,
		"build-go=": false,
	}
	ast.Inspect(function.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			if name, found := qualifiedSelector(node.Fun); found {
				if _, required := requiredCalls[name]; required {
					requiredCalls[name] = true
				}
			}
		case *ast.SelectorExpr:
			if name, found := qualifiedSelector(node); found {
				if _, required := requiredSelectors[name]; required {
					requiredSelectors[name] = true
				}
			}
		case *ast.BasicLit:
			if node.Kind != token.STRING {
				return true
			}
			value, err := strconv.Unquote(node.Value)
			if err != nil {
				t.Fatalf("unquote bundle source literal %q: %v", node.Value, err)
			}
			if _, required := requiredLiterals[value]; required {
				requiredLiterals[value] = true
			}
		}
		return true
	})
	for name, found := range requiredCalls {
		if !found {
			t.Fatalf("bundleDigest no longer reads %s", name)
		}
	}
	for name, found := range requiredSelectors {
		if !found {
			t.Fatalf("bundleDigest no longer includes %s", name)
		}
	}
	for literal, found := range requiredLiterals {
		if !found {
			t.Fatalf("bundleDigest no longer includes %q", literal)
		}
	}
}

func qualifiedSelector(expression ast.Expr) (string, bool) {
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	packageName, ok := selector.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	return packageName.Name + "." + selector.Sel.Name, true
}

func staticBundleInputDigest(inputs []string) string {
	return digestInputs(inputs)
}

func runtimeBundleDigest(staticInputs []string) ucidomain.IndexDigest {
	parts := append([]string(nil), staticInputs...)
	parts = append(parts, "go="+runtime.Version(), "target="+runtime.GOOS+"/"+runtime.GOARCH)
	if build, ok := debug.ReadBuildInfo(); ok && build.GoVersion != "" {
		parts = append(parts, "build-go="+build.GoVersion)
	}
	return ucidomain.IndexDigest(digestInputs(parts))
}

func digestInputs(inputs []string) string {
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

func assertTargetEvidence(t *testing.T, manifest parserManifest) {
	t.Helper()

	targets := manifest.Targets
	assertNoDuplicateTargets(t, targets)
	if !reflect.DeepEqual(targets, expectedTargets) {
		t.Fatalf("target matrix = %#v, want %#v", targets, expectedTargets)
	}
	for _, target := range targets {
		if strings.ContainsAny(target.BuildCommand, "<>") || strings.Contains(strings.ToLower(target.BuildCommand), "latest") {
			t.Fatalf("target %s/%s build command is not a deterministic pinned command: %q", target.GOOS, target.GOARCH, target.BuildCommand)
		}
		if got, want := target.BuildCommand, targetBuildCommand(target.GOOS, target.GOARCH); got != want {
			t.Fatalf("target %s/%s build command = %q, want %q", target.GOOS, target.GOARCH, got, want)
		}
		if target.CGOToolchainRequirement != "a C compiler capable of GOOS/GOARCH configured as CC" {
			t.Fatalf("target %s/%s CGO toolchain requirement = %q", target.GOOS, target.GOARCH, target.CGOToolchainRequirement)
		}
		assertSourceProbeEvidence(t, manifest, target)
	}
}

func assertSourceProbeEvidence(t *testing.T, manifest parserManifest, target parserTargetEvidence) {
	t.Helper()

	probe := target.SourceProbe
	switch probe.Code {
	case "source_build_and_javascript_smoke_passed":
		if (target.GOOS != "windows" && target.GOOS != "linux") || target.GOARCH != "amd64" {
			t.Fatalf("source probe pass is only recorded for windows/amd64 or linux/amd64, got %s/%s", target.GOOS, target.GOARCH)
		}
		if target.SupportStatus != "source_probe_passed" || target.EvidenceState != "passed" {
			t.Fatalf("source probe pass status for %s/%s = %q/%q, want source_probe_passed/passed", target.GOOS, target.GOARCH, target.SupportStatus, target.EvidenceState)
		}
		if probe.BuildState != "passed" || probe.SmokeState != "passed" {
			t.Fatalf("source probe pass build/smoke state for %s/%s = %q/%q, want passed/passed", target.GOOS, target.GOARCH, probe.BuildState, probe.SmokeState)
		}
		if probe.ObservedProtocolRevision != manifest.ParserProtocolRevision {
			t.Fatalf("source probe pass protocol for %s/%s = %q, want %q", target.GOOS, target.GOARCH, probe.ObservedProtocolRevision, manifest.ParserProtocolRevision)
		}
		if !sha256Digest.MatchString(probe.ObservedBundleDigest) {
			t.Fatalf("source probe pass bundle digest for %s/%s %q is noncanonical", target.GOOS, target.GOARCH, probe.ObservedBundleDigest)
		}
		if probe.ObservedCoverage != "complete" {
			t.Fatalf("source probe pass coverage for %s/%s = %q, want complete", target.GOOS, target.GOARCH, probe.ObservedCoverage)
		}
		if probe.BlockerDetail != "" {
			t.Fatalf("source probe pass for %s/%s records an unexpected blocker %q", target.GOOS, target.GOARCH, probe.BlockerDetail)
		}
	case "not_reverified_after_facts_contract_revision":
		if (target.GOOS != "windows" && target.GOOS != "linux") || target.GOARCH != "amd64" {
			t.Fatalf("unreverified source probe is only recorded for windows/amd64 or linux/amd64, got %s/%s", target.GOOS, target.GOARCH)
		}
		if target.SupportStatus != "not_claimed" || target.EvidenceState != "not_reverified" {
			t.Fatalf("unreverified source probe status for %s/%s = %q/%q, want not_claimed/not_reverified", target.GOOS, target.GOARCH, target.SupportStatus, target.EvidenceState)
		}
		if probe.BuildState != "not_run" || probe.SmokeState != "not_run" {
			t.Fatalf("unreverified source probe build/smoke state for %s/%s = %q/%q, want not_run/not_run", target.GOOS, target.GOARCH, probe.BuildState, probe.SmokeState)
		}
		if probe.ObservedProtocolRevision != "" || probe.ObservedBundleDigest != "" || probe.ObservedCoverage != "" {
			t.Fatalf("unreverified source probe must not claim current smoke output %#v", probe)
		}
		if !strings.Contains(probe.BlockerDetail, manifest.FactsExtractionContractRevision) || !strings.Contains(probe.BlockerDetail, "uci-tree-sitter/v1") {
			t.Fatalf("unreverified source probe lacks precise prior/current contract provenance %q", probe.BlockerDetail)
		}
	case "blocked_missing_cross_c_toolchain":
		if target.GOOS != "linux" && target.GOOS != "darwin" {
			t.Fatalf("cross-C-toolchain blocker is not valid for %s/%s", target.GOOS, target.GOARCH)
		}
		if target.SupportStatus != "not_claimed" || target.EvidenceState != "blocked" {
			t.Fatalf("blocked target %s/%s status = %q/%q, want not_claimed/blocked", target.GOOS, target.GOARCH, target.SupportStatus, target.EvidenceState)
		}
		if probe.BuildState != "blocked" || probe.SmokeState != "not_run" {
			t.Fatalf("blocked target %s/%s build/smoke state = %q/%q, want blocked/not_run", target.GOOS, target.GOARCH, probe.BuildState, probe.SmokeState)
		}
		if probe.ObservedProtocolRevision != "" || probe.ObservedBundleDigest != "" || probe.ObservedCoverage != "" {
			t.Fatalf("blocked target %s/%s must not claim smoke output %#v", target.GOOS, target.GOARCH, probe)
		}
		if strings.TrimSpace(probe.BlockerDetail) == "" {
			t.Fatalf("blocked target %s/%s is missing its cross-C-toolchain detail", target.GOOS, target.GOARCH)
		}
	}
}

func targetBuildCommand(goos, goarch string) string {
	return "CGO_ENABLED=1 GOOS=" + goos + " GOARCH=" + goarch + " go build ./tools/uci-parser"
}

func assertParentProtocolIdentity(t *testing.T, manifest parserManifest) {
	t.Helper()
	if got := ucidomain.TreeSitterWorkerProtocolVersion; got != manifest.ParserProtocolRevision {
		t.Fatalf("parent protocol revision = %q, want %q", got, manifest.ParserProtocolRevision)
	}
	if got := ucidomain.TreeSitterFactsExtractionContractRevision; got != manifest.FactsExtractionContractRevision {
		t.Fatalf("parent facts extraction contract revision = %q, want %q", got, manifest.FactsExtractionContractRevision)
	}
}

func assertNoDuplicateDependencies(t *testing.T, dependencies []parserDependency) {
	t.Helper()

	keys := make([]string, 0, len(dependencies))
	for _, dependency := range dependencies {
		keys = append(keys, dependency.Module)
	}
	assertNoDuplicateStrings(t, "dependency modules", keys)
}

func assertNoDuplicateGrammars(t *testing.T, grammars []compiledGrammar) {
	t.Helper()

	languages := make([]string, 0, len(grammars))
	bindings := make([]string, 0, len(grammars))
	for _, grammar := range grammars {
		languages = append(languages, grammar.Language)
		bindings = append(bindings, grammar.GoBindingImport+"\x00"+grammar.BindingSymbol)
	}
	assertNoDuplicateStrings(t, "grammar languages", languages)
	assertNoDuplicateStrings(t, "grammar bindings", bindings)
}

func assertNoDuplicateTargets(t *testing.T, targets []parserTargetEvidence) {
	t.Helper()

	keys := make([]string, 0, len(targets))
	for _, target := range targets {
		keys = append(keys, target.GOOS+"/"+target.GOARCH)
	}
	assertNoDuplicateStrings(t, "target pairs", keys)
}

func assertNoDuplicateStrings(t *testing.T, label string, values []string) {
	t.Helper()

	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			t.Fatalf("%s contains an empty entry", label)
		}
		if _, exists := seen[value]; exists {
			t.Fatalf("%s contains duplicate entry %q", label, value)
		}
		seen[value] = struct{}{}
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
