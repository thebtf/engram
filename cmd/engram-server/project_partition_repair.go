package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
)

type partitionRepairCLIOptions struct {
	stage        string
	sourceFile   string
	targetFile   string
	planFile     string
	planID       string
	parentDigest string
}

func parseProjectPartitionRepairArgs(args []string) (partitionRepairCLIOptions, error) {
	var options partitionRepairCLIOptions
	if len(args) == 0 {
		return options, errors.New("PROJECT_ADOPTION_CLI_ARGUMENTS_INVALID")
	}
	options.stage = args[0]
	if options.stage != "plan" && options.stage != "dry-run" && options.stage != "apply" {
		return options, errors.New("PROJECT_ADOPTION_CLI_ARGUMENTS_INVALID")
	}
	flags := flag.NewFlagSet("repair-project-partitions "+options.stage, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.sourceFile, "source-dsn-file", "", "Private read-only source connection file")
	flags.StringVar(&options.targetFile, "target-dsn-file", "", "Private separately restored target connection file")
	flags.StringVar(&options.planFile, "plan-file", "", "Private sealed repair plan file; created exclusively by plan")
	flags.StringVar(&options.planID, "plan-id", "", "Exact plan ID observed in the accepted dry-run")
	flags.StringVar(&options.parentDigest, "parent-receipt-digest", "", "Exact SHA-256 of the stored parent B7 receipt JSONB text")
	if err := flags.Parse(args[1:]); err != nil {
		return options, err
	}
	if flags.NArg() != 0 || options.sourceFile == "" || options.planFile == "" {
		return options, errors.New("PROJECT_ADOPTION_CLI_ARGUMENTS_INVALID")
	}
	if options.stage == "plan" {
		if len(options.parentDigest) != 64 || options.targetFile != "" || options.planID != "" {
			return options, errors.New("PROJECT_ADOPTION_CLI_ARGUMENTS_INVALID")
		}
	} else if options.targetFile == "" || options.parentDigest != "" {
		return options, errors.New("PROJECT_ADOPTION_CLI_ARGUMENTS_INVALID")
	}
	if options.stage == "apply" && options.planID == "" {
		return options, errors.New("PROJECT_ADOPTION_PLAN_ID_REQUIRED")
	}
	return options, nil
}

func runProjectPartitionRepair(args []string) int {
	return runProjectPartitionRepairOutput(args, os.Stdout, os.Stderr)
}

func runProjectPartitionRepairOutput(args []string, stdout, stderr io.Writer) int {
	fail := func(code string) int {
		_ = json.NewEncoder(stderr).Encode(struct {
			Code string `json:"code"`
		}{code})
		return 1
	}
	options, err := parseProjectPartitionRepairArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = io.WriteString(stdout, "engram-server repair-project-partitions plan|dry-run|apply --source-dsn-file S --plan-file P [--parent-receipt-digest SHA256 | --target-dsn-file T [--plan-id ID]]\n")
		return 0
	}
	if err != nil {
		if options.stage == "apply" && options.planID == "" {
			return fail("PROJECT_ADOPTION_PLAN_ID_REQUIRED")
		}
		return fail("PROJECT_ADOPTION_CLI_ARGUMENTS_INVALID")
	}
	sourceDSN, err := readConsolidationDSN(options.sourceFile)
	if err != nil {
		return fail("PROJECT_ADOPTION_PRIVATE_INPUT_UNAVAILABLE")
	}
	var targetDSN string
	if options.stage != "plan" {
		targetDSN, err = readConsolidationDSN(options.targetFile)
		if err != nil {
			return fail("PROJECT_ADOPTION_PRIVATE_INPUT_UNAVAILABLE")
		}
		if sourceDSN == targetDSN {
			return fail("PROJECT_ADOPTION_TARGET_IS_SOURCE")
		}
	}
	vault, err := vaultcrypto.OpenExistingVault(config.Get())
	if err != nil {
		return fail("PROJECT_ADOPTION_EXISTING_VAULT_REQUIRED")
	}
	source, closeSource, err := openConsolidationDatabase(sourceDSN, true)
	if err != nil {
		return fail("PROJECT_ADOPTION_CONNECTION_INVALID")
	}
	defer closeSource()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if options.stage == "plan" {
		plan, err := gormdb.NewProjectConsolidationStore(source, vault, nil).PlanProjectPartitionRepair(ctx, options.parentDigest)
		if err != nil {
			return fail(consolidationCLIError(err))
		}
		file, err := os.OpenFile(options.planFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fail("PROJECT_ADOPTION_PLAN_FILE_UNAVAILABLE")
		}
		encodeErr := json.NewEncoder(file).Encode(plan)
		syncErr := file.Sync()
		closeErr := file.Close()
		if encodeErr != nil || syncErr != nil || closeErr != nil {
			return fail("PROJECT_ADOPTION_PLAN_FILE_WRITE_FAILED")
		}
		plan.Reference = ""
		if json.NewEncoder(stdout).Encode(plan) != nil {
			return 1
		}
		return 0
	}
	request, err := readConsolidationPlan(options.planFile, options.planID)
	if err != nil {
		return fail("PROJECT_ADOPTION_PLAN_FILE_INVALID")
	}
	target, closeTarget, err := openConsolidationDatabase(targetDSN, false)
	if err != nil {
		return fail("PROJECT_ADOPTION_CONNECTION_INVALID")
	}
	defer closeTarget()
	store := gormdb.NewProjectConsolidationStore(source, vault, target)
	input := gormdb.ProjectPartitionRepairApplyRequest{PlanReference: request.PlanReference, PlanFingerprint: request.PlanFingerprint, ExpectedPlanID: request.ExpectedPlanID}
	var result gormdb.ProjectPartitionRepairResult
	if options.stage == "apply" {
		result, err = store.ApplyProjectPartitionRepair(ctx, input)
	} else {
		result, err = store.DryRunProjectPartitionRepair(ctx, input)
	}
	if err != nil {
		return fail(consolidationCLIError(err))
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return 1
	}
	return 0
}
