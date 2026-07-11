package loom_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	loomhandler "github.com/thebtf/engram/internal/handlers/loom"
)

var loomCLIHelperName string

func TestMain(m *testing.M) {
	helperDir, err := os.MkdirTemp("", "engram-loom-cli-helper-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create loom helper dir: %v\n", err)
		os.Exit(1)
	}

	loomCLIHelperName = "engram-loom-cli-helper"
	if runtime.GOOS == "windows" {
		loomCLIHelperName += ".exe"
	}
	helperPath := filepath.Join(helperDir, loomCLIHelperName)
	build := exec.Command("go", "build", "-o", helperPath, "./testdata/clihelper")
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		_ = os.RemoveAll(helperDir)
		fmt.Fprintf(os.Stderr, "build loom CLI helper: %v\n", err)
		os.Exit(1)
	}

	originalPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", helperDir+string(os.PathListSeparator)+originalPath); err != nil {
		_ = os.RemoveAll(helperDir)
		fmt.Fprintf(os.Stderr, "prepend loom helper PATH: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	if err := os.Setenv("PATH", originalPath); err != nil {
		fmt.Fprintf(os.Stderr, "restore PATH after loom tests: %v\n", err)
		code = 1
	}
	if err := os.RemoveAll(helperDir); err != nil {
		fmt.Fprintf(os.Stderr, "remove loom helper dir: %v\n", err)
		code = 1
	}
	os.Exit(code)
}

func TestCliWorker_ProductionReadinessStructuredArgsAndCWD(t *testing.T) {
	t.Parallel()

	cwd := t.TempDir()
	task := helperTask("structured prompt", "state")
	task.Role = "maker"
	task.Model = "test-model"
	task.Effort = "high"
	task.CWD = cwd
	task.Env["MY_TEST_VAR"] = "structured-env"

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{loomCLIHelperName})
	result, err := w.Execute(context.Background(), task)
	require.NoError(t, err)

	var state struct {
		Args   []string `json:"args"`
		CWD    string   `json:"cwd"`
		Env    string   `json:"env"`
		Prompt string   `json:"prompt"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content), &state))
	assert.Equal(t, []string{"--role", "maker", "--model", "test-model", "--effort", "high"}, state.Args)
	assert.Equal(t, cwd, state.CWD)
	assert.Equal(t, "structured-env", state.Env)
	assert.Equal(t, "structured prompt", state.Prompt)
	assert.GreaterOrEqual(t, result.DurationMS, int64(0))
}

func TestCliWorker_ProductionReadinessOutputLimit(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{loomCLIHelperName})
	result, err := w.Execute(context.Background(), helperTask("", "huge"))
	require.NoError(t, err)
	require.Len(t, result.Content, 10*1024*1024, "stdout must be capped at maxOutputBytes")
}

func TestCliWorker_ProductionReadinessOutputLimitUnalignedWrite(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{loomCLIHelperName})
	result, err := w.Execute(context.Background(), helperTask("", "huge-unaligned"))
	require.NoError(t, err, "crossing the cap inside a child write must discard overflow without io.ErrShortWrite")
	require.Len(t, result.Content, 10*1024*1024, "unaligned stdout must still be capped at maxOutputBytes")
}

func TestCliWorker_ProductionReadinessExitWithoutStderr(t *testing.T) {
	t.Parallel()

	w := loomhandler.NewCLIWorkerWithAllowlist([]string{loomCLIHelperName})
	_, err := w.Execute(context.Background(), helperTask("", "exit"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exited with code 9")
}

func TestCliWorker_ProductionReadinessMissingExecutable(t *testing.T) {
	t.Parallel()

	const missing = "engram-loom-definitely-missing-command"
	w := loomhandler.NewCLIWorkerWithAllowlist([]string{missing})
	task := helperTask("", "echo")
	task.CLI = missing

	_, err := w.Execute(context.Background(), task)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "run "+missing)
}
