package engramcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io/fs"
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
	"github.com/thebtf/engram/internal/module/dispatcher"
	"github.com/thebtf/engram/internal/projectidentity"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
)

func identityRaceCleanupWitness(t *testing.T) *[]string {
	t.Helper()
	paths := new([]string)
	t.Cleanup(func() {
		for _, path := range *paths {
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Errorf("test-owned temporary root remains after cleanup: %v", err)
			}
		}
		t.Logf("cleanup: checked removal of %d test-owned temporary roots", len(*paths))
	})
	return paths
}

func identityRaceFiles(t *testing.T, root string) map[string][sha256.Size]byte {
	t.Helper()
	files := make(map[string][sha256.Size]byte)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err == nil {
			relative, relativeErr := filepath.Rel(root, path)
			if relativeErr != nil {
				return relativeErr
			}
			files[relative] = sha256.Sum256(raw)
		}
		return err
	}))
	return files
}

func identityRacePreserved(t *testing.T, root string, before map[string][sha256.Size]byte) {
	t.Helper()
	after := identityRaceFiles(t, root)
	for path, digest := range before {
		actual, present := after[path]
		assert.True(t, present && actual == digest, "original fixture file changed: %s", path)
	}
	for path := range after {
		assert.False(t, strings.HasSuffix(path, ".lock"), "selection must release its local lock")
		assert.False(t, strings.HasPrefix(filepath.Base(path), ".engram-context-"), "selection must remove temporary publication files")
	}
	_, err := os.Lstat(filepath.Join(root, ".git", "engram-project.json"))
	assert.True(t, os.IsNotExist(err), "recovery must not publish a canonical common cache")
	t.Logf("preservation: original_files=%d current_files=%d legacy_marker_sha256=%x", len(before), len(after), sha256.Sum256(issueRecoveryMarker))
}

func identityRaceDispatch(ctx context.Context, disp *dispatcher.Dispatcher, project muxcore.ProjectContext, tool string, args json.RawMessage) (projectChoiceReply, error) {
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 508, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	if err != nil {
		return projectChoiceReply{}, err
	}
	raw, err := disp.HandleRequest(ctx, project, request)
	if err != nil {
		return projectChoiceReply{}, err
	}
	var reply projectChoiceReply
	err = json.Unmarshal(raw, &reply)
	return reply, err
}

func identityRaceCall(t *testing.T, disp *dispatcher.Dispatcher, project muxcore.ProjectContext, tool string, args json.RawMessage) projectChoiceReply {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	reply, err := identityRaceDispatch(ctx, disp, project, tool, args)
	require.NoError(t, err)
	return reply
}

func identityRaceChoices(t *testing.T, disp *dispatcher.Dispatcher, project muxcore.ProjectContext) []projectChoiceItem {
	t.Helper()
	reply := identityRaceCall(t, disp, project, "project_context", json.RawMessage(`{"action":"choices"}`))
	require.False(t, reply.failed(), "authorized choices must remain available")
	require.Len(t, reply.Result.Content, 1)
	var payload struct {
		Choices []projectChoiceItem `json:"choices"`
	}
	require.NoError(t, json.Unmarshal([]byte(reply.Result.Content[0].Text), &payload))
	require.Len(t, payload.Choices, 2)
	for index, projectID := range []string{projectChoiceA, projectChoiceB} {
		assert.Equal(t, projectID, payload.Choices[index].ProjectID)
		assert.Equal(t, "Current shared name", payload.Choices[index].Name)
		require.NotEmpty(t, payload.Choices[index].SelectionRef)
	}
	assert.NotEqual(t, payload.Choices[0].RepositoryQualifier, payload.Choices[1].RepositoryQualifier)
	return payload.Choices
}

func identityRaceSelect(t *testing.T, disp *dispatcher.Dispatcher, project muxcore.ProjectContext, reference string) projectChoiceReply {
	t.Helper()
	args, err := json.Marshal(map[string]string{"action": "select", "selection_ref": reference})
	require.NoError(t, err)
	return identityRaceCall(t, disp, project, "project_context", args)
}

func identityRaceAssertWire(t *testing.T, backend *projectChoiceBackend) []issueRecoveryCall {
	t.Helper()
	backend.mu.Lock()
	defer backend.mu.Unlock()
	for _, request := range backend.requests {
		assert.Equal(t, http.MethodGet, request.method, "identity recovery must not mutate the backend")
		assert.Contains(t, []string{"/api/projects", "/api/projects/resolve"}, request.path)
		assert.True(t, request.authorization == "Bearer "+issueRecoveryToken, "HTTP must retain only the synthetic keycard")
		assert.Empty(t, request.cookie, "a host session is not a browser credential")
		if request.path == "/api/projects/resolve" {
			assert.Equal(t, "legacy_id="+issueRecoveryLegacyID, request.rawQuery)
		} else {
			assert.Empty(t, request.rawQuery)
		}
	}
	for _, call := range backend.calls {
		assert.Equal(t, "issues", call.tool, "local recovery actions must not be forwarded")
		assert.True(t, len(call.authorization) == 1 && call.authorization[0] == "Bearer "+issueRecoveryToken, "gRPC must retain only the synthetic keycard")
		assert.Empty(t, call.session, "missing actual host session must not borrow daemon or reported session identity")
	}
	assert.Zero(t, backend.initializes, "recovery must not initialize an inferred backend session")
	t.Logf("wire: identity_reads=%d original_operations=%d initializes=%d", len(backend.requests), len(backend.calls), backend.initializes)
	return append([]issueRecoveryCall(nil), backend.calls...)
}

func TestProjectRecoveryIdentity_IndependentInstallationsKeepSeparateChoices(t *testing.T) {
	cleanup := identityRaceCleanupWitness(t)
	fixture := newProjectChoiceFixture(t, true)
	*cleanup = append(*cleanup, fixture.home, fixture.project.Cwd, fixture.mod.deps.StorageDir)
	t.Setenv(config.EnvClaudeSessionID, "synthetic-daemon-session-must-not-be-borrowed")
	delete(fixture.project.Env, config.EnvClaudeSessionID)
	dispA, modA, projectA := buildContractDispatcherWithClientInstanceID(t, "", "11111111-aaaa-4111-8111-111111111111")
	dispB, modB, projectB := buildContractDispatcherWithClientInstanceID(t, "", "22222222-bbbb-4222-8222-222222222222")
	*cleanup = append(*cleanup, modA.deps.StorageDir, modB.deps.StorageDir, projectA.Cwd, projectB.Cwd)
	require.NotEmpty(t, modA.v3ClientInstanceID)
	require.NotEqual(t, modA.v3ClientInstanceID, modB.v3ClientInstanceID)
	// Both installations receive the identical muxcore worktree context. Its ID
	// cannot distinguish the installations and supplies no principal authority.
	project := fixture.project
	before := identityRaceFiles(t, project.Cwd)
	originals := []json.RawMessage{
		json.RawMessage(`{"action":"create","title":"installation A original","target_project":"` + projectChoiceTarget + `","session_id":"reported-A","client":"client-A"}`),
		json.RawMessage(`{"action":"reopen","id":508,"comment":"installation B original","session_id":"reported-B","client":"client-B"}`),
	}
	for _, disp := range []*dispatcher.Dispatcher{dispA, dispB} {
		assert.True(t, identityRaceCall(t, disp, project, "issues", originals[0]).failed(), "ambiguous legacy identity must hold the original operation")
	}
	fixture.backend.mu.Lock()
	initialForwards := len(fixture.backend.calls)
	fixture.backend.mu.Unlock()
	require.Zero(t, initialForwards, "ambiguous identity must not send a mutation")
	choicesA := identityRaceChoices(t, dispA, project)
	choicesB := identityRaceChoices(t, dispB, project)
	assert.NotEqual(t, choicesA[0].SelectionRef, choicesB[0].SelectionRef, "selection references must bind the existing installation ID, not only a shared worktree hash")
	require.False(t, identityRaceSelect(t, dispA, project, choicesA[0].SelectionRef).failed())
	// B re-lists after A commits so a same-caller stale-reference refusal
	// cannot masquerade as independent installation isolation.
	choicesB = identityRaceChoices(t, dispB, project)
	selectedB := identityRaceSelect(t, dispB, project, choicesB[1].SelectionRef)
	assert.False(t, selectedB.failed(), "installation B must independently select its authorized existing project")
	for index, disp := range []*dispatcher.Dispatcher{dispA, dispB} {
		assert.False(t, identityRaceCall(t, disp, project, "issues", originals[index]).failed())
	}
	freshA, _ := projectChoiceFreshDispatcher(t, modA)
	freshB, _ := projectChoiceFreshDispatcher(t, modB)
	for index, disp := range []*dispatcher.Dispatcher{freshA, freshB} {
		assert.False(t, identityRaceCall(t, disp, project, "issues", originals[index]).failed())
	}
	calls := identityRaceAssertWire(t, fixture.backend)
	require.Equal(t, 4, len(calls), "two explicit operations plus two fresh-module operations, without automatic replay")
	for index, call := range calls {
		assert.Equal(t, []string{projectChoiceA, projectChoiceB}[index%2], call.project, "an installation must never route through the other installation's choice")
		assert.JSONEq(t, string(originals[index%2]), string(call.arguments))
	}
	bindings, err := filepath.Glob(filepath.Join(project.Cwd, ".engram-context.*.json"))
	require.NoError(t, err)
	assert.Len(t, bindings, 2, "independent installed clients need independent durable caller bindings")
	identityRacePreserved(t, project.Cwd, before)
}

func TestProjectRecoveryIdentity_InFlightLookupRejectsChangedCallerEvidence(t *testing.T) {
	for _, change := range []string{"nearer-marker", "selected-caller-binding"} {
		t.Run(change, func(t *testing.T) {
			cleanup := identityRaceCleanupWitness(t)
			home := t.TempDir()
			for _, key := range []string{"HOME", "USERPROFILE", "XDG_CONFIG_HOME", "APPDATA", "LOCALAPPDATA"} {
				t.Setenv(key, home)
			}
			t.Setenv("ENGRAM_TLS_CA", "")
			t.Setenv(config.EnvClaudeSessionID, "synthetic-daemon-session-must-not-be-borrowed")
			disp, mod, project := buildContractDispatcherWithClientInstanceID(t, "", "33333333-cccc-4333-8333-333333333333")
			root := project.Cwd
			*cleanup = append(*cleanup, home, root, mod.deps.StorageDir)
			command := exec.CommandContext(t.Context(), "git", "-C", root, "init", "--quiet", "--template=")
			command.Env = projectidentity.RepositoryGitEnvironmentV3()
			output, err := command.CombinedOutput()
			require.NoError(t, err, "initialize isolated caller repository: %s", output)
			require.NoError(t, os.WriteFile(filepath.Join(root, ".engram-project"), issueRecoveryMarker, 0o600))
			project.Cwd = filepath.Join(root, "nested", "caller")
			require.NoError(t, os.MkdirAll(project.Cwd, 0o700))
			project.Env[config.EnvWorkstationToken] = issueRecoveryToken
			before := identityRaceFiles(t, root)
			backend := &projectChoiceBackend{catalogStatus: http.StatusOK, expectedToken: issueRecoveryToken, catalog: []projectChoiceItem{
				{ProjectID: projectChoiceA, Name: "Current shared name", RepositoryQualifier: "example.test/team/first"},
				{ProjectID: projectChoiceB, Name: "Current shared name", RepositoryQualifier: "example.test/team/second"},
			}}
			started, release := make(chan struct{}), make(chan struct{})
			var startOnce, releaseOnce sync.Once
			grpcServer := grpc.NewServer()
			pb.RegisterEngramServiceServer(grpcServer, backend)
			server := httptest.NewServer(h2c.NewHandler(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.ProtoMajor == 2 && strings.HasPrefix(request.Header.Get("Content-Type"), "application/grpc") {
					grpcServer.ServeHTTP(w, request)
					return
				}
				if request.URL.Path != "/api/projects/resolve" {
					backend.serve(w, request)
					return
				}
				backend.mu.Lock()
				backend.requests = append(backend.requests, issueRecoveryHTTPRequest{method: request.Method, path: request.URL.Path, rawQuery: request.URL.RawQuery, authorization: request.Header.Get("Authorization"), cookie: request.Header.Get("Cookie")})
				backend.mu.Unlock()
				if request.Method != http.MethodGet || request.URL.RawQuery != "legacy_id="+issueRecoveryLegacyID || request.Header.Get("Authorization") != "Bearer "+issueRecoveryToken || request.Header.Get("Cookie") != "" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				startOnce.Do(func() { close(started) })
				select {
				case <-release:
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"status":"resolved","project_id":"` + projectChoiceA + `","name":"old mapping"}`))
				case <-request.Context().Done():
				}
			}), &http2.Server{}))
			project.Env[config.EnvServerURL] = server.URL
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				mod.pool.closeAll()
				grpcServer.Stop()
				server.Close()
			})
			original := json.RawMessage(`{"action":"create","title":"held before identity changed","target_project":"` + projectChoiceTarget + `","session_id":"reported-not-authenticated","client":"original-client"}`)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			type result struct {
				reply projectChoiceReply
				err   error
			}
			done := make(chan result, 1)
			go func() {
				reply, err := identityRaceDispatch(ctx, disp, project, "issues", original)
				done <- result{reply: reply, err: err}
			}()
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("real legacy HTTP lookup did not reach the channel barrier")
			}
			var nearerMarker []byte
			if change == "nearer-marker" {
				nearerMarker = []byte("{\"project_id\":\"" + projectChoiceB + "\",\"name\":\"nearer explicit project\"}\n")
				require.NoError(t, os.WriteFile(filepath.Join(project.Cwd, ".engram-project"), nearerMarker, 0o600))
			} else {
				choices := identityRaceChoices(t, disp, project)
				require.False(t, identityRaceSelect(t, disp, project, choices[1].SelectionRef).failed())
			}
			atChange := identityRaceFiles(t, root)
			releaseOnce.Do(func() { close(release) })
			var held result
			select {
			case held = <-done:
			case <-ctx.Done():
				t.Fatal("held dispatcher operation did not complete after HTTP release")
			}
			require.NoError(t, held.err, "dispatcher must return a proper refusal envelope, not a harness error")
			assert.True(t, held.reply.failed(), "changed caller evidence must refuse the held operation before forwarding, not use the old HTTP mapping")
			refusalText := ""
			if held.reply.Error != nil {
				refusalText = held.reply.Error.Message
			}
			for _, content := range held.reply.Result.Content {
				refusalText += content.Text
			}
			assert.True(t, strings.Contains(refusalText, "PROJECT_SELECTION_STALE") || strings.Contains(refusalText, "PROJECT_IDENTITY_CONFLICT"), "refusal must identify stale or conflicting caller identity")
			backend.mu.Lock()
			heldCalls := append([]issueRecoveryCall(nil), backend.calls...)
			backend.mu.Unlock()
			assert.Zero(t, len(heldCalls), "identity refusal and mutation transport are separate facts: no held mutation may have been sent")
			assert.Equal(t, atChange, identityRaceFiles(t, root), "finishing the old HTTP lookup must not overwrite new caller evidence")
			followup := json.RawMessage(`{"action":"reopen","id":509,"comment":"new explicit operation after identity change"}`)
			assert.False(t, identityRaceCall(t, disp, project, "issues", followup).failed(), "the new caller evidence must be usable, not a malformed fixture")
			calls := identityRaceAssertWire(t, backend)
			stale, heldCount, followupCount, lookups := 0, 0, 0, 0
			for _, call := range calls {
				if call.project == projectChoiceA {
					stale++
				}
				if bytes.Equal(call.arguments, original) {
					heldCount++
				}
				if bytes.Equal(call.arguments, followup) {
					followupCount++
					assert.Equal(t, projectChoiceB, call.project)
				}
			}
			backend.mu.Lock()
			for _, request := range backend.requests {
				if request.path == "/api/projects/resolve" {
					lookups++
				}
			}
			backend.mu.Unlock()
			assert.Zero(t, stale, "the previously returned mapping is stale")
			assert.Zero(t, heldCount, "the held operation was not sent, even once")
			assert.Equal(t, 1, followupCount, "the separately issued follow-up is not duplicated")
			assert.Equal(t, 1, lookups, "one real in-flight legacy lookup, with no hidden retry")
			assert.Equal(t, 1, len(calls), "only the separately issued follow-up may mutate the backend")
			identityRacePreserved(t, root, before)
			if nearerMarker != nil {
				actual, err := os.ReadFile(filepath.Join(project.Cwd, ".engram-project"))
				require.NoError(t, err)
				assert.True(t, bytes.Equal(nearerMarker, actual), "new nearer marker bytes must survive")
			}
			t.Logf("race: identity_refusal=%t held_mutations=%d stale_mapping_mutations=%d followup_mutations=%d legacy_lookups=%d original_sha256=%x", held.reply.failed(), heldCount, stale, followupCount, lookups, sha256.Sum256(original))
		})
	}
}
