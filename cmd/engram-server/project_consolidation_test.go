package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectConsolidationCLIRequiresPrivateFilesAndExplicitCommitPlan(t *testing.T) {
	for _, args := range [][]string{nil, {"--source-dsn-file", "source", "--target-dsn-file", "target"}, {"--source-dsn-file", "source", "--target-dsn-file", "target", "--plan-file", "plan", "--commit"}, {"--source-dsn-file", "source", "--target-dsn-file", "target", "--plan-file", "plan", "unexpected"}} {
		_, err := parseProjectConsolidationArgs(args)
		require.Error(t, err)
	}
	options, err := parseProjectConsolidationArgs([]string{"--source-dsn-file", "source", "--target-dsn-file", "target", "--plan-file", "plan"})
	require.NoError(t, err)
	require.False(t, options.commit)
	require.Empty(t, options.planID)
	options, err = parseProjectConsolidationArgs([]string{"--source-dsn-file", "source", "--target-dsn-file", "target", "--plan-file", "plan", "--plan-id", "expected", "--commit"})
	require.NoError(t, err)
	require.True(t, options.commit)
	require.Equal(t, "expected", options.planID)
}

func TestProjectConsolidationCLIPlanUsesOnlyAuthenticatedReference(t *testing.T) {
	file := filepath.Join(t.TempDir(), "plan.json")
	raw := `{"plan_reference":"retained-private-token","plan_fingerprint":"fingerprint","mapping":[{"project_id":"caller-untrusted"}],"plan_id":"caller-untrusted"}`
	require.NoError(t, os.WriteFile(file, []byte(raw), 0o600))
	request, err := readConsolidationPlan(file, "expected")
	require.NoError(t, err)
	require.Equal(t, "retained-private-token", request.PlanReference)
	require.Equal(t, "fingerprint", request.PlanFingerprint)
	require.Equal(t, "expected", request.ExpectedPlanID)
	for _, raw := range []string{`{}`, `{"plan_reference":"token"}`, `{"plan_reference":"token","plan_fingerprint":"fp"}{}`, `{"plan_reference":"token","plan_fingerprint":"fp"} trailing`} {
		require.NoError(t, os.WriteFile(file, []byte(raw), 0o600))
		_, err = readConsolidationPlan(file, "")
		require.Error(t, err)
	}
}

func TestProjectConsolidationCLINeverPrintsInputOrFallsBackToLiveDSN(t *testing.T) {
	t.Setenv("DATABASE_DSN", "private-live-dsn-must-not-be-used")
	var stdout, stderr bytes.Buffer
	code := runProjectConsolidationOutput([]string{"--source-dsn-file", "missing-source", "--target-dsn-file", "missing-target", "--plan-file", "missing-plan"}, &stdout, &stderr)
	require.NotZero(t, code)
	require.Empty(t, stdout.String())
	require.NotContains(t, stderr.String(), "private-live-dsn")
	require.NotContains(t, stderr.String(), "missing-plan")
	var refusal struct {
		Code string `json:"code"`
	}
	require.NoError(t, json.Unmarshal(stderr.Bytes(), &refusal))
	require.Equal(t, "PROJECT_ADOPTION_PLAN_FILE_INVALID", refusal.Code)
	directory := t.TempDir()
	plan := filepath.Join(directory, "plan.json")
	require.NoError(t, os.WriteFile(plan, []byte(`{"plan_reference":"never-print-this-token","plan_fingerprint":"fp"}`), 0o600))
	source, target := filepath.Join(directory, "source"), filepath.Join(directory, "target")
	private := []byte("postgresql://owner:private-test-value@127.0.0.1:12345/same")
	require.NoError(t, os.WriteFile(source, private, 0o600))
	require.NoError(t, os.WriteFile(target, private, 0o600))
	stdout.Reset()
	stderr.Reset()
	code = runProjectConsolidationOutput([]string{"--source-dsn-file", source, "--target-dsn-file", target, "--plan-file", plan}, &stdout, &stderr)
	require.NotZero(t, code)
	require.Empty(t, stdout.String())
	require.NotContains(t, stderr.String(), "private-test-value")
	require.NotContains(t, stderr.String(), "never-print-this-token")
	require.Contains(t, stderr.String(), "PROJECT_ADOPTION_TARGET_IS_SOURCE")
}
