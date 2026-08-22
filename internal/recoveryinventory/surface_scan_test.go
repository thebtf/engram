package recoveryinventory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanSurfacesUsesRouterRegistrationSemantics(t *testing.T) {
	root := t.TempDir()
	writeSurfaceSource(t, root, "internal/worker/routes.go", `package worker

import chi "github.com/go-chi/chi/v5"

type service struct { router *chi.Mux }
type query struct{}
type unrelated struct{}

func (query) Get(string) string { return "" }
func (unrelated) Get(string, any) {}

func routes(r chi.Router) {
	child := chi.NewRouter()
	child.Get("/child", nil)
	child.Route("/nested", func(r chi.Router) {
		r.Post("/leaf", nil)
	})
	r.Mount("/mounted", child)
	r.Route("/api", func(r chi.Router) {
		r.Route("/nested", func(r chi.Router) {
			r.Get("/fixture", nil)
		})
	})
}

func (s *service) setupRoutes() {
	s.router.Get("/field", nil)
}

func unrelatedMethods(q query, u unrelated) {
	_ = q.Get("/query")
	u.Get("/fabricated", nil)
}
`)
	writeSurfaceSource(t, root, "internal/worker/routes_test.go", `package worker

import chi "github.com/go-chi/chi/v5"

func testRoutes(r chi.Router) { r.Get("/test-only", nil) }
`)
	writeSurfaceSource(t, root, "internal/handlers/codeintel/module.go", `package codeintel

import "github.com/thebtf/engram/internal/module"

const indexedTool = "daemon_index"

type Module struct{}

func (*Module) Tools() []module.ToolDef {
	return []module.ToolDef{{Name: indexedTool}, {Name: "daemon_status"}}
}
`)
	writeSurfaceSource(t, root, "plugin/engram/hooks/hooks.json", `{"hooks":{"SessionStart":[{"hooks":[{"command":"node dispatcher.cjs"}]}]}}`)
	writeSurfaceSource(t, root, "plugin/engram/hooks/dispatcher.cjs", "module.exports.main = () => {}\n")
	writeSurfaceSource(t, root, "plugin/engram/hooks/inactive.cjs", "module.exports.main = () => {}\n")

	report, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		kind string
		name string
	}{
		{"http-route", "GET /api/nested/fixture"},
		{"http-route", "GET /mounted/child"},
		{"http-route", "POST /mounted/nested/leaf"},
		{"http-route", "GET /field"},
		{"daemon-tool", "daemon_index"},
		{"daemon-tool", "daemon_status"},
		{"hook", "dispatcher"},
	} {
		if !hasSurfaceRecord(report, want.kind, want.name) {
			t.Fatalf("missing %s %q: %#v", want.kind, want.name, report.Records)
		}
	}
	for _, fabricated := range []string{"GET /query", "GET /fabricated", "GET /test-only"} {
		if hasSurfaceRecord(report, "http-route", fabricated) {
			t.Fatalf("fabricated route %q: %#v", fabricated, report.Records)
		}
	}
	if hasSurfaceRecord(report, "hook", "inactive") {
		t.Fatalf("unactivated CJS hook was inventoried: %#v", report.Records)
	}
}

func TestScanSurfacesRejectsChiImportPrefixCollision(t *testing.T) {
	root := t.TempDir()
	writeSurfaceSource(t, root, "internal/worker/routes.go", `package worker

import (
	actual "github.com/go-chi/chi/v5"
	chi "github.com/go-chi/chimera"
)

func actualRoutes(r actual.Router) { r.Get("/actual", nil) }
func collisionRoutes(r chi.Router) { r.Get("/collision", nil) }
`)

	report, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSurfaceRecord(report, "http-route", "GET /actual") {
		t.Fatalf("versioned chi alias was not inventoried: %#v", report.Records)
	}
	if hasSurfaceRecord(report, "http-route", "GET /collision") {
		t.Fatalf("prefix-collision import authorized a route: %#v", report.Records)
	}
}

func TestScanSurfacesRejectsMiddlewareOnlyChiImport(t *testing.T) {
	root := t.TempDir()
	writeSurfaceSource(t, root, "internal/worker/routes.go", `package worker

	import chi "github.com/go-chi/chi/v5/middleware"

	func routes(r chi.Router) { r.Get("/fabricated", nil) }
`)

	report, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	if hasSurfaceRecord(report, "http-route", "GET /fabricated") {
		t.Fatalf("middleware-only import authorized a route: %#v", report.Records)
	}
}

func TestScanSurfacesMarksUnresolvedChiRoutesUncertain(t *testing.T) {
	root := t.TempDir()
	writeSurfaceSource(t, root, "internal/worker/routes.go", `package worker

import chi "github.com/go-chi/chi/v5"

var mounted chi.Router

func callback(chi.Router) {}

func routes(r chi.Router, child chi.Router, prefix, path, method string) {
	r.Get(path, nil)
	r.Method(method, "/method", nil)
	r.Route(prefix, func(r chi.Router) { r.Get("/nested", nil) })
	r.Route("/callback", callback)
	r.Mount(prefix, child)
	r.Mount("/mounted", mounted)
}
`)

	report, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := map[int]struct{}{10: {}, 11: {}, 12: {}, 13: {}, 14: {}, 15: {}}
	for _, record := range report.Records {
		if record.Kind != "http-route" || record.Path != "internal/worker/routes.go" || record.Classification != "source-uncertain" {
			continue
		}
		if record.Name != "" {
			t.Fatalf("unresolved route fabricated a name: %#v", record)
		}
		if _, ok := wantLines[record.Line]; !ok {
			t.Fatalf("unexpected unresolved route record: %#v", record)
		}
		delete(wantLines, record.Line)
	}
	if len(wantLines) != 0 {
		t.Fatalf("missing unresolved route records at lines %#v: %#v", wantLines, report.Records)
	}
	if hasSurfaceRecord(report, "http-route", "GET /nested") {
		t.Fatalf("dynamic route prefix fabricated a literal route: %#v", report.Records)
	}
}

func TestScanSurfacesExcludesTestHarnessClaims(t *testing.T) {
	root := t.TempDir()
	fixtures := []struct {
		path      string
		wantClaim bool
	}{
		{"apps/operator-console/src/dashboard.ts", true},
		{"apps/operator-console/test/dashboard.ts", false},
		{"apps/operator-console/tests/browser/dashboard.ts", false},
		{"apps/operator-console/__tests__/dashboard.ts", false},
		{"apps/operator-console/testdata/dashboard.ts", false},
		{"apps/operator-console/fixtures/dashboard.ts", false},
		{"apps/operator-console/src/dashboard.spec.ts", false},
		{"apps/operator-console/src/dashboard.test.vue", false},
		{"apps/operator-console/playwright.config.ts", false},
	}
	for _, fixture := range fixtures {
		writeSurfaceSource(t, root, fixture.path, "export const dashboard = true\n")
	}

	report, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		if got := hasSurfacePath(report, "ui-claim", fixture.path); got != fixture.wantClaim {
			t.Errorf("ui claim for %q = %t, want %t: %#v", fixture.path, got, fixture.wantClaim, report.Records)
		}
	}
}

func TestSurfaceTestFileRecognizesSupportedSuffixes(t *testing.T) {
	for _, path := range []string{"apps/operator-console/src/dashboard.test.vue", "tools/dashboard.test.py"} {
		if !isSurfaceTestFile(path) {
			t.Errorf("test path %q was not excluded", path)
		}
	}
}

func TestActivatesDispatcherRequiresExactPath(t *testing.T) {
	for command, want := range map[string]bool{
		"node dispatcher.cjs":                                                  true,
		"node ./dispatcher.cjs":                                                true,
		"node plugin/engram/hooks/dispatcher.cjs":                              true,
		"node -e \"require('plugin/engram/hooks/dispatcher.cjs')\"":            true,
		"node -e \"const dispatcher = 'dispatcher.cjs'; require(dispatcher)\"": false,
		"node inactive/dispatcher.cjs":                                         false,
		"node hook-runner.cjs dispatcher.cjs":                                  false,
		"node -e \"require('custom-dispatcher.cjs')\"":                         false,
		"node -e \"console.log('dispatcher.cjs')\"":                            false,
		"node -e \"const dispatcher = process.env.DISPATCHER\"":                false,
	} {
		if got := activatesDispatcher(command); got != want {
			t.Errorf("activatesDispatcher(%q) = %t, want %t", command, got, want)
		}
	}
}

func TestScanSurfacesClassifiesUnresolvedHookActivation(t *testing.T) {
	for name, command := range map[string]string{
		"runtime-selected": "node -e 'const roots = [process.cwd()]; const f = roots.map(root => root + `/hooks/dispatcher.cjs`)[0]; require(f)'",
		"unresolvable":     "hook-runner --dispatcher $DISPATCHER",
		"direct":           "node plugin/engram/hooks/dispatcher.cjs",
		"custom":           "node custom-dispatcher.cjs",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeSurfaceSource(t, root, "plugin/engram/hooks/hooks.json", `{"hooks":{"SessionStart":[{"hooks":[{"command":"`+command+`"}]}]}}`)
			writeSurfaceSource(t, root, "plugin/engram/hooks/dispatcher.cjs", "module.exports.main = () => {}\n")

			report, err := ScanSurfaces(root)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, record := range report.Records {
				if record.Kind != "hook" || record.Name != "dispatcher" {
					continue
				}
				found = true
				want := "source-uncertain"
				if name == "direct" {
					want = "source-declared"
				}
				if record.Classification != want {
					t.Fatalf("dispatcher classification = %q, want %q: %#v", record.Classification, want, report.Records)
				}
			}
			if found != (name != "custom") {
				t.Fatalf("dispatcher record = %t, want %t: %#v", found, name != "custom", report.Records)
			}
		})
	}
}

func TestScanSurfacesMarksUnavailableHookManifestUncertain(t *testing.T) {
	for name, manifest := range map[string]string{"missing": "", "malformed": "{"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if manifest != "" {
				writeSurfaceSource(t, root, "plugin/engram/hooks/hooks.json", manifest)
			}
			writeSurfaceSource(t, root, "plugin/engram/hooks/dispatcher.cjs", "module.exports.main = () => {}\n")

			report, err := ScanSurfaces(root)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range report.Records {
				if record.Kind == "hook" && record.Name == "dispatcher" && record.Classification == "source-uncertain" {
					return
				}
			}
			t.Fatalf("missing uncertain dispatcher record: %#v", report.Records)
		})
	}
}

func TestScanSurfacesMarksUnresolvedSourceSemanticsUncertain(t *testing.T) {
	root := t.TempDir()
	writeSurfaceSource(t, root, "internal/worker/routes.go", `package worker

import chi "github.com/go-chi/chi/v5"

type dynamicRouter interface { Get(string, any) }

func factory() dynamicRouter { return nil }

func routes(r chi.Router, unknown dynamicRouter) {
	r.Get("/exact", nil)
	unknown.Get("/unknown", nil)
	dynamic := factory()
	dynamic.Get("/dynamic", nil)
}`)
	writeSurfaceSource(t, root, "internal/handlers/tools.go", `package handlers

import "github.com/thebtf/engram/internal/module"

var dynamicName string

func factory() []module.ToolDef { return nil }

func (*Static) Tools() []module.ToolDef { return []module.ToolDef{{Name: "literal"}} }

func (*Local) Tools() []module.ToolDef {
	tools := []module.ToolDef{{Name: "local"}}
	return tools
}

func (*Dynamic) Tools() []module.ToolDef { return factory() }

func (*DynamicName) Tools() []module.ToolDef { return []module.ToolDef{{Name: dynamicName}} }
`)

	report, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSurfaceRecord(report, "http-route", "GET /exact") || !hasSurfaceRecord(report, "daemon-tool", "literal") {
		t.Fatalf("literal routes or tools missing: %#v", report.Records)
	}
	uncertainRoutes, uncertainTools := 0, 0
	for _, record := range report.Records {
		switch record.Kind {
		case "http-route":
			if record.Classification == "source-uncertain" {
				if record.Name != "" {
					t.Fatalf("unresolved route fabricated a name: %#v", record)
				}
				uncertainRoutes++
			}
		case "daemon-tool":
			if record.Classification == "source-uncertain" {
				if record.Name != "" {
					t.Fatalf("unresolved daemon tool fabricated a name: %#v", record)
				}
				uncertainTools++
			}
		}
	}
	if uncertainRoutes != 2 || uncertainTools != 3 {
		t.Fatalf("unresolved records routes=%d tools=%d, want 2 and 3: %#v", uncertainRoutes, uncertainTools, report.Records)
	}
}

func TestScanSurfacesMarksDynamicProxyToolsUncertain(t *testing.T) {
	root := t.TempDir()
	writeSurfaceSource(t, root, "internal/handlers/engramcore/tools.go", `package engramcore

import "github.com/thebtf/engram/internal/module"

func fetchRemote() []module.ToolDef { return nil }

func (*Module) ProxyTools() ([]module.ToolDef, error) {
	tools := fetchRemote()
	return tools, nil
}`)

	report, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range report.Records {
		if record.Kind != "daemon-tool" || record.Path != "internal/handlers/engramcore/tools.go" || record.Classification != "source-uncertain" {
			continue
		}
		if record.Name != "" || record.Line != 9 {
			t.Fatalf("dynamic ProxyTools uncertainty = %#v, want provider return location without a tool name", record)
		}
		return
	}
	t.Fatalf("missing dynamic ProxyTools uncertainty: %#v", report.Records)
}

func TestScanSurfacesMarksDynamicOpenClawFactoryNamesUncertain(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{
		"plugin/openclaw-engram/src/tools/engram-search.ts",
		"plugin/openclaw-engram/src/tools/engram-presets.ts",
	} {
		writeSurfaceSource(t, root, path, `function createTool(name: string) {
	return {
		name,
		description: 'dynamic tool',
	};
}
const declared = { name: 'openclaw_declared' };
`)
	}

	report, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	if !hasSurfaceRecord(report, "openclaw-tool", "openclaw_declared") {
		t.Fatalf("missing direct literal OpenClaw declaration: %#v", report.Records)
	}
	for _, path := range []string{
		"plugin/openclaw-engram/src/tools/engram-search.ts",
		"plugin/openclaw-engram/src/tools/engram-presets.ts",
	} {
		uncertain := 0
		for _, record := range report.Records {
			if record.Kind != "openclaw-tool" || record.Path != path || record.Classification != "source-uncertain" {
				continue
			}
			if record.Name != "" {
				t.Fatalf("dynamic OpenClaw tool fabricated a name: %#v", record)
			}
			uncertain++
		}
		if uncertain != 1 {
			t.Fatalf("dynamic OpenClaw tool uncertainty for %s = %d, want 1: %#v", path, uncertain, report.Records)
		}
	}
}

func TestScanSurfacesRecoversLiteralOpenClawFactoryNames(t *testing.T) {
	root := t.TempDir()
	writeSurfaceSource(t, root, "plugin/openclaw-engram/src/tools/engram-search.ts", `function createSearchTool(name: string) {
	return { name };
}

const declared = createSearchTool('engram_search');
const dynamic = createSearchTool(dynamicName);
// createSearchTool('engram_comment')
const text = "createSearchTool('engram_string')";
const raw = `+"`createSearchTool('engram_template')`"+`;
`)
	writeSurfaceSource(t, root, "plugin/openclaw-engram/src/tools/engram-presets.ts", `function createPresetTool(name: string) {
	return { name };
}

const declared = createPresetTool('engram_changes');
const dynamic = createPresetTool(dynamicName);
// createPresetTool('preset_comment')
const text = "createPresetTool('preset_string')";
const raw = `+"`createPresetTool('preset_template')`"+`;
`)

	report, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		path string
		name string
	}{
		{"plugin/openclaw-engram/src/tools/engram-search.ts", "engram_search"},
		{"plugin/openclaw-engram/src/tools/engram-presets.ts", "engram_changes"},
	} {
		found := false
		for _, record := range report.Records {
			if record.Kind != "openclaw-tool" || record.Name != want.name {
				continue
			}
			if record.Path != want.path || record.Classification != "source-declared" {
				t.Fatalf("literal OpenClaw factory declaration = %#v, want source-declared %s in %s", record, want.name, want.path)
			}
			found = true
		}
		if !found {
			t.Fatalf("missing literal OpenClaw factory declaration %q: %#v", want.name, report.Records)
		}
	}
	for _, name := range []string{
		"engram_comment", "engram_string", "engram_template",
		"preset_comment", "preset_string", "preset_template",
	} {
		if hasSurfaceRecord(report, "openclaw-tool", name) {
			t.Fatalf("fabricated OpenClaw factory declaration %q: %#v", name, report.Records)
		}
	}
	for _, path := range []string{
		"plugin/openclaw-engram/src/tools/engram-search.ts",
		"plugin/openclaw-engram/src/tools/engram-presets.ts",
	} {
		uncertain := 0
		for _, record := range report.Records {
			if record.Kind != "openclaw-tool" || record.Path != path || record.Classification != "source-uncertain" {
				continue
			}
			if record.Name != "" {
				t.Fatalf("dynamic OpenClaw factory name = %#v, want unnamed uncertainty", record)
			}
			uncertain++
		}
		if uncertain != 1 {
			t.Fatalf("dynamic OpenClaw factory uncertainty for %s = %d, want 1: %#v", path, uncertain, report.Records)
		}
	}
}
func TestScanSurfacesLexesToolAndRPCDeclarationsWithoutSourceInjection(t *testing.T) {
	root := t.TempDir()
	writeSurfaceSource(t, root, "internal/mcp/tools.go", "package mcp\n\n"+
		"type ToolDefinition struct{ Name string }\n\n"+
		"var declared = ToolDefinition{Name: \"mcp_declared\"}\n"+
		"var dynamicName string\n"+
		"var dynamic = ToolDefinition{Name: dynamicName}\n"+
		"// ToolDefinition{Name: \"mcp_comment\"}\n"+
		"var raw = `ToolDefinition{Name: \"mcp_raw\"}`\n")
	writeSurfaceSource(t, root, "plugin/openclaw-engram/src/tools/tools.ts", "const literal = { name: 'openclaw_declared' };\n"+
		"const dynamic = { name: dynamicName };\n"+
		"// const comment = { name: 'openclaw_comment' };\n"+
		"const text = \"{ name: 'openclaw_string' }\";\n"+
		"const raw = `{ name: 'openclaw_template' }`;\n")
	writeSurfaceSource(t, root, "proto/example.proto", `syntax = "proto3";

service Example {
  rpc Declared(DeclaredRequest) returns (DeclaredResponse);
  // rpc Comment(CommentRequest) returns (CommentResponse);
  option (description) = "rpc StringValue(StringRequest)";
  rpc;
}`)

	report, err := ScanSurfaces(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		kind string
		name string
	}{
		{"mcp-tool", "mcp_declared"},
		{"openclaw-tool", "openclaw_declared"},
		{"grpc-method", "Declared"},
	} {
		if !hasSurfaceRecord(report, want.kind, want.name) {
			t.Fatalf("missing static %s declaration %q: %#v", want.kind, want.name, report.Records)
		}
	}
	for _, fabricated := range []struct {
		kind string
		name string
	}{
		{"mcp-tool", "mcp_comment"},
		{"mcp-tool", "mcp_raw"},
		{"openclaw-tool", "openclaw_comment"},
		{"openclaw-tool", "openclaw_string"},
		{"openclaw-tool", "openclaw_template"},
		{"grpc-method", "Comment"},
		{"grpc-method", "StringValue"},
	} {
		if hasSurfaceRecord(report, fabricated.kind, fabricated.name) {
			t.Fatalf("fabricated %s declaration %q: %#v", fabricated.kind, fabricated.name, report.Records)
		}
	}
	for _, want := range []struct {
		kind string
		path string
	}{
		{"mcp-tool", "internal/mcp/tools.go"},
		{"openclaw-tool", "plugin/openclaw-engram/src/tools/tools.ts"},
		{"grpc-method", "proto/example.proto"},
	} {
		uncertain := 0
		for _, record := range report.Records {
			if record.Kind != want.kind || record.Path != want.path || record.Classification != "source-uncertain" {
				continue
			}
			if record.Name != "" {
				t.Fatalf("uncertain %s record fabricated a name: %#v", want.kind, record)
			}
			uncertain++
		}
		if uncertain != 1 {
			t.Fatalf("uncertain %s records for %s = %d, want 1: %#v", want.kind, want.path, uncertain, report.Records)
		}
	}
}
func hasSurfaceRecord(report Report, kind, name string) bool {
	for _, record := range report.Records {
		if record.Kind == kind && record.Name == name {
			return true
		}
	}
	return false
}

func hasSurfacePath(report Report, kind, path string) bool {
	for _, record := range report.Records {
		if record.Kind == kind && record.Path == path {
			return true
		}
	}
	return false
}

func writeSurfaceSource(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
