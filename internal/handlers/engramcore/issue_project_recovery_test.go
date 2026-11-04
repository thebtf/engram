package engramcore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	issueRecoveryLegacyID = "941b49"
	issueRecoveryToken    = "synthetic-issue-recovery-keycard"
	issueRecoverySession  = "01a1a342-ddad-75a4-8b01-2d26be99ef88"
)

var issueRecoveryMarker = []byte("{ \"id\": \"941b49\", \"name\": \"same label $(not-authority)\" }\n")

type issueRecoveryHTTPRequest struct {
	method, path, rawQuery, authorization string
	cookie                                string
}

type issueRecoveryCall struct {
	tool, project, session string
	arguments              []byte
	authorization          []string
}

// The doubles record wire requests only. Identity resolution and the decision
// whether to forward remain in the real ProxyHandleTool entry point.
type issueRecoveryBackend struct {
	pb.UnimplementedEngramServiceServer
	mu           sync.Mutex
	requests     []issueRecoveryHTTPRequest
	calls        []issueRecoveryCall
	initializes  int
	lookupStatus int
	lookupBody   string
	mutationCode codes.Code
	authDisabled bool
}

func (backend *issueRecoveryBackend) Initialize(context.Context, *pb.InitializeRequest) (*pb.InitializeResponse, error) {
	backend.mu.Lock()
	backend.initializes++
	backend.mu.Unlock()
	return &pb.InitializeResponse{}, nil
}

func (backend *issueRecoveryBackend) CallTool(ctx context.Context, request *pb.CallToolRequest) (*pb.CallToolResponse, error) {
	incoming, _ := metadata.FromIncomingContext(ctx)
	backend.mu.Lock()
	backend.calls = append(backend.calls, issueRecoveryCall{
		tool: request.GetToolName(), project: request.GetProject(), session: request.GetSessionId(),
		arguments:     bytes.Clone(request.GetArgumentsJson()),
		authorization: append([]string(nil), incoming.Get("authorization")...),
	})
	code := backend.mutationCode
	backend.mu.Unlock()
	// Recording the accepted mutation before losing its response makes replay
	// observable without sleeps or a second product implementation.
	if code != codes.OK {
		return nil, status.Error(code, "synthetic response lost after issue mutation; outcome unknown")
	}
	return &pb.CallToolResponse{ContentJson: []byte(`{"issue_id":508,"status":"resolved"}`)}, nil
}

func (backend *issueRecoveryBackend) serveLookup(w http.ResponseWriter, request *http.Request) {
	backend.mu.Lock()
	backend.requests = append(backend.requests, issueRecoveryHTTPRequest{
		method: request.Method, path: request.URL.Path, rawQuery: request.URL.RawQuery,
		authorization: request.Header.Get("Authorization"),
		cookie:        request.Header.Get("Cookie"),
	})
	code, body := backend.lookupStatus, backend.lookupBody
	backend.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if request.Method != http.MethodGet || request.URL.Path != "/api/projects/resolve" ||
		request.URL.RawQuery != "legacy_id="+issueRecoveryLegacyID {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"UNEXPECTED_IDENTITY_OPERATION"}`))
		return
	}
	wantAuthorization := "Bearer " + issueRecoveryToken
	if backend.authDisabled {
		wantAuthorization = ""
	}
	if request.Header.Get("Authorization") != wantAuthorization {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"PERMISSION_DENIED"}`))
		return
	}
	w.WriteHeader(code)
	_, _ = w.Write([]byte(body))
}

func newIssueRecoveryFixture(t *testing.T, backend *issueRecoveryBackend, marker []byte) (*Module, muxcore.ProjectContext) {
	t.Helper()
	t.Setenv("ENGRAM_TLS_CA", "")
	_, mod, project := buildContractDispatcher(t, "")
	project.Env[config.EnvWorkstationToken] = issueRecoveryToken
	project.Env[config.EnvClaudeSessionID] = issueRecoverySession
	if backend.authDisabled {
		t.Setenv(config.EnvWorkstationToken, "")
		project.Env[config.EnvWorkstationToken] = ""
	}
	command := exec.CommandContext(t.Context(), "git", "-C", project.Cwd, "init", "--quiet", "--template=")
	command.Env = projectidentity.RepositoryGitEnvironmentV3()
	output, err := command.CombinedOutput()
	require.NoError(t, err, "initialize isolated caller repository: %s", output)
	require.NoError(t, os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), marker, 0o600))

	// One loopback origin serves the actual HTTP reader and gRPC proxy dial;
	// no pool alias, global transport replacement or fake resolver is installed.
	grpcServer := grpc.NewServer()
	pb.RegisterEngramServiceServer(grpcServer, backend)
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.ProtoMajor == 2 && strings.HasPrefix(request.Header.Get("Content-Type"), "application/grpc") {
			grpcServer.ServeHTTP(w, request)
			return
		}
		backend.serveLookup(w, request)
	})
	server := httptest.NewServer(h2c.NewHandler(handler, &http2.Server{}))
	project.Env[config.EnvServerURL] = server.URL
	t.Cleanup(func() {
		mod.pool.closeAll()
		grpcServer.Stop()
		server.Close()
	})
	return mod, project
}

func assertIssueRecoveryPreserved(t *testing.T, root string, marker []byte) {
	t.Helper()
	actual, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	require.NoError(t, err)
	assert.Equal(t, marker, actual, "recovery must preserve the historical marker, including its original bytes")
}

func assertIssueRecoveryRequests(t *testing.T, backend *issueRecoveryBackend, reads, forwards int) {
	t.Helper()
	backend.mu.Lock()
	defer backend.mu.Unlock()
	assert.Len(t, backend.requests, reads, "one bounded legacy lookup; no bootstrap, project/alias creation, name lookup or hidden retry")
	wantAuthorization := "Bearer " + issueRecoveryToken
	if backend.authDisabled {
		wantAuthorization = ""
	}
	for _, request := range backend.requests {
		assert.Equal(t, http.MethodGet, request.method)
		assert.Equal(t, "/api/projects/resolve", request.path)
		assert.Equal(t, "legacy_id="+issueRecoveryLegacyID, request.rawQuery, "only the exact validated marker ID is selector evidence")
		assert.Equal(t, wantAuthorization, request.authorization)
		assert.Empty(t, request.cookie, "a native host session must not be fabricated as an auth_session browser cookie")
	}
	assert.Len(t, backend.calls, forwards, "the original mutation must be forwarded exactly once, or not at all on refusal")
	assert.Zero(t, backend.initializes, "recovery must not substitute catalog/session initialization for the held operation")
}

func TestIssueProjectRecovery_OriginalOperationResumesExactlyOnce(t *testing.T) {
	for _, operation := range []struct {
		name, arguments string
	}{
		{"create", ` {"action":"create","title":"Retain the original issue","body":"operation payload","target_project":"33333333-3333-4333-8333-333333333333"} `},
		{"resolve", `{"action":"update","status":"resolved","id":508,"comment":"original resolution"}`},
		{"reopen", `{"action":"reopen","id":508,"comment":"original reopen"}`},
	} {
		t.Run(operation.name, func(t *testing.T) {
			backend := &issueRecoveryBackend{
				lookupStatus: http.StatusOK,
				lookupBody:   `{"status":"resolved","project_id":"` + contractProjectID + `","name":"different server label"}`,
			}
			mod, project := newIssueRecoveryFixture(t, backend, issueRecoveryMarker)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			content, err := mod.ProxyHandleTool(ctx, project, "issues", json.RawMessage(operation.arguments))
			assertIssueRecoveryPreserved(t, project.Cwd, issueRecoveryMarker)
			assertIssueRecoveryRequests(t, backend, 1, 1)
			require.NoError(t, err, "an existing verified server mapping must recover inside this original ProxyHandleTool call")
			var block struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			require.NoError(t, json.Unmarshal(content, &block))
			assert.Equal(t, "text", block.Type)
			assert.JSONEq(t, `{"issue_id":508,"status":"resolved"}`, block.Text)
			backend.mu.Lock()
			calls := append([]issueRecoveryCall(nil), backend.calls...)
			backend.mu.Unlock()
			require.Len(t, calls, 1)
			assert.Equal(t, "issues", calls[0].tool)
			assert.Equal(t, contractProjectID, calls[0].project, "canonical identity comes only from the authenticated resolved mapping")
			assert.Equal(t, issueRecoverySession, calls[0].session, "retain the supplied host session, not a project or generated identity")
			assert.Equal(t, []byte(operation.arguments), calls[0].arguments, "forward original arguments byte-for-byte, including explicit target")
			assert.Equal(t, []string{"Bearer " + issueRecoveryToken}, calls[0].authorization)
		})
	}
}

func TestIssueProjectRecovery_UnsafeLookupNeverForwardsOrMutatesIdentity(t *testing.T) {
	for _, scenario := range []struct {
		name, body string
		status     int
	}{
		{"unknown", `{"status":"unresolved","reference":"retained-reference","reason":"Historical membership remains unresolved"}`, http.StatusOK},
		{"ambiguous", `{"status":"unresolved","reference":"ambiguous-reference","reason":"Historical membership remains unresolved"}`, http.StatusOK},
		{"conflicting", `{"code":"PROJECT_IDENTITY_CONFLICT"}`, http.StatusConflict},
		{"invalid-canonical-id", `{"status":"resolved","project_id":"941b49","name":"same label $(not-authority)"}`, http.StatusOK},
		{"nil-canonical-id", `{"status":"resolved","project_id":"00000000-0000-0000-0000-000000000000"}`, http.StatusOK},
		{"missing-canonical-id", `{"status":"resolved","name":"same label $(not-authority)"}`, http.StatusOK},
		{"unresolved-with-candidate-id", `{"status":"unresolved","project_id":"` + contractProjectID + `","reference":"retained-reference"}`, http.StatusOK},
		{"unverified-status-with-id", `{"status":"migration_required","project_id":"` + contractProjectID + `"}`, http.StatusOK},
		{"conflicting-duplicate-id", `{"status":"resolved","project_id":"` + contractProjectID + `","project_id":"` + otherContractProjectID + `"}`, http.StatusOK},
		{"trailing-document", `{"status":"resolved","project_id":"` + contractProjectID + `"}{"status":"unresolved"}`, http.StatusOK},
		{"malformed", `{"status":`, http.StatusOK},
		{"denied", `{"code":"PERMISSION_DENIED"}`, http.StatusForbidden},
		{"unavailable", `{"code":"PROJECT_RESOLUTION_UNAVAILABLE"}`, http.StatusServiceUnavailable},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			backend := &issueRecoveryBackend{lookupStatus: scenario.status, lookupBody: scenario.body}
			mod, project := newIssueRecoveryFixture(t, backend, issueRecoveryMarker)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			content, err := mod.ProxyHandleTool(ctx, project, "issues", json.RawMessage(`{"action":"update","status":"resolved","id":508,"comment":"do not lose this operation"}`))
			assertIssueRecoveryPreserved(t, project.Cwd, issueRecoveryMarker)
			assertIssueRecoveryRequests(t, backend, 1, 0)
			require.Error(t, err)
			assert.Empty(t, content, "a held operation must not be reported as success")
			var refusal *module.ModuleError
			require.ErrorAs(t, err, &refusal, "return an actionable native refusal, not a raw parse or transport failure")
			assert.NotEmpty(t, refusal.Code)
			assert.True(t, strings.Contains(refusal.Message, "/api/projects/") ||
				strings.Contains(refusal.Message, "project resolve") ||
				strings.Contains(strings.ToLower(refusal.Message), "permission"), "name a supported recovery/permission action: %s", refusal.Message)
			assert.NotContains(t, refusal.Message, "$(not-authority)", "untrusted display name is not recovery authority or executable advice")
			_, statErr := os.Lstat(filepath.Join(project.Cwd, ".git", "engram-project.json"))
			assert.True(t, errors.Is(statErr, os.ErrNotExist), "refusal must not pin a guessed identity in the common cache")
		})
	}
}

func TestIssueProjectRecovery_InvalidMarkerNeverConsultsServer(t *testing.T) {
	for _, raw := range []string{
		`{"id":"941B49","name":"untrusted label"}`,
		`{"id":"941b49&name=other","name":"untrusted label"}`,
		`{"id":"941b49","name":null}`,
		`{"id":"941b49","name":"untrusted label","project_id":"` + contractProjectID + `"}`,
		`{"id":"941b49","id":"abcdef","name":"untrusted label"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			backend := &issueRecoveryBackend{lookupStatus: http.StatusOK, lookupBody: `{"status":"resolved","project_id":"` + contractProjectID + `"}`}
			mod, project := newIssueRecoveryFixture(t, backend, []byte(raw))
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			content, err := mod.ProxyHandleTool(ctx, project, "issues", json.RawMessage(`{"action":"update","status":"resolved","id":508}`))
			assertIssueRecoveryPreserved(t, project.Cwd, []byte(raw))
			assertIssueRecoveryRequests(t, backend, 0, 0)
			require.Error(t, err)
			assert.Empty(t, content)
			var refusal *module.ModuleError
			require.ErrorAs(t, err, &refusal)
			assert.Equal(t, "PROJECT_IDENTITY_INVALID", refusal.Code)
			assert.Contains(t, refusal.Message, "Preserve")
			assert.Contains(t, refusal.Message, "4096", "the next action must be bounded inspection, not initialization")
		})
	}
}

func TestIssueProjectRecovery_PostMutationResponseLossNeverReplays(t *testing.T) {
	for _, code := range []codes.Code{codes.DeadlineExceeded, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			backend := &issueRecoveryBackend{
				lookupStatus: http.StatusOK,
				lookupBody:   `{"status":"resolved","project_id":"` + contractProjectID + `","name":"different server label"}`,
				mutationCode: code,
			}
			mod, project := newIssueRecoveryFixture(t, backend, issueRecoveryMarker)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			content, err := mod.ProxyHandleTool(ctx, project, "issues", json.RawMessage(`{"action":"create","title":"accepted once despite response loss"}`))
			assertIssueRecoveryPreserved(t, project.Cwd, issueRecoveryMarker)
			assertIssueRecoveryRequests(t, backend, 1, 1)
			require.Error(t, err, "response loss after acceptance is not success and must not be retried")
			assert.Empty(t, content)
			assert.Equal(t, code, status.Code(err), "retain the real gRPC unknown-outcome failure instead of converting it to migration recovery")
		})
	}
}

func TestIssueProjectRecovery_AuthDisabledMappingResumesWithoutFabricatedCredential(t *testing.T) {
	backend := &issueRecoveryBackend{
		lookupStatus: http.StatusOK,
		lookupBody:   `{"status":"resolved","project_id":"` + contractProjectID + `","name":"existing server project"}`,
		authDisabled: true,
	}
	mod, project := newIssueRecoveryFixture(t, backend, issueRecoveryMarker)
	arguments := json.RawMessage(`{"action":"update","status":"resolved","id":508,"comment":"existing mapping without a keycard"}`)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	content, err := mod.ProxyHandleTool(ctx, project, "issues", arguments)
	assertIssueRecoveryPreserved(t, project.Cwd, issueRecoveryMarker)
	assertIssueRecoveryRequests(t, backend, 1, 1)
	require.NoError(t, err, "an auth-disabled reader may verify an existing mapping without manufacturing a keycard")
	require.NotEmpty(t, content)
	backend.mu.Lock()
	calls := append([]issueRecoveryCall(nil), backend.calls...)
	backend.mu.Unlock()
	require.Len(t, calls, 1)
	assert.Equal(t, "issues", calls[0].tool)
	assert.Equal(t, contractProjectID, calls[0].project)
	assert.Equal(t, issueRecoverySession, calls[0].session)
	assert.Equal(t, []byte(arguments), calls[0].arguments)
	assert.Empty(t, calls[0].authorization, "the resumed operation must not fabricate a gRPC credential either")
}

func TestIssueProjectRecovery_MissingConnectionSessionNeverBorrowsDaemonSession(t *testing.T) {
	t.Setenv(config.EnvClaudeSessionID, "stale-daemon-process-session")
	backend := &issueRecoveryBackend{
		lookupStatus: http.StatusOK,
		lookupBody:   `{"status":"resolved","project_id":"` + contractProjectID + `","name":"existing server project"}`,
	}
	mod, project := newIssueRecoveryFixture(t, backend, issueRecoveryMarker)
	delete(project.Env, config.EnvClaudeSessionID)
	arguments := json.RawMessage(` {"action":"update","status":"resolved","id":508,"session_id":"explicit-reported-session","comment":"preserve reported attribution separately"} `)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	content, err := mod.ProxyHandleTool(ctx, project, "issues", arguments)
	assertIssueRecoveryPreserved(t, project.Cwd, issueRecoveryMarker)
	assertIssueRecoveryRequests(t, backend, 1, 1)
	require.NoError(t, err)
	require.NotEmpty(t, content)
	backend.mu.Lock()
	calls := append([]issueRecoveryCall(nil), backend.calls...)
	backend.mu.Unlock()
	require.Len(t, calls, 1)
	assert.Equal(t, "issues", calls[0].tool)
	assert.Equal(t, contractProjectID, calls[0].project)
	assert.Empty(t, calls[0].session, "neither daemon process identity nor a reported argument is the missing host connection session")
	assert.Equal(t, []byte(arguments), calls[0].arguments, "explicit reported session_id remains original argument data")
	assert.Equal(t, []string{"Bearer " + issueRecoveryToken}, calls[0].authorization)
}
