package engramcore

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/module/dispatcher"
	"github.com/thebtf/engram/internal/module/registry"
	"github.com/thebtf/engram/internal/projectidentity"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
)

// Draft wire projection: choices carries the existing catalog descriptor fields
// and one opaque selection_ref per candidate. No hypothetical production helper
// is called: all recovery actions enter the real dispatcher.
const (
	projectChoiceA      = "11111111-1111-4111-8111-111111111111"
	projectChoiceB      = "22222222-2222-4222-8222-222222222222"
	projectChoiceTarget = "33333333-3333-4333-8333-333333333333"
)

type projectChoiceItem struct {
	ProjectID           string `json:"project_id"`
	Name                string `json:"name"`
	RepositoryQualifier string `json:"repository_qualifier,omitempty"`
	SelectionRef        string `json:"selection_ref"`
}

type projectChoiceReply struct {
	Result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
		Tools   []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (reply projectChoiceReply) failed() bool { return reply.Error != nil || reply.Result.IsError }

// The embedded existing fixture records actual gRPC requests, auth metadata and
// unknown mutation outcomes. This extension only supplies catalog wire data.
type projectChoiceBackend struct {
	issueRecoveryBackend
	catalog       []projectChoiceItem
	catalogStatus int
	expectedToken string
}

func (backend *projectChoiceBackend) Initialize(context.Context, *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	backend.mu.Lock()
	backend.initializes++
	backend.mu.Unlock()
	return &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{
		Name: "issues", Description: "Fixture backend issue entry", InputSchemaJson: []byte(`{"type":"object"}`),
	}}}, nil
}

func (backend *projectChoiceBackend) serve(w http.ResponseWriter, request *http.Request) {
	backend.mu.Lock()
	defer backend.mu.Unlock()
	backend.requests = append(backend.requests, issueRecoveryHTTPRequest{
		method: request.Method, path: request.URL.Path, rawQuery: request.URL.RawQuery,
		authorization: request.Header.Get("Authorization"), cookie: request.Header.Get("Cookie"),
	})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	if request.Header.Get("Authorization") != "Bearer "+backend.expectedToken {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"code":"UNAUTHENTICATED"}`))
		return
	}
	if request.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = w.Write([]byte(`{"code":"NO_REMOTE_IDENTITY_MUTATION_ALLOWED"}`))
		return
	}
	switch request.URL.Path {
	case "/api/projects":
		if request.URL.RawQuery != "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if backend.catalogStatus != http.StatusOK {
			w.WriteHeader(backend.catalogStatus)
			_, _ = w.Write([]byte(`{"code":"PERMISSION_DENIED"}`))
			return
		}
		// Do not send selection_ref: the real local entry must create it.
		items := make([]map[string]string, 0, len(backend.catalog))
		for _, item := range backend.catalog {
			items = append(items, map[string]string{"project_id": item.ProjectID, "name": item.Name, "repository_qualifier": item.RepositoryQualifier})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "unresolved": []any{}})
	case "/api/projects/resolve":
		if request.URL.RawQuery != "legacy_id="+issueRecoveryLegacyID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"status":"unresolved","reference":"urn:engram:migration:unresolved:choice-fixture","reason":"Existing membership is ambiguous"}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"UNEXPECTED_RECOVERY_ENDPOINT"}`))
	}
}

type projectChoiceFixture struct {
	disp        *dispatcher.Dispatcher
	mod         *Module
	project     muxcore.ProjectContext
	backend     *projectChoiceBackend
	home        string
	initialRoot string
}

func newProjectChoiceFixture(t *testing.T, gitRoot bool) *projectChoiceFixture {
	t.Helper()
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Setenv(key, home)
	}
	t.Setenv("ENGRAM_TLS_CA", "")
	disp, mod, project := buildContractDispatcher(t, "")
	project.Env[config.EnvWorkstationToken] = issueRecoveryToken
	project.Env[config.EnvClaudeSessionID] = issueRecoverySession
	if gitRoot {
		command := exec.CommandContext(t.Context(), "git", "-C", project.Cwd, "init", "--quiet", "--template=")
		command.Env = projectidentity.RepositoryGitEnvironmentV3()
		output, err := command.CombinedOutput()
		require.NoError(t, err, "initialize isolated Git root: %s", output)
	} else {
		command := exec.CommandContext(t.Context(), "git", "-C", project.Cwd, "rev-parse", "--is-inside-work-tree")
		command.Env = projectidentity.RepositoryGitEnvironmentV3()
		output, err := command.CombinedOutput()
		require.Error(t, err, "non-Git coverage requires a genuine non-Git root, not an inherited checkout: %s", output)
		var exitError *exec.ExitError
		require.ErrorAs(t, err, &exitError, "a missing Git executable is a harness failure, not non-Git coverage")
		require.Equal(t, 128, exitError.ExitCode())
	}
	require.NoError(t, os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), issueRecoveryMarker, 0o600))
	backend := &projectChoiceBackend{
		catalogStatus: http.StatusOK, expectedToken: issueRecoveryToken,
		catalog: []projectChoiceItem{
			{ProjectID: projectChoiceA, Name: "Current shared name", RepositoryQualifier: "example.test/team/first"},
			{ProjectID: projectChoiceB, Name: "Current shared name", RepositoryQualifier: "example.test/team/second"},
		},
	}
	grpcServer := grpc.NewServer()
	pb.RegisterEngramServiceServer(grpcServer, backend)
	server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.ProtoMajor == 2 && strings.HasPrefix(request.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, request)
			return
		}
		backend.serve(w, request)
	}), &http2.Server{}))
	project.Env[config.EnvServerURL] = server.URL
	t.Cleanup(func() { mod.pool.closeAll(); grpcServer.Stop(); server.Close() })
	return &projectChoiceFixture{disp: disp, mod: mod, project: project, backend: backend, home: home, initialRoot: project.Cwd}
}

func projectChoiceDispatch(t *testing.T, disp *dispatcher.Dispatcher, project muxcore.ProjectContext, name string, args json.RawMessage) projectChoiceReply {
	t.Helper()
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 508, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	raw, err := disp.HandleRequest(ctx, project, request)
	require.NoError(t, err)
	var reply projectChoiceReply
	require.NoError(t, json.Unmarshal(raw, &reply))
	projectChoiceAssertPrivate(t, string(raw), project.Cwd)
	if token := project.Env[config.EnvWorkstationToken]; token != "" {
		assert.NotContains(t, string(raw), token)
	}
	for _, block := range reply.Result.Content {
		projectChoiceAssertPrivate(t, block.Text, project.Cwd)
		if token := project.Env[config.EnvWorkstationToken]; token != "" {
			assert.NotContains(t, block.Text, token)
		}
	}
	return reply
}

func projectChoiceAssertPrivate(t *testing.T, output, root string) {
	t.Helper()
	for _, private := range []string{root, filepath.ToSlash(root), issueRecoveryToken, `"authorization":`, `"auth_context":`} {
		assert.NotContains(t, output, private)
	}
	encodedRoot, err := json.Marshal(root)
	require.NoError(t, err)
	assert.NotContains(t, output, string(encodedRoot[1:len(encodedRoot)-1]), "JSON escaping must not hide a leaked caller path")
}

func projectChoiceList(t *testing.T, fixture *projectChoiceFixture) []projectChoiceItem {
	t.Helper()
	reply := projectChoiceDispatch(t, fixture.disp, fixture.project, "project_context", json.RawMessage(`{"action":"choices"}`))
	require.False(t, reply.failed(), "local project_context choices must work with unresolved legacy identity: %+v", reply)
	require.Len(t, reply.Result.Content, 1)
	var payload struct {
		Choices []projectChoiceItem `json:"choices"`
	}
	require.NoError(t, json.Unmarshal([]byte(reply.Result.Content[0].Text), &payload))
	require.Len(t, payload.Choices, 2, "both existing authorized candidates must remain visible")
	for index, item := range payload.Choices {
		assert.Equal(t, fixture.backend.catalog[index].ProjectID, item.ProjectID)
		assert.Equal(t, "Current shared name", item.Name, "show the current catalog name, not the historical marker label")
		assert.Equal(t, fixture.backend.catalog[index].RepositoryQualifier, item.RepositoryQualifier)
		require.NotEmpty(t, item.SelectionRef)
		assert.NotEqual(t, item.ProjectID, item.SelectionRef, "a UUID alone is not a caller-bound choice")
		assert.NotContains(t, item.SelectionRef, fixture.project.Cwd)
		assert.NotContains(t, item.SelectionRef, issueRecoveryToken)
	}
	assert.NotEqual(t, payload.Choices[0].SelectionRef, payload.Choices[1].SelectionRef)
	return payload.Choices
}

func projectChoiceSelect(t *testing.T, fixture *projectChoiceFixture, ref string) projectChoiceReply {
	t.Helper()
	args, err := json.Marshal(map[string]string{"action": "select", "selection_ref": ref})
	require.NoError(t, err)
	return projectChoiceDispatch(t, fixture.disp, fixture.project, "project_context", args)
}

func projectChoiceSnapshot(t *testing.T, fixture *projectChoiceFixture) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, root := range []string{fixture.initialRoot, fixture.project.Cwd, fixture.mod.deps.StorageDir, fixture.home} {
		require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				result[path] = "directory"
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			result[path] = "file:" + string(raw)
			return nil
		}))
	}
	return result
}

func projectChoiceAssertNoForwards(t *testing.T, backend *projectChoiceBackend) {
	t.Helper()
	backend.mu.Lock()
	defer backend.mu.Unlock()
	assert.Empty(t, backend.calls, "choices/select/refusal must not forward an issue or a project_context tool to the backend")
}

func projectChoiceAssertReadsOnly(t *testing.T, backend *projectChoiceBackend) {
	t.Helper()
	backend.mu.Lock()
	defer backend.mu.Unlock()
	catalogReads := 0
	for _, request := range backend.requests {
		assert.Equal(t, http.MethodGet, request.method, "no project creation, migration or global binding mutation")
		assert.Contains(t, []string{"/api/projects", "/api/projects/resolve"}, request.path)
		assert.Equal(t, "Bearer "+issueRecoveryToken, request.authorization)
		assert.Empty(t, request.cookie, "host session is not a browser auth_session cookie")
		if request.path == "/api/projects" {
			catalogReads++
			assert.Empty(t, request.rawQuery)
		}
	}
	assert.Positive(t, catalogReads, "choices must use the real authenticated catalog reader")
}

func projectChoiceFreshDispatcher(t *testing.T, original *Module) (*dispatcher.Dispatcher, *Module) {
	t.Helper()
	fresh := NewModuleWithClientInstanceID(original.v3ClientInstanceID)
	require.NoError(t, fresh.Init(t.Context(), original.deps), "fresh module uses the same real persistent storage, not copied memory")
	t.Cleanup(func() { require.NoError(t, fresh.Shutdown(context.Background())) })
	reg := registry.New()
	require.NoError(t, reg.Register(fresh))
	reg.Freeze()
	return dispatcher.New(reg, slog.Default()), fresh
}

func TestProjectContextChoices_LocalToolIsDiscoverableBeforeProjectResolution(t *testing.T) {
	fixture := newProjectChoiceFixture(t, true)
	before := projectChoiceSnapshot(t, fixture)
	raw, err := fixture.disp.HandleRequest(t.Context(), fixture.project, jsonrpcListReq(1))
	require.NoError(t, err)
	var reply projectChoiceReply
	require.NoError(t, json.Unmarshal(raw, &reply))
	require.False(t, reply.failed())
	found := false
	for _, tool := range reply.Result.Tools {
		if tool.Name != "project_context" {
			continue
		}
		found = true
		assert.NotEmpty(t, tool.Description)
		assert.Contains(t, string(tool.InputSchema), "choices")
		assert.Contains(t, string(tool.InputSchema), "select")
		assert.Contains(t, string(tool.InputSchema), "selection_ref")
	}
	require.True(t, found, "tools/list must advertise local project_context even though the backend only advertises issues")
	assert.Equal(t, before, projectChoiceSnapshot(t, fixture))
	projectChoiceAssertNoForwards(t, fixture.backend)
}

func TestProjectContextChoices_SecondChoiceRetainsCallerWorkContextAndOriginalOperation(t *testing.T) {
	for _, gitRoot := range []bool{true, false} {
		name := "non-git"
		if gitRoot {
			name = "git"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newProjectChoiceFixture(t, gitRoot)
			original := json.RawMessage(`{"action":"create","title":"Keep original issue","body":"payload","target_project":"` + projectChoiceTarget + `","session_id":"reported-session-distinct-from-host","client":"original-client"}`)
			held := projectChoiceDispatch(t, fixture.disp, fixture.project, "issues", original)
			require.True(t, held.failed(), "an ambiguous legacy mapping cannot choose a project implicitly")
			projectChoiceAssertNoForwards(t, fixture.backend)
			before := projectChoiceSnapshot(t, fixture)
			choices := projectChoiceList(t, fixture)
			assert.Equal(t, before, projectChoiceSnapshot(t, fixture), "choices is read-only, including marker, common cache and local selection storage")
			require.False(t, projectChoiceSelect(t, fixture, choices[1].SelectionRef).failed(), "the user explicitly chose the second identically named project")
			projectChoiceAssertNoForwards(t, fixture.backend)
			assertIssueRecoveryPreserved(t, fixture.project.Cwd, issueRecoveryMarker)
			assert.NotEqual(t, before, projectChoiceSnapshot(t, fixture), "successful selection must persist a durable binding, not only a module-local choice")

			resumed := projectChoiceDispatch(t, fixture.disp, fixture.project, "issues", original)
			require.False(t, resumed.failed())
			fixture.backend.mu.Lock()
			calls := append([]issueRecoveryCall(nil), fixture.backend.calls...)
			fixture.backend.mu.Unlock()
			require.Len(t, calls, 1, "explicit retry dispatches the original mutation once, not once per choice")
			assert.Equal(t, "issues", calls[0].tool)
			assert.Equal(t, projectChoiceB, calls[0].project)
			assert.NotEqual(t, fixture.project.ID, calls[0].project, "authenticated caller identity and selected work project remain separate")
			assert.Equal(t, issueRecoverySession, calls[0].session)
			assert.Equal(t, []string{"Bearer " + issueRecoveryToken}, calls[0].authorization)
			assert.JSONEq(t, string(original), string(calls[0].arguments), "action, target, reported attribution and payload survive normal dispatch")
			assert.Equal(t, "contract-test-project", fixture.project.ID)
			assert.Equal(t, issueRecoveryToken, fixture.project.Env[config.EnvWorkstationToken])

			// The fresh instance shares durable storage only; it cannot retain an
			// in-memory map from the module that performed selection.
			freshDisp, fresh := projectChoiceFreshDispatcher(t, fixture.mod)
			repeated := json.RawMessage(` {"action":"reopen","id":508,"comment":"same caller after module restart"} `)
			content, err := fresh.ProxyHandleTool(t.Context(), fixture.project, "issues", repeated)
			require.NoError(t, err)
			require.NotEmpty(t, content)
			fixture.backend.mu.Lock()
			calls = append([]issueRecoveryCall(nil), fixture.backend.calls...)
			fixture.backend.mu.Unlock()
			require.Len(t, calls, 2)
			assert.Equal(t, projectChoiceB, calls[1].project)
			assert.Equal(t, []byte(repeated), calls[1].arguments, "direct proxy retains the original argument bytes")
			assert.Equal(t, issueRecoverySession, calls[1].session)
			assert.Equal(t, calls[0].authorization, calls[1].authorization)

			other := fixture.project
			other.Cwd = t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(other.Cwd, ".engram-project"), issueRecoveryMarker, 0o600))
			require.True(t, projectChoiceDispatch(t, freshDisp, other, "issues", original).failed(), "same caller ID must not switch another root")
			assertIssueRecoveryPreserved(t, other.Cwd, issueRecoveryMarker)
			fixture.backend.mu.Lock()
			assert.Len(t, fixture.backend.calls, 2)
			fixture.backend.mu.Unlock()
			assertIssueRecoveryPreserved(t, fixture.project.Cwd, issueRecoveryMarker)
			projectChoiceAssertReadsOnly(t, fixture.backend)
		})
	}
}

func TestProjectContextChoices_StaleForgedAndUnauthorizedSelectionsDoNotWrite(t *testing.T) {
	for _, scenario := range []string{"changed-marker", "changed-root", "changed-catalog", "candidate-removed", "forged-ref", "raw-project-id", "different-caller", "different-server", "unauthorized", "readonly-keycard"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newProjectChoiceFixture(t, true)
			choices := projectChoiceList(t, fixture)
			ref := choices[1].SelectionRef
			switch scenario {
			case "changed-marker":
				require.NoError(t, os.WriteFile(filepath.Join(fixture.project.Cwd, ".engram-project"), append(bytes.Clone(issueRecoveryMarker), '\n'), 0o600))
			case "changed-root":
				fixture.project.Cwd = t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(fixture.project.Cwd, ".engram-project"), issueRecoveryMarker, 0o600))
			case "changed-catalog":
				fixture.backend.catalog[1].Name = "Renamed after choices"
			case "candidate-removed":
				fixture.backend.catalog = fixture.backend.catalog[:1]
			case "forged-ref":
				ref += "forged"
			case "raw-project-id":
				ref = projectChoiceB
			case "different-caller":
				fixture.mod.v3ClientInstanceID = "different-native-client-instance"
			case "different-server":
				// A second real fixture supplies another reachable server with the
				// same catalog. A server-bound ref is not portable between them.
				other := httptest.NewServer(http.HandlerFunc(fixture.backend.serve))
				t.Cleanup(other.Close)
				fixture.project.Env[config.EnvServerURL] = other.URL
			case "unauthorized":
				fixture.project.Env[config.EnvWorkstationToken] = "synthetic-other-caller-token"
			case "readonly-keycard":
				// The existing catalog route rejects restricted/HAP callers. This
				// exercises that real 403 boundary, not an invented local role flag.
				fixture.backend.catalogStatus = http.StatusForbidden
			}
			before := projectChoiceSnapshot(t, fixture)
			require.True(t, projectChoiceSelect(t, fixture, ref).failed(), "reject stale/unbound/unauthorized selector, never silently refresh or guess")
			assert.Equal(t, before, projectChoiceSnapshot(t, fixture), "failed selection must leave all local authority unchanged")
			projectChoiceAssertNoForwards(t, fixture.backend)
		})
	}
}

func TestProjectContextChoices_InvalidSelectorsDoNotWrite(t *testing.T) {
	for _, args := range []string{
		`{"action":"select"}`, `{"action":"select","selection_ref":""}`,
		`{"action":"select","selection_ref":"   "}`, `{"action":"select","selection_ref":null}`,
		`{"action":"select","selection_ref":42}`, `{"action":"select","selection_ref":[]}`,
		`{"action":"select","project_id":"` + projectChoiceB + `"}`,
	} {
		t.Run(args, func(t *testing.T) {
			fixture := newProjectChoiceFixture(t, false)
			before := projectChoiceSnapshot(t, fixture)
			require.True(t, projectChoiceDispatch(t, fixture.disp, fixture.project, "project_context", json.RawMessage(args)).failed())
			assert.Equal(t, before, projectChoiceSnapshot(t, fixture))
			projectChoiceAssertNoForwards(t, fixture.backend)
		})
	}
}

func TestProjectContextChoices_ConflictingBindingIsPreserved(t *testing.T) {
	fixture := newProjectChoiceFixture(t, true)
	choices := projectChoiceList(t, fixture)
	require.False(t, projectChoiceSelect(t, fixture, choices[0].SelectionRef).failed())
	before := projectChoiceSnapshot(t, fixture)
	freshDisp, fresh := projectChoiceFreshDispatcher(t, fixture.mod)
	fixture.disp = freshDisp
	fixture.mod = fresh
	require.True(t, projectChoiceSelect(t, fixture, choices[1].SelectionRef).failed(), "preserve-first CAS must not replace an existing different binding")
	assert.Equal(t, before, projectChoiceSnapshot(t, fixture))
	reply := projectChoiceDispatch(t, fixture.disp, fixture.project, "issues", json.RawMessage(`{"action":"update","status":"resolved","id":508}`))
	require.False(t, reply.failed())
	fixture.backend.mu.Lock()
	defer fixture.backend.mu.Unlock()
	require.Len(t, fixture.backend.calls, 1)
	assert.Equal(t, projectChoiceA, fixture.backend.calls[0].project)
}

func TestProjectContextChoices_SelectedWorkContextDoesNotGrantAuthorization(t *testing.T) {
	fixture := newProjectChoiceFixture(t, false)
	choices := projectChoiceList(t, fixture)
	require.False(t, projectChoiceSelect(t, fixture, choices[1].SelectionRef).failed())
	fixture.backend.mutationCode = codes.PermissionDenied
	reply := projectChoiceDispatch(t, fixture.disp, fixture.project, "issues", json.RawMessage(`{"action":"update","status":"resolved","id":508}`))
	require.True(t, reply.failed(), "selection is not an authorization grant; normal backend refusal must survive")
	fixture.backend.mu.Lock()
	defer fixture.backend.mu.Unlock()
	require.Len(t, fixture.backend.calls, 1)
	assert.Equal(t, projectChoiceB, fixture.backend.calls[0].project)
	assert.Equal(t, []string{"Bearer " + issueRecoveryToken}, fixture.backend.calls[0].authorization)
}

func TestProjectContextChoices_UnknownMutationOutcomeIsNotRetried(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			fixture := newProjectChoiceFixture(t, false)
			choices := projectChoiceList(t, fixture)
			require.False(t, projectChoiceSelect(t, fixture, choices[1].SelectionRef).failed())
			fixture.backend.mutationCode = code
			reply := projectChoiceDispatch(t, fixture.disp, fixture.project, "issues", json.RawMessage(`{"action":"create","title":"Accepted before response loss","target_project":"`+projectChoiceTarget+`"}`))
			require.True(t, reply.failed(), "unknown outcome is not success")
			fixture.backend.mu.Lock()
			defer fixture.backend.mu.Unlock()
			require.Len(t, fixture.backend.calls, 1, "no automatic replay after an accepted mutation loses its response")
			assert.Equal(t, projectChoiceB, fixture.backend.calls[0].project)
			assert.Equal(t, issueRecoverySession, fixture.backend.calls[0].session)
		})
	}
}

func TestProjectContextChoices_CatalogRefusalNeverReturnsChoicesOrWrites(t *testing.T) {
	for _, statusCode := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			fixture := newProjectChoiceFixture(t, false)
			fixture.backend.catalogStatus = statusCode
			before := projectChoiceSnapshot(t, fixture)
			reply := projectChoiceDispatch(t, fixture.disp, fixture.project, "project_context", json.RawMessage(`{"action":"choices"}`))
			require.True(t, reply.failed(), "unauthorized or unavailable catalog is not a successful empty choice list")
			for _, block := range reply.Result.Content {
				assert.NotContains(t, block.Text, projectChoiceA)
				assert.NotContains(t, block.Text, projectChoiceB)
				assert.NotContains(t, block.Text, "Current shared name")
			}
			assert.Equal(t, before, projectChoiceSnapshot(t, fixture))
			projectChoiceAssertNoForwards(t, fixture.backend)
		})
	}
}

func TestProjectContextChoices_CompetingFreshModulesPublishOnlyOneBinding(t *testing.T) {
	fixture := newProjectChoiceFixture(t, false)
	choices := projectChoiceList(t, fixture)
	first, _ := projectChoiceFreshDispatcher(t, fixture.mod)
	second, _ := projectChoiceFreshDispatcher(t, fixture.mod)
	type selectionResult struct {
		index int
		raw   []byte
		err   error
	}
	results := make(chan selectionResult, 2)
	start := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for index, disp := range []*dispatcher.Dispatcher{first, second} {
		request, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": index + 1, "method": "tools/call",
			"params": map[string]any{"name": "project_context", "arguments": map[string]string{"action": "select", "selection_ref": choices[index].SelectionRef}},
		})
		require.NoError(t, err)
		go func(index int, disp *dispatcher.Dispatcher, request []byte) {
			<-start
			raw, err := disp.HandleRequest(ctx, fixture.project, request)
			results <- selectionResult{index: index, raw: raw, err: err}
		}(index, disp, request)
	}
	close(start)
	successes, winner := 0, -1
	for range 2 {
		result := <-results
		require.NoError(t, result.err)
		var reply projectChoiceReply
		require.NoError(t, json.Unmarshal(result.raw, &reply))
		if !reply.failed() {
			successes++
			winner = result.index
		}
	}
	require.Equal(t, 1, successes, "independent module instances must not both overwrite the same unbound caller context")
	projectChoiceAssertNoForwards(t, fixture.backend)
	assertIssueRecoveryPreserved(t, fixture.project.Cwd, issueRecoveryMarker)
	fresh, _ := projectChoiceFreshDispatcher(t, fixture.mod)
	require.False(t, projectChoiceDispatch(t, fresh, fixture.project, "issues", json.RawMessage(`{"action":"update","status":"resolved","id":508}`)).failed())
	fixture.backend.mu.Lock()
	defer fixture.backend.mu.Unlock()
	require.Len(t, fixture.backend.calls, 1)
	assert.Equal(t, choices[winner].ProjectID, fixture.backend.calls[0].project)
}
