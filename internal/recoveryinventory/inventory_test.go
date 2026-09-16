package recoveryinventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestScansAreDeterministicAndRedacted(t *testing.T) {
	root := inventoryFixture(t)
	first, err := ScanAll(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ScanAll(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("scan output changed between identical source reads")
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"hunter2", "https://private.example", "private-default"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("inventory exposed source content %q: %s", forbidden, encoded)
		}
	}
}

func TestSensitiveEnvironmentNames(t *testing.T) {
	for _, want := range []struct {
		name, inventoryName, classification string
	}{
		{"ENGRAM_AUTH_ADMIN_TOKEN", "redacted", "credential-reader"},
		{"ENGRAM_DATABASE_PASSWORD", "redacted", "credential-reader"},
		{"ENGRAM_SERVICE_SECRET", "redacted", "credential-reader"},
		{"ENGRAM_API_KEY", "redacted", "credential-reader"},
		{"ENGRAM_CONTEXT_MAX_TOKENS", "ENGRAM_CONTEXT_MAX_TOKENS", "configuration-reader"},
	} {
		if got := redactedName(want.name); got != want.inventoryName {
			t.Errorf("redactedName(%q) = %q, want %q", want.name, got, want.inventoryName)
		}
		if got := environmentClassification(want.name); got != want.classification {
			t.Errorf("environmentClassification(%q) = %q, want %q", want.name, got, want.classification)
		}
	}
}

func TestPesterPowerShellTestSourcesAreExcluded(t *testing.T) {
	root := t.TempDir()
	path := "scripts/safety-gate.Tests.ps1"
	writeFixture(t, root, path, `$env:ENGRAM_PESTER_TEST_ONLY`)
	if !isTestSource(path) {
		t.Fatalf("Pester test source %q was not recognized", path)
	}
	flags, err := ScanFlags(root)
	if err != nil {
		t.Fatal(err)
	}
	if hasPathRecord(flags, "environment-reader", path) {
		t.Fatalf("Pester test source leaked into runtime flag inventory: %#v", flags.Records)
	}
}

func TestTestSourceRecognizesCaseInsensitiveSegments(t *testing.T) {
	for _, path := range []string{
		"Tests/reader.go",
		"ui/__TESTS__/reader.js",
		"internal/handlers/loom/testdata/clihelper/main.go",
		"tools/quality/fixtures/recovery.go",
		"internal/moduletest/harness.go",
	} {
		if !isTestSource(path) {
			t.Fatalf("test source %q was not recognized", path)
		}
	}
}

func TestFixtureArtifactsAreExcludedFromRuntimeInventories(t *testing.T) {
	root := t.TempDir()
	productionPath := "tools/quality/harness/runtime.go"
	writeFixture(t, root, productionPath, `package harness
import "os"
func Runtime() { _ = os.Getenv("ENGRAM_RUNTIME") }
type RuntimeProjectData struct { ProjectID string }
`)
	fixturePaths := []string{
		"internal/handlers/loom/testdata/clihelper/main.go",
		"tests/fixtures/recovery/fixture/main.go",
		"tools/quality/fixtures/recovery.go",
		"internal/moduletest/harness.go",
	}
	for _, fixturePath := range fixturePaths {
		writeFixture(t, root, fixturePath, `package fixture
import "os"
func Runtime() { _ = os.Getenv("ENGRAM_FIXTURE_ONLY") }
type FixtureProjectData struct { ProjectID string }
`)
	}

	source, err := ScanSource(root)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := ScanFlags(root)
	if err != nil {
		t.Fatal(err)
	}
	projectData, err := ScanProjectData(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range []Report{source, flags, projectData} {
		for _, fixturePath := range fixturePaths {
			for _, record := range report.Records {
				if record.Path == fixturePath {
					t.Fatalf("fixture artifact %q leaked into %s inventory: %#v", fixturePath, report.Inventory, report.Records)
				}
			}
		}
	}
	if !hasPathRecord(source, "go-package", productionPath) || !hasPathRecord(flags, "environment-reader", productionPath) || !hasPathRecord(projectData, "project-bearing-field", productionPath) {
		t.Fatalf("production harness source missing from runtime inventories: source=%#v flags=%#v project_data=%#v", source.Records, flags.Records, projectData.Records)
	}
}

func TestScanAllSkipsSerenaMetadata(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, ".serena/project.local.yml", "project: local-agent-metadata\n")
	writeFixture(t, root, "docs/current.md", "# current claim\n")

	reports, err := ScanAll(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, report := range reports {
		for _, record := range report.Records {
			if strings.HasPrefix(record.Path, ".serena/") {
				t.Fatalf("Serena metadata leaked into %s inventory: %#v", report.Inventory, report.Records)
			}
		}
	}
}

func TestFlagScanRespectsMultilineLexicalTruth(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "scripts/help.ps1", `$outside = $env:ENGRAM_PS_OUTSIDE
$literal = @'
$env:ENGRAM_PS_LITERAL
'@
$interpolated = @"
$env:ENGRAM_PS_HERE_INTERPOLATED
"@`)
	writeFixture(t, root, "scripts/help.sh", `printf '%s\n' "$ENGRAM_SHELL_OUTSIDE"
cat <<'LITERAL'
${CLAUDE_PROJECT}
LITERAL
cat <<INTERPOLATED
${ENGRAM_SHELL_HEREDOC}
INTERPOLATED`)
	writeFixture(t, root, "plugin/engram/hooks/help.js", `const outside = process.env.ENGRAM_JS_OUTSIDE
/**
 * process.env.ENGRAM_JS_JSDOC
 * process.env.ENGRAM_JS_DOCUMENTED
 */
const after = process.env.ENGRAM_JS_AFTER
const dynamic = process.env[dynamicName]`)
	writeFixture(t, root, "plugin/engram/hooks/template.js", "const documented = `\n"+
		"escaped backtick: \\`\n"+
		"process.env.ENGRAM_LITERAL_ONLY\n"+
		"`\n"+
		"const interpolated = `\n"+
		"${value}\n"+
		"`\n"+
		"const afterTemplate = process.env.ENGRAM_JS_TEMPLATE_AFTER\n")
	writeFixture(t, root, "plugin/engram/hooks/continued.js", `const single = 'literal \
process.env.ENGRAM_JS_SINGLE_LITERAL'
const double = "literal \
process.env.ENGRAM_JS_DOUBLE_LITERAL"
const actual = process.env.ENGRAM_JS_CONTINUED_AFTER`)
	writeFixture(t, root, "scripts/help.py", `outside = os.getenv("ENGRAM_PY_OUTSIDE")
bracket = os.environ["ENGRAM_PY_BRACKET"]
# os.getenv("ENGRAM_PY_COMMENT")
documentation = """
os.getenv("ENGRAM_PY_DOCSTRING")
"""
literal = "os.getenv('ENGRAM_PY_STRING')"
dynamic = os.getenv(runtime_name)`)
	flags, err := ScanFlags(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		path, name, parser string
	}{
		{"scripts/help.ps1", "ENGRAM_PS_OUTSIDE", "powershell-string"},
		{"scripts/help.ps1", "ENGRAM_PS_HERE_INTERPOLATED", "powershell-string"},
		{"scripts/help.sh", "ENGRAM_SHELL_OUTSIDE", "shell-parameter-expansion"},
		{"scripts/help.sh", "ENGRAM_SHELL_HEREDOC", "shell-parameter-expansion"},
		{"plugin/engram/hooks/help.js", "ENGRAM_JS_OUTSIDE", "source-string"},
		{"plugin/engram/hooks/help.js", "ENGRAM_JS_AFTER", "source-string"},
		{"plugin/engram/hooks/template.js", "ENGRAM_JS_TEMPLATE_AFTER", "source-string"},
		{"plugin/engram/hooks/continued.js", "ENGRAM_JS_CONTINUED_AFTER", "source-string"},
		{"scripts/help.py", "ENGRAM_PY_OUTSIDE", "python-string"},
		{"scripts/help.py", "ENGRAM_PY_BRACKET", "python-string"},
	} {
		if !hasReader(flags, want.path, want.name, want.parser, "empty-unset", "configuration-reader") {
			t.Fatalf("missing genuine environment reader %#v: %#v", want, flags.Records)
		}
	}
	for _, forbidden := range []string{"ENGRAM_PS_LITERAL", "CLAUDE_PROJECT", "ENGRAM_JS_JSDOC", "ENGRAM_JS_DOCUMENTED", "ENGRAM_LITERAL_ONLY", "ENGRAM_JS_SINGLE_LITERAL", "ENGRAM_JS_DOUBLE_LITERAL", "ENGRAM_PY_COMMENT", "ENGRAM_PY_DOCSTRING", "ENGRAM_PY_STRING"} {
		if hasRecord(flags, "environment-reader", forbidden, "") {
			t.Fatalf("literal multiline body produced an environment reader %q: %#v", forbidden, flags.Records)
		}
	}
	if !hasReader(flags, "plugin/engram/hooks/help.js", "", "source-uncertain", "source-unspecified", "source-uncertain") {
		t.Fatalf("dynamic JavaScript reader must remain source-uncertain: %#v", flags.Records)
	}
	if !hasReader(flags, "plugin/engram/hooks/template.js", "", "source-uncertain", "source-unspecified", "source-uncertain") {
		t.Fatalf("template interpolation must remain source-uncertain: %#v", flags.Records)
	}
	if !hasReader(flags, "scripts/help.py", "", "source-uncertain", "source-unspecified", "source-uncertain") {
		t.Fatalf("dynamic Python reader must remain source-uncertain: %#v", flags.Records)
	}
}

func TestFlagScanRecordsPowerShellEnvironmentAPIReads(t *testing.T) {
	root := t.TempDir()
	path := "scripts/environment-api.ps1"
	writeFixture(t, root, path, `# [Environment]::GetEnvironmentVariable("ENGRAM_PS_COMMENT")
$literal = [Environment]::GetEnvironmentVariable("ENGRAM_PS_API_SETTING")
$secret = [System.Environment]::GetEnvironmentVariable('ENGRAM_API_TOKEN')
$dynamic = [Environment]::GetEnvironmentVariable($name)
$all = [Environment]::GetEnvironmentVariables()
$text = '[Environment]::GetEnvironmentVariable("ENGRAM_PS_STRING")'`)
	flags, err := ScanFlags(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		name, classification string
	}{
		{"ENGRAM_PS_API_SETTING", "configuration-reader"},
		{"redacted", "credential-reader"},
	} {
		if !hasReader(flags, path, want.name, "powershell-environment-api", "empty-unset", want.classification) {
			t.Fatalf("missing literal PowerShell Environment API reader %#v: %#v", want, flags.Records)
		}
	}
	uncertain := 0
	for _, record := range flags.Records {
		if record.Kind == "environment-reader" && record.Path == path && record.Parser == "source-uncertain" && record.Default == "source-unspecified" && record.Classification == "source-uncertain" {
			uncertain++
		}
	}
	if uncertain != 2 {
		t.Fatalf("dynamic PowerShell Environment APIs reported %d uncertain readers, want 2: %#v", uncertain, flags.Records)
	}
	for _, forbidden := range []string{"ENGRAM_PS_COMMENT", "ENGRAM_PS_STRING"} {
		if hasRecord(flags, "environment-reader", forbidden, "") {
			t.Fatalf("comment or string produced an Environment API reader %q: %#v", forbidden, flags.Records)
		}
	}
}

func TestFlagScanCoversCurrentRuntimeReaderSyntax(t *testing.T) {
	root := inventoryFixture(t)
	flags, err := ScanFlags(root)
	if err != nil {
		t.Fatal(err)
	}
	assertRuntimeFlagReaders(t, flags)
	assertNoTestOrLocalFlagReaders(t, flags)
	assertNoLiteralFlagReaders(t, flags)
	assertDynamicFlagReaders(t, flags)
	assertNoFlagTestPaths(t, flags)
	assertRuntimeSourceFiles(t, root)
}

type runtimeFlagReader struct {
	path, name, parser, defaultKind, classification string
}

func assertRuntimeFlagReaders(t *testing.T, flags Report) {
	t.Helper()
	for _, want := range []runtimeFlagReader{
		{"internal/worker/routes.go", "ENGRAM_FEATURE", "exact-lowercase-true", "false-unless-exact-true", "feature-flag-reader"},
		{"internal/worker/routes.go", "redacted", "source-uncertain", "source-unspecified", "credential-reader"},
		{"internal/worker/routes.go", "ENGRAM_DB_PATH", "source-uncertain", "source-unspecified", "configuration-reader"},
		{"internal/worker/routes.go", "DATABASE_DSN", "source-uncertain", "source-unspecified", "configuration-reader"},
		{"internal/worker/routes.go", "ENGRAM_AUTH_ENABLED", "exact-lowercase-true", "false-unless-exact-true", "configuration-reader"},
		{"internal/config/config.go", "ENGRAM_CONTEXT_MAX_TOKENS", "integer-parser", "source-parser-default", "configuration-reader"},
		{"internal/config/config.go", "ENGRAM_INJECT_UNIFIED", "parse-bool", "source-parser-default", "configuration-reader"},
		{"internal/config/config.go", "ENGRAM_AUTHENTIK_ENABLED", "exact-true-or-one", "false-unless-true-or-one", "configuration-reader"},
		{"internal/config/config.go", "ENGRAM_FOLLOWING_PARSER", "integer-parser", "source-parser-default", "configuration-reader"},
		{"plugin/engram/hooks/runtime.js", "ENGRAM_JS_TIMEOUT", "integer-parser", "source-default-expression", "configuration-reader"},
		{"plugin/engram/hooks/dispatcher.cjs", "ENGRAM_CJS_FEATURE", "exact-lowercase-true", "empty-unset", "feature-flag-reader"},
		{"apps/operator-console/runtime.ts", "ENGRAM_TS_MODE", "source-string", "source-default-expression", "configuration-reader"},
		{"apps/operator-console/runtime.ts", "ENGRAM_TS_OPTIONAL", "source-string", "source-default-expression", "configuration-reader"},
		{"scripts/runtime.sh", "ENGRAM_SCRIPT_URL", "shell-parameter-expansion", "source-default-expression", "configuration-reader"},
		{"scripts/runtime.sh", "ENGRAM_SHELL_SOURCE", "shell-parameter-expansion", "source-default-expression", "configuration-reader"},
		{"scripts/runtime.sh", "ENGRAM_NESTED_OUTER", "shell-parameter-expansion", "source-default-expression", "configuration-reader"},
		{"scripts/runtime.sh", "ENGRAM_NESTED_INNER", "shell-parameter-expansion", "source-default-expression", "configuration-reader"},
		{"scripts/runtime.sh", "ENGRAM_REQUIRED", "shell-parameter-expansion", "required-environment", "configuration-reader"},
		{"scripts/runtime.sh", "ENGRAM_UNBRACED", "shell-parameter-expansion", "empty-unset", "configuration-reader"},
		{"scripts/runtime.ps1", "ENGRAM_SCRIPT_TIMEOUT", "powershell-string", "source-default-expression", "configuration-reader"},
		{"scripts/runtime.py", "ENGRAM_PY_SETTING", "python-string", "source-default-expression", "configuration-reader"},
	} {
		if !hasReader(flags, want.path, want.name, want.parser, want.defaultKind, want.classification) {
			t.Fatalf("missing runtime environment reader %#v: %#v", want, flags.Records)
		}
	}
}

func assertNoTestOrLocalFlagReaders(t *testing.T, flags Report) {
	t.Helper()
	for _, record := range flags.Records {
		if isTestSource(record.Path) || strings.HasSuffix(record.Name, "_LOCAL") {
			t.Fatalf("test-only or local reader leaked into runtime inventory: %#v", record)
		}
	}
}

func assertNoLiteralFlagReaders(t *testing.T, flags Report) {
	t.Helper()
	for _, forbidden := range []string{"ENGRAM_JS_COMMENT", "ENGRAM_JS_STRING", "ENGRAM_CJS_COMMENT", "ENGRAM_CJS_STRING", "ENGRAM_PS_COMMENT", "ENGRAM_PS_STRING", "ENGRAM_PS_ESCAPED"} {
		if hasRecord(flags, "environment-reader", forbidden, "") {
			t.Fatalf("comment or string produced an environment reader %q: %#v", forbidden, flags.Records)
		}
	}
}

func assertDynamicFlagReaders(t *testing.T, flags Report) {
	t.Helper()
	if !hasReader(flags, "internal/worker/dynamic.go", "", "source-uncertain", "source-unspecified", "source-uncertain") || !hasReader(flags, "plugin/engram/hooks/runtime.js", "", "source-uncertain", "source-unspecified", "source-uncertain") {
		t.Fatalf("dynamic environment readers must remain source-uncertain: %#v", flags.Records)
	}
}

func assertNoFlagTestPaths(t *testing.T, flags Report) {
	t.Helper()
	for _, path := range []string{"Tests/reader.go", "ui/__TESTS__/reader.js", "playwright.config.ts"} {
		if hasPathRecord(flags, "environment-reader", path) {
			t.Fatalf("test source %q leaked into runtime flag inventory: %#v", path, flags.Records)
		}
	}
}

func assertRuntimeSourceFiles(t *testing.T, root string) {
	t.Helper()
	source, err := ScanSource(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasPathRecord(source, "source-file", "plugin/engram/hooks/dispatcher.cjs") {
		t.Fatalf("CJS source file missing from current-source inventory: %#v", source.Records)
	}
	for _, testPath := range []string{"internal/worker/runtime_test.go", "tests/reader.go", "test/reader.js", "ui/__tests__/reader.js", "ui/reader_test.js", "Tests/reader.go", "ui/__TESTS__/reader.js", "playwright.config.ts"} {
		for _, record := range source.Records {
			if record.Path == testPath {
				t.Fatalf("test source leaked into current-source inventory: %#v", record)
			}
		}
	}
}

func TestFlagAndSurfaceScansClassifySourceClaims(t *testing.T) {
	root := inventoryFixture(t)
	flags, err := ScanFlags(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRecord(flags, "environment-reader", "ENGRAM_FEATURE", "exact-lowercase-true") {
		t.Fatalf("exact feature-flag reader missing: %#v", flags.Records)
	}
	if !hasRecord(flags, "environment-reader", "redacted", "") {
		t.Fatalf("sensitive environment reader was not redacted: %#v", flags.Records)
	}

	surfaces, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRecord(surfaces, "http-route", "GET /api/fixture", "") || !hasRecord(surfaces, "mcp-tool", "fixture_tool", "") || !hasRecord(surfaces, "hook", "session-end", "") {
		t.Fatalf("source-declared transport claims missing: %#v", surfaces.Records)
	}
	if !hasRecord(surfaces, "ui-root-claim", "ui", "") || !hasRecord(surfaces, "ui-root-claim", "apps/operator-console", "") {
		t.Fatalf("both UI roots must remain claim-only inventory roots: %#v", surfaces.Records)
	}
}

func TestProjectDataScanClassifiesFamiliesWithoutRows(t *testing.T) {
	report, err := ScanProjectData(inventoryFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	if !hasRecord(report, "project-bearing-field", "Memory.Project", "") || !hasRecord(report, "project-bearing-field", "Project.ID", "") {
		t.Fatalf("project-bearing relational families missing: %#v", report.Records)
	}
	for _, classification := range []string{"cache", "job", "import-export"} {
		if !hasClassification(report, classification) {
			t.Fatalf("project-bearing %s family missing: %#v", classification, report.Records)
		}
	}
	for _, path := range []string{"Tests/project_data.go", "internal/__TESTS__/project_data.go"} {
		if hasPathRecord(report, "project-bearing-field", path) || hasPathRecord(report, "project-data-family", path) {
			t.Fatalf("test source %q leaked into project-bearing data inventory: %#v", path, report.Records)
		}
	}
}

func TestProjectDataScanIncludesNonGoSourceContexts(t *testing.T) {
	report, err := ScanProjectData(inventoryFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"plugin/engram/extensions/engram-memory.mjs",
		"plugin/engram/hooks/session-start.js",
		"plugin/openclaw-engram/src/client.ts",
		"plugin/engram/hooks/project-cache.cjs",
		"plugin/openclaw-engram/src/project-view.tsx",
		"plugin/engram/hooks/project-template.mjs",
	} {
		if !hasUncertainProjectData(report, path) {
			t.Fatalf("project-bearing non-Go source %q missing: %#v", path, report.Records)
		}
	}
	for _, path := range []string{"plugin/engram/extensions/project-docs.mjs", "plugin/engram/hooks/project-data.test.js"} {
		if hasPathRecord(report, "project-bearing-data", path) {
			t.Fatalf("non-code project reference %q leaked into project-bearing inventory: %#v", path, report.Records)
		}
	}
}

func TestProjectDataScanIncludesVueScriptContexts(t *testing.T) {
	root := t.TempDir()
	const scriptPath = "apps/operator-console/components/SettingsModal.vue"
	writeFixture(t, root, scriptPath, `<template><p>project context</p></template>
<style>.project-card { color: black; }</style>
<script setup>
const draftSourceProject = ref("")
</script>
`)
	writeFixture(t, root, "apps/operator-console/components/TemplateOnly.vue", `<template><p>project context</p></template>
<style>.project-card { color: black; }</style>
`)

	report, err := ScanProjectData(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasUncertainProjectData(report, scriptPath) {
		t.Fatalf("Vue script project context missing: %#v", report.Records)
	}
	if projectDataRecordCount(report, scriptPath) != 1 {
		t.Fatalf("Vue template/style literals leaked into project inventory: %#v", report.Records)
	}
	if hasPathRecord(report, "project-bearing-data", "apps/operator-console/components/TemplateOnly.vue") {
		t.Fatalf("Vue template/style-only project text leaked into project inventory: %#v", report.Records)
	}
}

func TestProjectDataScanRequiresGoLexicalEvidence(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "internal/mcp/context.go", `package mcp

// Project data is unavailable here.
const contextLabel = "project"
`)
	writeFixture(t, root, "internal/mcp/context_types.go", `package mcp

type Context interface {
	ProjectData() string
}
`)

	report, err := ScanProjectData(root)
	if err != nil {
		t.Fatal(err)
	}
	if hasPathRecord(report, "project-data-family", "internal/mcp/context.go") {
		t.Fatalf("comment/string-only source leaked into project-bearing inventory: %#v", report.Records)
	}
	if !hasUncertainGoProjectFamily(report, "internal/mcp/context_types.go") {
		t.Fatalf("project-bearing Go declaration missing: %#v", report.Records)
	}
}

func TestProjectDataScanClassifiesDirectGoMapPayloadKeys(t *testing.T) {
	root := t.TempDir()
	const directPath = "internal/books/pipeline.go"
	const dynamicPath = "internal/books/dynamic.go"
	const commentPath = "internal/books/comments.go"
	writeFixture(t, root, directPath, `package books
func metadata(project string) map[string]any {
	return map[string]any{
		"project": "fixture-project-value",
	}
}
`)
	writeFixture(t, root, dynamicPath, `package books
func metadata(projectKey string) map[string]any {
	return map[string]any{
		projectKey: "fixture-project-value",
	}
}
`)
	writeFixture(t, root, commentPath, `package books
// "project": "fixture-project-value"
const label = "project"
`)

	report, err := ScanProjectData(root)
	if err != nil {
		t.Fatal(err)
	}

	directFound := false
	dynamicFound := false
	for _, record := range report.Records {
		switch record.Path {
		case directPath:
			if record.Kind != "project-bearing-data" || record.Line != 4 || record.Name != "" || record.Parser != "" || record.Default != "" || record.Classification != "serialized" {
				t.Fatalf("direct map key record = %#v", record)
			}
			directFound = true
		case dynamicPath:
			if record.Kind != "project-data-family" || record.Name != "unresolved-declaration" || record.Classification != "source-uncertain" {
				t.Fatalf("dynamic map key record = %#v", record)
			}
			dynamicFound = true
		case commentPath:
			t.Fatalf("comment/string-only map syntax leaked into project-bearing inventory: %#v", record)
		}
	}
	if !directFound || !dynamicFound {
		t.Fatalf("map key records missing: %#v", report.Records)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "fixture-project-value") {
		t.Fatalf("map payload value leaked into inventory: %s", encoded)
	}
}

func hasRecord(report Report, kind, name, parser string) bool {
	for _, record := range report.Records {
		if record.Kind == kind && record.Name == name && (parser == "" || record.Parser == parser) {
			return true
		}
	}
	return false
}

func hasReader(report Report, path, name, parser, defaultKind, classification string) bool {
	for _, record := range report.Records {
		if record.Kind == "environment-reader" && record.Path == path && record.Name == name && record.Parser == parser && record.Default == defaultKind && record.Classification == classification {
			return true
		}
	}
	return false
}

func hasPathRecord(report Report, kind, path string) bool {
	for _, record := range report.Records {
		if record.Kind == kind && record.Path == path {
			return true
		}
	}
	return false
}

func hasUncertainGoProjectFamily(report Report, path string) bool {
	for _, record := range report.Records {
		if record.Kind == "project-data-family" && record.Path == path && record.Name == "unresolved-declaration" && record.Classification == "source-uncertain" {
			return true
		}
	}
	return false
}

func projectDataRecordCount(report Report, path string) int {
	count := 0
	for _, record := range report.Records {
		if record.Kind == "project-bearing-data" && record.Path == path {
			count++
		}
	}
	return count
}

func hasUncertainProjectData(report Report, path string) bool {
	for _, record := range report.Records {
		if record.Kind == "project-bearing-data" && record.Path == path && record.Line > 0 && record.Name == "" && record.Parser == "" && record.Default == "" && record.Classification == "source-uncertain" {
			return true
		}
	}
	return false
}

func hasClassification(report Report, classification string) bool {
	for _, record := range report.Records {
		if record.Classification == classification {
			return true
		}
	}
	return false
}

func inventoryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "internal/worker/routes.go", `package worker
import (
  "os"
  chi "github.com/go-chi/chi/v5"
)
func routes(r chi.Router) {
	  _ = os.Getenv("ENGRAM_FEATURE") == "true"
	  _ = os.Getenv("ENGRAM_AUTH_ADMIN_TOKEN")
	  _ = os.Getenv("ENGRAM_DB_PATH")
	  _ = os.Getenv("DATABASE_DSN")
	  _ = os.Getenv("ENGRAM_AUTH_ENABLED") == "true"
	  r.Get("/api/fixture", handle)
}
var secret = "hunter2"
`)
	writeFixture(t, root, "internal/worker/runtime_test.go", `package worker
import "os"
func testOnly() { _ = os.Getenv("ENGRAM_TEST_ONLY") }
`)
	writeFixture(t, root, "tests/reader.go", `package tests
import "os"
func reader() { _ = os.Getenv("ENGRAM_TESTS_ONLY") }
`)
	writeFixture(t, root, "test/reader.js", `const reader = process.env.ENGRAM_TEST_DIRECTORY`)
	writeFixture(t, root, "ui/__tests__/reader.js", `const reader = process.env.ENGRAM_JAVASCRIPT_TEST_DIRECTORY`)
	writeFixture(t, root, "ui/reader_test.js", `const reader = process.env.ENGRAM_JAVASCRIPT_TEST_SUFFIX`)
	writeFixture(t, root, "Tests/reader.go", `package tests
import "os"
func reader() { _ = os.Getenv("ENGRAM_UPPERCASE_TEST_DIRECTORY") }
`)
	writeFixture(t, root, "ui/__TESTS__/reader.js", `const reader = process.env.ENGRAM_UPPERCASE_JAVASCRIPT_TEST_DIRECTORY`)
	writeFixture(t, root, "Tests/project_data.go", `package tests
type FixtureProjectData struct { ProjectID string }
`)
	writeFixture(t, root, "internal/__TESTS__/project_data.go", `package tests
type FixtureProjectData struct { ProjectID string }
`)
	writeFixture(t, root, "plugin/engram/extensions/engram-memory.mjs", `const projectContext = { Project: identity.project };`)
	writeFixture(t, root, "plugin/engram/extensions/project-docs.mjs", "// project comment\n/* workspace comment */\nconst documentation = \"tenant documentation\";\nconst template = `project documentation`;\n")
	writeFixture(t, root, "plugin/engram/hooks/session-start.js", `const cacheProject = ctx.ProjectSelector || project;`)
	writeFixture(t, root, "plugin/engram/hooks/project-cache.cjs", `const project = context.project;`)
	writeFixture(t, root, "plugin/engram/hooks/project-data.test.js", `const project = "test-only";`)
	writeFixture(t, root, "plugin/openclaw-engram/src/client.ts", `export interface ContextRequest { project: string }`)
	writeFixture(t, root, "plugin/openclaw-engram/src/project-view.tsx", `export const ProjectView = ({ project }: { project: string }) => project;`)
	writeFixture(t, root, "plugin/engram/hooks/project-template.mjs", "const rendered = `${identity.project}`\n")

	writeFixture(t, root, "internal/worker/dynamic.go", `package worker
	import "os"
	func dynamic(name string) string { return os.Getenv(name) }
	`)
	writeFixture(t, root, "internal/config/config.go", `package config
import (
	"os"
	"strconv"
	"strings"
)
func Load() {
	if v := strings.TrimSpace(os.Getenv("ENGRAM_CONTEXT_MAX_TOKENS")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 { _ = n }
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_INJECT_UNIFIED")); v != "" {
		if b, err := strconv.ParseBool(v); err == nil { _ = b }
	}
	if v := strings.TrimSpace(os.Getenv("ENGRAM_AUTHENTIK_ENABLED")); v == "true" || v == "1" { }
	raw := os.Getenv("ENGRAM_FOLLOWING_PARSER")
	normalized := strings.TrimSpace(raw)
	if n, err := strconv.Atoi(normalized); err == nil { _ = n }
}
`)
	writeFixture(t, root, "playwright.config.ts", `const port = process.env.ENGRAM_PLAYWRIGHT_PORT ?? "37777"`)

	writeFixture(t, root, "plugin/engram/hooks/runtime.js", `// process.env.ENGRAM_JS_COMMENT
const literal = "process.env.ENGRAM_JS_STRING";
const timeout = Number(process.env.ENGRAM_JS_TIMEOUT || "5");
const dynamic = process.env[runtimeKey]`)
	writeFixture(t, root, "plugin/engram/hooks/dispatcher.cjs", `// process.env.ENGRAM_CJS_COMMENT
const literal = "process.env.ENGRAM_CJS_STRING";
const enabled = process.env.ENGRAM_CJS_FEATURE === "true"`)
	writeFixture(t, root, "apps/operator-console/runtime.ts", `const mode = process.env["ENGRAM_TS_MODE"] ?? "safe"; const optional = process.env?.ENGRAM_TS_OPTIONAL ?? "safe"`)
	writeFixture(t, root, "scripts/runtime.sh", `ENGRAM_SCRIPT_URL="${ENGRAM_SCRIPT_URL:-https://private.example}"
WRAPPED="${ENGRAM_SHELL_SOURCE:-https://private.example}"
NESTED="${ENGRAM_NESTED_OUTER:-${ENGRAM_NESTED_INNER:-fallback}}"
: "${ENGRAM_REQUIRED:?must-set}"
printf '%s\n' "$ENGRAM_UNBRACED"
ENGRAM_LOCAL="local"
printf '%s\n' "$ENGRAM_LOCAL"`)
	writeFixture(t, root, "scripts/runtime.ps1", `# $env:ENGRAM_PS_COMMENT
$single = '$env:ENGRAM_PS_STRING'
$escaped = "`+"`"+`$env:ENGRAM_PS_ESCAPED"
$timeout = ${env:ENGRAM_SCRIPT_TIMEOUT} ?? "5"`)
	writeFixture(t, root, "scripts/runtime.py", `setting = os.getenv("ENGRAM_PY_SETTING", "private-default")`)
	writeFixture(t, root, "internal/mcp/tools.go", `package mcp
var tool = ToolDefinition{Name: "fixture_tool"}
type ToolDefinition struct { Name string }
`)
	writeFixture(t, root, "internal/db/gorm/models.go", `package gorm
	type Memory struct { Project string `+"`gorm:\"column:project\" json:\"project\"`"+` }
	type Project struct { ID string `+"`gorm:\"primaryKey\"`"+` }
	`)
	writeFixture(t, root, "internal/jobs/queue.go", `package jobs
type JobPayload struct { ProjectID string `+"`json:\"project_id\"`"+` }
`)
	writeFixture(t, root, "internal/cache/project_cache.go", `package cache
	type ProjectCacheEntry struct { ProjectID string }
	`)
	writeFixture(t, root, "internal/importer/project.go", `package importer
	type ImportRecord struct { Project string }
	`)
	writeFixture(t, root, "plugin/engram/hooks/session-end.js", `const endpoint = "https://private.example"`)
	writeFixture(t, root, "docs/current.md", "# current claim\n")
	writeFixture(t, root, "ui/view.vue", "<template>private dashboard</template>")
	writeFixture(t, root, "apps/operator-console/app.vue", "<template>private operator</template>")
	return root
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
