package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLogicalProjectRealCLIHandler(t *testing.T) {
	cli := os.Getenv("ENGRAM_LOGICAL_CLI")
	dsnFile := os.Getenv("ENGRAM_IDENTITY_TEST_DSN_FILE")
	if cli == "" || dsnFile == "" {
		t.Skip("requires assigned CLI and private isolated PostgreSQL fixture")
	}
	dsn, err := os.ReadFile(dsnFile)
	if err != nil {
		t.Fatal("private fixture DSN is unavailable")
	}
	u, err := url.Parse(strings.TrimSpace(string(dsn)))
	if err != nil || u.Host != "127.0.0.1:22366" || u.Path != "/identity_fixture" {
		t.Fatal("refusing an unassigned database target")
	}
	schema := "logical_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	admin, err := gormlib.Open(postgres.Open(u.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("isolated fixture connection failed")
	}
	adminPool, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer admin.Exec("DROP SCHEMA " + schema + " CASCADE")
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := gormlib.Open(postgres.Open(u.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("isolated schema connection failed")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(4)
	defer pool.Close()
	// Match the existing catalog storage, without request-path schema creation.
	if err := db.AutoMigrate(&gormdb.Project{}, &gormdb.ProjectIdentifier{}, &models.BehavioralRule{}); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE UNIQUE INDEX logical_locator_alias ON project_identifiers(scheme, normalized_value) WHERE status IN ('active','redirected')`,
		`ALTER TABLE project_identifiers ADD CONSTRAINT logical_project_fk FOREIGN KEY(project_key) REFERENCES projects(project_key)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	service := &Service{store: &gormdb.Store{DB: db}}
	var bootstrapCalls atomic.Int32
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/bootstrap" {
			http.NotFound(w, r)
			return
		}
		bootstrapCalls.Add(1)
		service.handleProjectBootstrap(w, r)
	}))
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	resolveCLI := func(root string) projectidentity.Project {
		t.Helper()
		command := exec.CommandContext(ctx, cli, "project", "resolve", "--root", root, "--json")
		command.Env = append(os.Environ(), "ENGRAM_URL="+httpServer.URL, "ENGRAM_SERVER_URL=", "ENGRAM_TOKEN=")
		raw, err := command.Output()
		if err != nil {
			t.Fatalf("actual CLI failed: %v: %s", err, raw)
		}
		var project projectidentity.Project
		if err := json.Unmarshal(raw, &project); err != nil {
			t.Fatal(err)
		}
		if err := projectidentity.ValidateProject(project); err != nil {
			t.Fatal(err)
		}
		return project
	}
	makeRepo := func(parent, origin string) string {
		t.Helper()
		root := filepath.Join(parent, "same-basename")
		if err := os.MkdirAll(root, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "--quiet"}, {"remote", "add", "origin", origin}, {"remote", "add", "upstream", "https://example.test/Common/Upstream.git"}} {
			if output, err := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
				t.Fatalf("git fixture: %v: %s", err, output)
			}
		}
		return root
	}
	root := makeRepo(t.TempDir(), "git@example.test:Team/Repo.git")
	if err := os.WriteFile(filepath.Join(root, ".engram-project"), []byte(`{"name":"Existing workspace"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	first := resolveCLI(root)
	if !projectidentity.ValidProjectID(first.ProjectID) {
		t.Fatal("catalog did not return UUID")
	}
	nested := filepath.Join(root, "src")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if repeated := resolveCLI(nested); repeated.ProjectID != first.ProjectID {
		t.Fatal("nested repeat changed ID")
	}
	if bootstrapCalls.Load() != 1 {
		t.Fatal("pinned repeat performed a catalog roundtrip")
	}
	rawMarker, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	if err != nil || !bytes.Contains(rawMarker, []byte(first.ProjectID)) {
		t.Fatal("name-only marker did not become portable")
	}
	var count int64
	if err := db.Model(&gormdb.Project{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("catalog count=%d err=%v", count, err)
	}
	var stored gormdb.Project
	if err := db.First(&stored, "id = ?", first.ProjectID).Error; err != nil || stored.ProjectKey.String != first.ProjectID || stored.AnchorProjectID.String != first.ProjectID {
		t.Fatal("catalog created a second UUID")
	}
	// Two simultaneous first locator requests return exactly the existing UUID.
	var group sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			project, err := projectidentity.HTTPBootstrap(httpServer.URL, "")(ctx, projectidentity.BootstrapRequest{RepositoryLocator: "example.test/Team/Repo"})
			if err != nil || project.ProjectID != first.ProjectID {
				failures <- fmt.Errorf("atomic bootstrap mismatch: %v", err)
			}
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	fork := makeRepo(t.TempDir(), "https://example.test/Fork/Repo.git")
	if second := resolveCLI(fork); second.ProjectID == first.ProjectID {
		t.Fatal("same basename/common upstream merged a fork")
	}
	// Real, existing MCP handler and gRPC authentication/dispatch, no tool mock.
	mcpServer := mcp.NewServer(mcp.ServerOptions{Version: "logical-source-proof"})
	mcpServer.SetBehavioralRulesStore(gormdb.NewBehavioralRulesStoreFromDB(db))
	rule := &models.BehavioralRule{Project: &first.ProjectID, Content: "portable-project-rule", Enabled: true, Version: 1}
	if err := db.Create(rule).Error; err != nil {
		t.Fatal(err)
	}
	grpcServer, internal := grpcserver.New(&mcpHandlerAdapter{mcpServer: mcpServer}, nil)
	internal.SetDB(db)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer grpcServer.Stop()
	go grpcServer.Serve(listener)
	core := engramcore.NewModuleWithClientInstanceID("logical-source-proof")
	defer core.Shutdown(context.Background())
	project := muxcore.ProjectContext{ID: "transport-local", Cwd: root, Env: map[string]string{"ENGRAM_URL": "http://" + listener.Addr().String(), "ENGRAM_TOKEN": ""}}
	tools, err := core.ProxyTools(ctx, muxcore.ProjectContext{Env: project.Env})
	if err != nil || len(tools) == 0 {
		t.Fatalf("catalog without context: %v tools=%d", err, len(tools))
	}
	args, _ := json.Marshal(map[string]string{"project": first.ProjectID})
	for range 2 {
		block, err := core.ProxyHandleTool(ctx, project, "list_rules", args)
		if err != nil || !bytes.Contains(block, []byte("portable-project-rule")) || !bytes.Contains(block, []byte(first.ProjectID)) {
			t.Fatalf("normal real handler roundtrip: %v: %s", err, block)
		}
	}
	_, err = core.ProxyHandleTool(ctx, muxcore.ProjectContext{Env: project.Env}, "list_rules", []byte(`{}`))
	var moduleErr *module.ModuleError
	if !errors.As(err, &moduleErr) || moduleErr.Code != "PROJECT_CONTEXT_REQUIRED" {
		t.Fatalf("missing scoped context: %v", err)
	}
	_, err = internal.CallTool(ctx, &pb.CallToolRequest{ToolName: "list_rules", ArgumentsJson: []byte(`{}`)})
	st := status.Convert(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("server scoped error: %v", err)
	}
	info, ok := st.Details()[0].(*errdetails.ErrorInfo)
	if !ok || info.GetReason() != "PROJECT_CONTEXT_REQUIRED" {
		t.Fatal("server scoped error is not typed")
	}
	if _, err := core.ProxyTools(ctx, muxcore.ProjectContext{Env: project.Env}); err != nil {
		t.Fatal("scoped failure killed catalog transport")
	}
	t.Logf("PASS actual CLI -> HTTP -> PostgreSQL; UUID=%s, nested repeat offline-pinned; 8 concurrent catalog requests; fork/common-upstream distinct; normal Go proxy -> real gRPC -> real MCP list_rules twice; missing context typed; catalog survives", first.ProjectID)
}

func TestLogicalProjectConcurrentFirstBootstrap(t *testing.T) {
	dsnFile := os.Getenv("ENGRAM_IDENTITY_TEST_DSN_FILE")
	if dsnFile == "" {
		t.Skip("requires private isolated PostgreSQL fixture")
	}
	dsn, err := os.ReadFile(dsnFile)
	if err != nil {
		t.Fatal("private fixture DSN is unavailable")
	}
	u, err := url.Parse(strings.TrimSpace(string(dsn)))
	if err != nil || u.Host != "127.0.0.1:22366" || u.Path != "/identity_fixture" {
		t.Fatal("refusing an unassigned database target")
	}
	admin, err := gormlib.Open(postgres.Open(u.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("isolated fixture connection failed")
	}
	adminPool, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer adminPool.Close()
	schema := "logical_first_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error("isolated schema cleanup failed")
		}
	}()
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	db, err := gormlib.Open(postgres.Open(u.String()), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("isolated schema connection failed")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	pool.SetMaxOpenConns(4)
	defer pool.Close()
	if err := db.AutoMigrate(&gormdb.Project{}, &gormdb.ProjectIdentifier{}); err != nil {
		t.Fatal(err)
	}
	for _, ddl := range []string{
		`CREATE UNIQUE INDEX logical_first_locator_alias ON project_identifiers(scheme, normalized_value) WHERE status IN ('active','redirected')`,
		`ALTER TABLE project_identifiers ADD CONSTRAINT logical_first_project_fk FOREIGN KEY(project_key) REFERENCES projects(project_key)`,
	} {
		if err := db.Exec(ddl).Error; err != nil {
			t.Fatal(err)
		}
	}
	locator := "example.test/FirstCreate/" + uuid.NewString()
	var projectCount, bindingCount int64
	if err := db.Model(&gormdb.Project{}).Count(&projectCount).Error; err != nil || projectCount != 0 {
		t.Fatalf("fresh project catalog count=%d err=%v", projectCount, err)
	}
	if err := db.Model(&gormdb.ProjectIdentifier{}).Count(&bindingCount).Error; err != nil || bindingCount != 0 {
		t.Fatalf("fresh binding catalog count=%d err=%v", bindingCount, err)
	}
	const callers = 8
	var arrivals atomic.Int32
	gate := make(chan struct{})
	service := &Service{store: &gormdb.Store{DB: db}}
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/bootstrap" {
			http.NotFound(w, r)
			return
		}
		if n := arrivals.Add(1); n <= callers {
			if n == callers {
				close(gate)
			}
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		service.handleProjectBootstrap(w, r)
	}))
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	bootstrap := projectidentity.HTTPBootstrap(httpServer.URL, "")
	request := projectidentity.BootstrapRequest{RepositoryLocator: locator}
	type response struct {
		project projectidentity.Project
		err     error
	}
	responses := make(chan response, callers)
	start := make(chan struct{})
	for range callers {
		go func() {
			<-start
			project, err := bootstrap(ctx, request)
			responses <- response{project: project, err: err}
		}()
	}
	close(start)
	var id string
	for range callers {
		result := <-responses
		if result.err != nil || !projectidentity.ValidProjectID(result.project.ProjectID) {
			t.Fatalf("first bootstrap response invalid: %v", result.err)
		}
		if id == "" {
			id = result.project.ProjectID
		}
		if result.project.ProjectID != id {
			t.Fatalf("first creation returned distinct UUIDs: %s and %s", id, result.project.ProjectID)
		}
	}
	replayed, err := bootstrap(ctx, request)
	if err != nil || replayed.ProjectID != id {
		t.Fatalf("bootstrap replay changed UUID: %v", err)
	}
	if arrivals.Load() != callers+1 {
		t.Fatalf("HTTP arrivals=%d want=%d", arrivals.Load(), callers+1)
	}
	if err := db.Model(&gormdb.Project{}).Count(&projectCount).Error; err != nil || projectCount != 1 {
		t.Fatalf("first creation project rows=%d want=1 err=%v", projectCount, err)
	}
	var stored gormdb.Project
	if err := db.First(&stored, "id = ?", id).Error; err != nil || stored.ProjectKey.String != id || stored.AnchorProjectID.String != id || stored.GitRemote.String != locator {
		t.Fatal("first creation did not retain one logical UUID and locator")
	}
	t.Logf("8 synchronized FIRST bootstrap requests returned UUID=%s; projects=1; replay preserves UUID", id)
	if err := db.Model(&gormdb.ProjectIdentifier{}).Count(&bindingCount).Error; err != nil || bindingCount != 1 {
		t.Fatalf("first creation identifier bindings=%d want=1 err=%v", bindingCount, err)
	}
	if err := db.Model(&gormdb.ProjectIdentifier{}).Where("project_key = ? AND scheme = ? AND normalized_value = ? AND status = ?", id, "manual_alias", locator, "active").Count(&bindingCount).Error; err != nil || bindingCount != 1 {
		t.Fatalf("first creation locator binding=%d want=1 err=%v", bindingCount, err)
	}
	var binding gormdb.ProjectIdentifier
	if err := db.First(&binding, "normalized_value = ?", locator).Error; err != nil || binding.Source != "logical_project_bootstrap" {
		t.Fatal("primary locator binding provenance source is missing")
	}
	var provenance map[string]string
	if err := json.Unmarshal([]byte(binding.Provenance), &provenance); err != nil || provenance["method"] != "primary_repository_locator" {
		t.Fatal("primary locator binding provenance is missing")
	}
	if err := db.Model(&gormdb.Project{}).Where("id = ?", id).Updates(map[string]any{
		"git_remote": "example.test/Metadata/Changed", "display_name": "Edited display name",
	}).Error; err != nil {
		t.Fatal(err)
	}
	clone := t.TempDir()
	for _, args := range [][]string{{"init", "--quiet"}, {"remote", "add", "origin", "https://" + locator + ".git"}} {
		if raw, err := exec.CommandContext(ctx, "git", append([]string{"-C", clone}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("fresh clone fixture: %v: %s", err, raw)
		}
	}
	cloned, err := projectidentity.ResolveLogicalProject(ctx, clone, bootstrap)
	if err != nil || cloned.ProjectID != id || cloned.Name != "Edited display name" {
		t.Fatalf("fresh clone catalog lookup after metadata edit: %+v %v", cloned, err)
	}
	var pinnedBootstrapCalls int
	pinned, err := projectidentity.ResolveLogicalProject(ctx, clone, func(ctx context.Context, input projectidentity.BootstrapRequest) (projectidentity.Project, error) {
		pinnedBootstrapCalls++
		return bootstrap(ctx, input)
	})
	if err != nil || pinned.ProjectID != id || pinnedBootstrapCalls != 0 {
		t.Fatal("fixed marker identity performed a bootstrap query")
	}
	if err := db.Model(&gormdb.Project{}).Count(&projectCount).Error; err != nil || projectCount != 1 {
		t.Fatalf("metadata edit or new clone created extra projects: count=%d err=%v", projectCount, err)
	}
	if err := db.Model(&gormdb.ProjectIdentifier{}).Count(&bindingCount).Error; err != nil || bindingCount != 1 {
		t.Fatalf("metadata edit or new clone changed binding count: count=%d err=%v", bindingCount, err)
	}
	// A real uniqueness collision after project insertion must roll back the entire transaction.
	collisionLocator := "example.test/Collision/" + uuid.NewString()
	trigger := fmt.Sprintf(`CREATE FUNCTION first_bootstrap_collision() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.git_remote = '%s' THEN
				INSERT INTO project_identifiers(project_key, scheme, normalized_value, source, provenance, status)
				VALUES ('%s', 'manual_alias', NEW.git_remote, 'collision-fixture', '{}', 'active');
			END IF;
			RETURN NEW;
		END $$`, collisionLocator, id)
	if err := db.Exec(trigger).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER first_bootstrap_collision BEFORE INSERT ON projects FOR EACH ROW EXECUTE FUNCTION first_bootstrap_collision()`).Error; err != nil {
		t.Fatal(err)
	}
	_, err = bootstrap(ctx, projectidentity.BootstrapRequest{RepositoryLocator: collisionLocator})
	if projectidentity.ProjectErrorCode(err) != "PROJECT_IDENTITY_CONFLICT" {
		t.Fatalf("locator uniqueness collision is not typed: %v", err)
	}
	if err := db.Model(&gormdb.Project{}).Count(&projectCount).Error; err != nil || projectCount != 1 {
		t.Fatalf("collision left an orphan project: count=%d err=%v", projectCount, err)
	}
	if err := db.Model(&gormdb.ProjectIdentifier{}).Count(&bindingCount).Error; err != nil || bindingCount != 1 {
		t.Fatalf("collision transaction did not roll back its binding: count=%d err=%v", bindingCount, err)
	}
	var retained gormdb.ProjectIdentifier
	if err := db.First(&retained, "identifier_id = ?", binding.IdentifierID).Error; err != nil || retained.ProjectKey != id || retained.NormalizedValue != locator {
		t.Fatal("collision changed the existing binding owner")
	}
	t.Log("PASS first creation: 8 synchronized requests, one UUID/project/active binding with provenance, stable replay; metadata edit and fresh clone retain catalog identity; pinned marker makes zero bootstrap calls; unique collision typed with full rollback and no orphan")
}
