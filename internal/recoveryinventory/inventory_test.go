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

func inventoryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "internal/worker/routes.go", `package worker
import "os"
func routes(r router) {
	_ = os.Getenv("ENGRAM_FEATURE") == "true"
	_ = os.Getenv("ENGRAM_AUTH_ADMIN_TOKEN")
	r.Get("/api/fixture", handle)
}
type router interface { Get(string, any) }
var secret = "hunter2"
`)
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
