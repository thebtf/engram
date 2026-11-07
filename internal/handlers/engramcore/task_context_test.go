package engramcore

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskcontext"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

func TestTaskContextNativeReadOnlyStages(t *testing.T) {
	root := t.TempDir()
	command := exec.Command("git", "init", "--quiet", root)
	require.NoError(t, command.Run())
	marker := []byte(`{"version":3,"project_id":"10000000-0000-4000-8000-000000000001","name":"fixture","scope":"repository"}`)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".engram-project"), marker, 0o600))
	for sample := range 3 {
		ctx, cancel := context.WithTimeout(context.Background(), taskcontext.MaxDuration)
		start := time.Now()
		ctx, locator, err := nativeCodebaseGitContext(ctx, root)
		locatorElapsed := time.Since(start)
		t.Logf("NATIVE_DISCOVERY sample=%d elapsed_ms=%.3f error=%v", sample, float64(locatorElapsed.Microseconds())/1000, err)
		require.NoError(t, err)
		start = time.Now()
		inspection, err := projectidentity.CheckLogicalProject(ctx, root, "", "")
		projectElapsed := time.Since(start)
		cancel()
		t.Logf("NATIVE_READ_ONLY_STAGES sample=%d locator_ms=%.3f project_ms=%.3f", sample, float64(locatorElapsed.Microseconds())/1000, float64(projectElapsed.Microseconds())/1000)
		require.NoError(t, err)
		require.Equal(t, "resolved", inspection.Status)
		require.NotEmpty(t, locator)
	}
	actual, err := os.ReadFile(filepath.Join(root, ".engram-project"))
	require.NoError(t, err)
	require.Equal(t, marker, actual)
	_, err = os.Stat(filepath.Join(root, ".git", "engram-project.json"))
	require.True(t, os.IsNotExist(err))
}

func TestTaskContextInvalidNativeInstallationIsClosed(t *testing.T) {
	module := NewModuleWithClientInstanceID("@invalid")
	ctx := auditcontext.WithUCITransportSession(t.Context(), "native-onboarding-session")
	raw, err := json.Marshal(taskcontext.Request{Schema: 1, HostSessionID: "host", Query: "Alpha", DeadlineUnixMS: time.Now().Add(500 * time.Millisecond).UnixMilli(), MaxBytes: 8000})
	require.NoError(t, err)
	block, err := module.ProxyHandleTool(ctx, muxcore.ProjectContext{}, "codebase_task_context", raw)
	require.NoError(t, err)
	var content struct {
		Text string `json:"text"`
	}
	require.NoError(t, json.Unmarshal(block, &content))
	var response taskcontext.Response
	require.NoError(t, json.Unmarshal([]byte(content.Text), &response))
	require.Equal(t, "forbidden", response.Status)
	require.NoError(t, response.Validate())
}
