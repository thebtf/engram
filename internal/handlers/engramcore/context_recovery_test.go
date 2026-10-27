package engramcore

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

func TestContextRecoveryNativePreflightNeverBootstrapsOrPublishesIdentity(t *testing.T) {
	for _, scenario := range []string{"new-unpinned-repository", "existing-migration-hold"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				require.Equal(t, "/api/projects/bootstrap", r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				if scenario == "existing-migration-hold" {
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"code":"PROJECT_MIGRATION_REQUIRED"}`))
					return
				}
				_, _ = w.Write([]byte(`{"project_id":"11111111-1111-4111-8111-111111111111","name":"Must not be published by inspection"}`))
			}))
			defer backend.Close()
			_, mod, project := buildContractDispatcher(t, "")
			project.Env["ENGRAM_URL"] = backend.URL
			for _, args := range [][]string{{"init", "--quiet", "--template="}, {"remote", "add", "origin", "https://example.test/Recovery/Preflight.git"}} {
				command := exec.CommandContext(t.Context(), "git", append([]string{"-C", project.Cwd}, args...)...)
				command.Env = projectidentity.RepositoryGitEnvironmentV3()
				raw, err := command.CombinedOutput()
				require.NoError(t, err, "synthetic Git fixture: %s", raw)
			}
			paths := []string{filepath.Join(project.Cwd, ".engram-project"), filepath.Join(project.Cwd, ".git", "engram-project.json")}
			var existing []byte
			if scenario == "existing-migration-hold" {
				existing = []byte("{ \"name\": \"Keep original legacy label\" }\n")
				for _, path := range paths {
					require.NoError(t, os.WriteFile(path, existing, 0o600))
				}
			}
			ctx := auditcontext.WithUCITransportSession(t.Context(), "recovery-preflight-session")
			for _, operation := range []struct{ tool, arguments string }{
				{"codebase_context", `{"action":"list"}`},
				{"codebase_context", `{"action":"resolve"}`},
				{"codebase_context", `{"action":"select","context_handle":"opaque-from-fresh-list"}`},
				{"issues", `{"action":"list"}`},
				{"issues", `{"action":"get","id":508}`},
			} {
				request := &pb.CallToolRequest{ToolName: operation.tool, ArgumentsJson: json.RawMessage(operation.arguments)}
				_, err := mod.proxyToolCallContext(ctx, project, request, isUCIProxyTool(operation.tool))
				require.NoError(t, err)
				require.Empty(t, request.Project, "project-free reads must not acquire canonical authority")
			}
			require.Zero(t, calls.Load(), "native guard must not bootstrap just to inspect or select authorized code contexts/read issues")
			if scenario == "existing-migration-hold" {
				request := &pb.CallToolRequest{ToolName: "issues", ArgumentsJson: json.RawMessage(`{"action":"create","project":"22222222-2222-4222-8222-222222222222"}`)}
				_, err := mod.proxyToolCallContext(ctx, project, request, false)
				var refusal *module.ModuleError
				require.ErrorAs(t, err, &refusal)
				require.Equal(t, "PROJECT_MIGRATION_REQUIRED", refusal.Code)
				require.Empty(t, request.Project, "caller project argument cannot bypass migration hold")
				require.EqualValues(t, 1, calls.Load(), "write control proves that the real bootstrap seam was reachable")
			}
			for _, path := range paths {
				actual, err := os.ReadFile(path)
				if existing == nil {
					require.True(t, os.IsNotExist(err), "new repository inspection must not publish marker/cache")
				} else {
					require.NoError(t, err)
					require.Equal(t, existing, actual, "migration preflight must preserve existing marker/cache")
				}
			}
			t.Logf("%s: native project-free guard skipped bootstrap for 5 reads; identity files preserved", scenario)
		})
	}
}

func TestContextRecoveryIdentityRefusalFingerprintsCallerWithoutDisclosure(t *testing.T) {
	var messages []string
	for index := range 2 {
		srv := &mockEngramServer{}
		_, mod, project := buildContractDispatcher(t, startMockGRPC(t, srv))
		if index == 1 {
			project.Cwd = filepath.ToSlash(project.Cwd)
		}
		command := exec.CommandContext(t.Context(), "git", "-C", project.Cwd, "init", "--quiet", "--template=")
		command.Env = projectidentity.RepositoryGitEnvironmentV3()
		raw, err := command.CombinedOutput()
		require.NoError(t, err, "synthetic Git fixture: %s", raw)
		marker := filepath.Join(project.Cwd, ".engram-project")
		original := []byte(`{"unknown":"private-display-name","credential":"private-credential"}`)
		require.NoError(t, os.WriteFile(marker, original, 0o600))
		_, err = mod.ProxyHandleTool(t.Context(), project, "recall", json.RawMessage(`{}`))
		var refusal *module.ModuleError
		require.ErrorAs(t, err, &refusal)
		require.Equal(t, "PROJECT_IDENTITY_INVALID", refusal.Code)
		messages = append(messages, refusal.Message)
		want := fmt.Sprintf("caller_root_sha256=%x", sha256.Sum256([]byte(project.Cwd)))
		assert.Contains(t, refusal.Message, want, "identify the selected caller only by the exact UTF-8 Cwd fingerprint")
		assert.Contains(t, refusal.Message, "path withheld")
		assert.Contains(t, refusal.Message, "no selection or migration authority")
		assert.Contains(t, refusal.Message, "Preserve")
		assert.Contains(t, refusal.Message, "4096")
		assert.Less(t, len(refusal.Message), 2048, "refusal stays bounded and carries no path-sized payload")
		for _, private := range []string{project.Cwd, project.ID, contractProjectID, "private-display-name", "private-credential"} {
			for _, exposed := range []string{private, strconv.Quote(private), url.QueryEscape(private), base64.StdEncoding.EncodeToString([]byte(private)), hex.EncodeToString([]byte(private))} {
				assert.NotContains(t, refusal.Message, exposed, "raw, quoted or reversible encoding is still a disclosure")
			}
		}
		_, suffix, found := strings.Cut(refusal.Message, "caller_root_sha256=")
		if assert.True(t, found) {
			digest, _, _ := strings.Cut(suffix, ";")
			assert.Len(t, digest, 64)
		}
		srv.mu.Lock()
		called := srv.callReq != nil
		srv.mu.Unlock()
		require.False(t, called, "private refusal must not reach scoped server dispatch")
		preserved, readErr := os.ReadFile(marker)
		require.NoError(t, readErr)
		require.Equal(t, original, preserved)
	}
	require.NotEqual(t, messages[0], messages[1], "distinct caller roots produce distinct non-authoritative fingerprints")
}
