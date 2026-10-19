package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/thebtf/engram/internal/config"
	vaultcrypto "github.com/thebtf/engram/internal/crypto"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"gorm.io/driver/postgres"
	gormlib "gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const consolidationPlanFileLimit = 128 << 20

type consolidationCLIOptions struct {
	sourceFile string
	targetFile string
	planFile   string
	planID     string
	commit     bool
}

func parseProjectConsolidationArgs(args []string) (consolidationCLIOptions, error) {
	var options consolidationCLIOptions
	flags := flag.NewFlagSet("consolidate-projects", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&options.sourceFile, "source-dsn-file", "", "Private source connection file")
	flags.StringVar(&options.targetFile, "target-dsn-file", "", "Private isolated target connection file")
	flags.StringVar(&options.planFile, "plan-file", "", "Private ordinary Preflight plan JSON file")
	flags.StringVar(&options.planID, "plan-id", "", "Expected ID reported by the validated dry-run")
	flags.BoolVar(&options.commit, "commit", false, "Apply to the isolated target; default is rollback")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 || options.sourceFile == "" || options.targetFile == "" || options.planFile == "" {
		return options, errors.New("PROJECT_ADOPTION_CLI_ARGUMENTS_INVALID")
	}
	if options.commit && options.planID == "" {
		return options, errors.New("PROJECT_ADOPTION_PLAN_ID_REQUIRED")
	}
	return options, nil
}

func openConsolidationInput(path string, limit int64) (*os.File, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("PROJECT_ADOPTION_PRIVATE_INPUT_UNAVAILABLE")
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, errors.New("PROJECT_ADOPTION_PRIVATE_INPUT_UNAVAILABLE")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		file.Close()
		return nil, errors.New("PROJECT_ADOPTION_PRIVATE_INPUT_INVALID")
	}
	return file, nil
}

func readConsolidationDSN(path string) (string, error) {
	file, err := openConsolidationInput(path, 65536)
	if err != nil {
		return "", err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(raw) > 65536 {
		return "", errors.New("PROJECT_ADOPTION_PRIVATE_INPUT_INVALID")
	}
	dsn := strings.TrimSpace(string(raw))
	if dsn == "" {
		return "", errors.New("PROJECT_ADOPTION_PRIVATE_INPUT_INVALID")
	}
	return dsn, nil
}

func readConsolidationPlan(path, expectedID string) (gormdb.ConsolidationNativeRequest, error) {
	request := gormdb.ConsolidationNativeRequest{ExpectedPlanID: expectedID}
	file, err := openConsolidationInput(path, consolidationPlanFileLimit)
	if err != nil {
		return request, err
	}
	defer file.Close()
	var input struct {
		Reference   string `json:"plan_reference"`
		Fingerprint string `json:"plan_fingerprint"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, consolidationPlanFileLimit+1))
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.Reference == "" || len(input.Reference) > 64<<20 || input.Fingerprint == "" || len(input.Fingerprint) > 256 {
		return request, errors.New("PROJECT_ADOPTION_PLAN_FILE_INVALID")
	}
	request.PlanReference, request.PlanFingerprint = input.Reference, input.Fingerprint
	return request, nil
}

func consolidationCLIError(err error) string {
	var domain *gormdb.ConsolidationError
	if errors.As(err, &domain) {
		switch domain.Code {
		case "PROJECT_ADOPTION_PLAN_ID_REQUIRED", "PROJECT_ADOPTION_STALE_PLAN", "PROJECT_ADOPTION_INVENTORY_INCOMPLETE", "PROJECT_ADOPTION_BACKUP_MISSING", "PROJECT_ADOPTION_ISOLATED_TARGET_MISSING", "PROJECT_ADOPTION_TARGET_IS_SOURCE", "PROJECT_ADOPTION_SOURCE_IDENTITY_UNAVAILABLE", "PROJECT_ADOPTION_CONSERVATION_FAILED", "PROJECT_ADOPTION_SCHEMA_MISMATCH", "PROJECT_ADOPTION_UNSTABLE_NATIVE_KEY", "PROJECT_ADOPTION_STALE_MAPPING", "PROJECT_ADOPTION_REPLAY_CONFLICT", "PROJECT_ADOPTION_COLLISION", "PROJECT_ADOPTION_SECRET_UNREADABLE":
			return domain.Code
		}
	}
	if errors.Is(err, context.Canceled) {
		return "PROJECT_ADOPTION_CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "PROJECT_ADOPTION_DEADLINE"
	}
	return "PROJECT_ADOPTION_NATIVE_OPERATION_FAILED"
}

func openConsolidationDatabase(dsn string, source bool) (*gormlib.DB, func() error, error) {
	configuration, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, nil, errors.New("PROJECT_ADOPTION_CONNECTION_INVALID")
	}
	configuration.RuntimeParams["application_name"] = "engram_project_maintenance"
	if source {
		configuration.RuntimeParams["default_transaction_read_only"] = "on"
	}
	pool := stdlib.OpenDB(*configuration)
	pool.SetMaxOpenConns(2)
	pool.SetMaxIdleConns(1)
	db, err := gormlib.Open(postgres.New(postgres.Config{Conn: pool}), &gormlib.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true})
	if err != nil {
		_ = pool.Close()
		return nil, nil, err
	}
	return db, pool.Close, nil
}

func runProjectConsolidation(args []string) int {
	return runProjectConsolidationOutput(args, os.Stdout, os.Stderr)
}

func runProjectConsolidationOutput(args []string, stdout, stderr io.Writer) int {
	fail := func(code string) int {
		_ = json.NewEncoder(stderr).Encode(struct {
			Code string `json:"code"`
		}{code})
		return 1
	}
	options, err := parseProjectConsolidationArgs(args)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = io.WriteString(stdout, "engram-server consolidate-projects --source-dsn-file S --target-dsn-file T --plan-file P [--plan-id ID] [--commit]\n")
		return 0
	}
	if err != nil {
		if options.commit && options.planID == "" {
			return fail("PROJECT_ADOPTION_PLAN_ID_REQUIRED")
		}
		return fail("PROJECT_ADOPTION_CLI_ARGUMENTS_INVALID")
	}
	request, err := readConsolidationPlan(options.planFile, options.planID)
	if err != nil {
		return fail("PROJECT_ADOPTION_PLAN_FILE_INVALID")
	}
	sourceDSN, err := readConsolidationDSN(options.sourceFile)
	if err != nil {
		return fail("PROJECT_ADOPTION_PRIVATE_INPUT_UNAVAILABLE")
	}
	targetDSN, err := readConsolidationDSN(options.targetFile)
	if err != nil {
		return fail("PROJECT_ADOPTION_PRIVATE_INPUT_UNAVAILABLE")
	}
	if sourceDSN == targetDSN {
		return fail("PROJECT_ADOPTION_TARGET_IS_SOURCE")
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
	target, closeTarget, err := openConsolidationDatabase(targetDSN, false)
	if err != nil {
		return fail("PROJECT_ADOPTION_CONNECTION_INVALID")
	}
	defer closeTarget()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	store := gormdb.NewProjectConsolidationStore(source, vault, target)
	var result gormdb.ConsolidationNativeResult
	if options.commit {
		result, err = store.ApplyNative(ctx, request)
	} else {
		result, err = store.DryRunNative(ctx, request)
	}
	if err != nil {
		return fail(consolidationCLIError(err))
	}
	var before, after int64
	for _, role := range result.Receipt.Roles {
		before += role.Before
		after += role.After
	}
	summary := struct {
		PlanID            string `json:"plan_id"`
		TargetIdentity    string `json:"target_identity"`
		DryRun            bool   `json:"dry_run"`
		Replay            bool   `json:"replay"`
		OperationID       string `json:"operation_id"`
		ReceiptReference  string `json:"receipt_reference"`
		Snapshot          string `json:"after_snapshot_identity"`
		OccurrencesBefore int64  `json:"occurrences_before"`
		OccurrencesAfter  int64  `json:"occurrences_after"`
	}{result.PlanID, result.TargetIdentity, result.DryRun, result.Replay, result.Receipt.OperationID, result.Receipt.Reference, result.Receipt.Snapshot, before, after}
	if json.NewEncoder(stdout).Encode(summary) != nil {
		return fail("PROJECT_ADOPTION_OUTPUT_FAILED")
	}
	return 0
}
