package recoveryinventory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
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
	for _, forbidden := range []string{"hunter2", "https://private.example"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("inventory exposed source content %q: %s", forbidden, encoded)
		}
	}
}

func TestFlagAndSurfaceScansClassifySourceClaims(t *testing.T) {
	root := inventoryFixture(t)
	flags, err := ScanFlags(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ENGRAM_FEATURE", "ENGRAM_DYNAMIC_FEATURE", "ENGRAM_JS_FEATURE", "ENGRAM_TS_FEATURE", "ENGRAM_SCRIPT_MODE", "ENGRAM_PS_MODE", "source-dynamic"} {
		if !hasRecord(flags, "environment-reader", name, "") {
			t.Fatalf("environment reader %q missing: %#v", name, flags.Records)
		}
	}
	if !hasRecord(flags, "environment-reader", "ENGRAM_JS_FEATURE", "source-conditional-default") || !hasRecord(flags, "environment-default", "ENGRAM_SCRIPT_DEFAULT", "source-default-declaration") {
		t.Fatalf("script default declarations missing: %#v", flags.Records)
	}
	if !hasRecord(flags, "environment-reader", "redacted", "") {
		t.Fatalf("sensitive environment reader was not redacted: %#v", flags.Records)
	}

	surfaces, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []struct {
		kind string
		name string
	}{
		{"http-route", "GET /api/fixture"},
		{"http-route", "GET /api/nested/fixture"},
		{"mcp-tool", "fixture_tool"},
		{"openclaw-tool", "engram_fixture"},
		{"hook", "session-end"},
	} {
		if !hasRecord(surfaces, record.kind, record.name, "") {
			t.Fatalf("source-declared %s missing: %#v", record.name, surfaces.Records)
		}
	}
	if hasRecord(surfaces, "http-route", "GET /project", "") {
		t.Fatalf("query accessor was misclassified as an HTTP route: %#v", surfaces.Records)
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
	if !hasRecord(report, "project-bearing-map-key", "map.project", "") || !hasRecord(report, "project-bearing-map-key", "map.project_id", "") || !hasRecord(report, "project-bearing-payload-key", "payload.project", "") {
		t.Fatalf("serialized project map and payload fields missing: %#v", report.Records)
	}
	for _, classification := range []string{"cache", "job", "import-export", "serialized-map", "serialized-payload"} {
		if !hasClassification(report, classification) {
			t.Fatalf("project-bearing %s family missing: %#v", classification, report.Records)
		}
	}
}

func TestCurrentSourceInventoryCoverageIsDeterministicAndRedacted(t *testing.T) {
	root := repositoryRoot(t)
	first, err := ScanAll(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ScanAll(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("current-source inventory is not deterministic")
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "ENGRAM_AUTH_ADMIN_TOKEN") {
		t.Fatalf("current-source inventory exposed a sensitive environment name: %s", encoded)
	}
	if !hasRecord(first[1], "environment-reader", "ENGRAM_V7_S1_STATE", "") || !hasRecord(first[2], "http-route", "GET /api/admin/invitations", "") || !hasRecord(first[2], "openclaw-tool", "engram_decisions", "") || !hasRecord(first[3], "project-bearing-payload-key", "payload.project", "") {
		t.Fatalf("current-source inventory omitted a declared recovery surface")
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

func hasClassification(report Report, classification string) bool {
	for _, record := range report.Records {
		if record.Classification == classification {
			return true
		}
	}
	return false
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate repository root")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func inventoryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "internal/worker/routes.go", `package worker
import "os"
type router struct { URL urlSource }
type urlSource struct{}
type querySource struct{}
func (urlSource) Query() querySource { return querySource{} }
func (querySource) Get(string) string { return "" }
func (router) Get(string, any) {}
func (router) Route(string, func(router)) {}
func dynamicFlag(string) string { return "ENGRAM_DYNAMIC_FEATURE" }
func routes(r router) {
	_ = os.Getenv("ENGRAM_FEATURE") == "true"
	_ = os.Getenv("ENGRAM_AUTH_ADMIN_TOKEN")
	_ = os.Getenv(dynamicFlag("feature"))
	_ = r.URL.Query().Get("project")
	r.Get("/api/fixture", nil)
	r.Route("/api", func(r router) {
		r.Route("/nested", func(r router) { r.Get("/fixture", nil) })
	})
}
var secret = "hunter2"
`)
	writeFixture(t, root, "internal/mcp/tools.go", `package mcp
var tool = ToolDefinition{Name: "fixture_tool"}
type ToolDefinition struct { Name string }
`)
	writeFixture(t, root, "internal/db/gorm/models.go", `package gorm
type Memory struct { Project string `+"`gorm:\"column:project\" json:\"project\"`"+` }
type Project struct { ID string `+"`gorm:\"primaryKey\"`"+` }
var payload = map[string]any{"project": "private"}
func usePayload(value map[string]any) { _ = value["project_id"] }
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
	writeFixture(t, root, "plugin/engram/hooks/session-end.js", `const endpoint = "https://private.example"; const enabled = process.env.ENGRAM_JS_FEATURE || "true"; const dynamic = process.env[key];`)
	writeFixture(t, root, "plugin/engram/hooks/config.ts", `const enabled = process.env["ENGRAM_TS_FEATURE"] ?? "false";`)
	writeFixture(t, root, "scripts/recovery.sh", `: "${ENGRAM_SCRIPT_MODE:=safe}"
export ENGRAM_SCRIPT_DEFAULT=safe
`)
	writeFixture(t, root, "scripts/recovery.ps1", `$mode = $env:ENGRAM_PS_MODE ?? 'safe'
`)
	writeFixture(t, root, "plugin/openclaw-engram/src/tools/fixture.ts", `export const fixture = { name: 'engram_fixture', project: 'private' }`)
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
