package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProjectPartitionRepairCLIRequiresDistinctExplicitStages(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"apply", "--source-dsn-file", "S", "--target-dsn-file", "T", "--plan-file", "P"},
		{"plan", "--source-dsn-file", "S", "--plan-file", "P"},
		{"plan", "--source-dsn-file", "S", "--target-dsn-file", "T", "--plan-file", "P", "--parent-receipt-digest", strings.Repeat("a", 64)},
		{"dry-run", "--source-dsn-file", "S", "--plan-file", "P"},
		{"dry-run", "--source-dsn-file", "S", "--target-dsn-file", "T", "--plan-file", "P", "--commit"},
		{"apply", "--source-dsn-file", "S", "--target-dsn-file", "T", "--plan-file", "P", "--plan-id", "ID", "extra"},
	} {
		_, err := parseProjectPartitionRepairArgs(args)
		require.Error(t, err)
	}
	plan, err := parseProjectPartitionRepairArgs([]string{"plan", "--source-dsn-file", "S", "--plan-file", "P", "--parent-receipt-digest", strings.Repeat("a", 64)})
	require.NoError(t, err)
	require.Empty(t, plan.targetFile)
	for _, stage := range []string{"dry-run", "apply"} {
		options, err := parseProjectPartitionRepairArgs([]string{stage, "--source-dsn-file", "S", "--target-dsn-file", "T", "--plan-file", "P", "--plan-id", "ID"})
		require.NoError(t, err)
		require.Equal(t, stage, options.stage)
		require.Equal(t, "ID", options.planID)
	}
}

func TestProjectPartitionRepairCLIHelpAndRefusalNeverStartService(t *testing.T) {
	var stdout, stderr bytes.Buffer
	require.Zero(t, runProjectPartitionRepairOutput([]string{"plan", "--help"}, &stdout, &stderr))
	require.Contains(t, stdout.String(), "repair-project-partitions plan|dry-run|apply")
	require.Empty(t, stderr.String())
	stdout.Reset()
	require.NotZero(t, runProjectPartitionRepairOutput([]string{"apply", "--source-dsn-file", "missing-source", "--target-dsn-file", "missing-target", "--plan-file", "missing-plan"}, &stdout, &stderr))
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), "PROJECT_ADOPTION_PLAN_ID_REQUIRED")
}
