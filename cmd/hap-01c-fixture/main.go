// hap-01c-fixture is the runner-private controller for one guarded HAP-01C
// PostgreSQL fixture. It never accepts a DSN or keycard on argv or stdout.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thebtf/engram/internal/hap01cfixture"
)

const (
	usageExitCode    = 2
	failureExitCode  = 1
	boundaryExitCode = 10
)

type fixtureController interface {
	Close() error
	Seed(context.Context, string, string) (hap01cfixture.SeedReceipt, error)
	RotateProjectKeycard(context.Context, string) (hap01cfixture.RotationReceipt, error)
	Snapshot(context.Context, string) (hap01cfixture.SnapshotReceipt, error)
}

type commandDependencies struct {
	open             func(context.Context, string, string) (fixtureController, error)
	workingDirectory func() (string, error)
}

func defaultCommandDependencies() commandDependencies {
	return commandDependencies{
		open: func(ctx context.Context, dsnFile, runID string) (fixtureController, error) {
			return hap01cfixture.Open(ctx, dsnFile, runID)
		},
		workingDirectory: os.Getwd,
	}
}

type invocation struct {
	command     string
	runID       string
	dsnFile     string
	requestFile string
	secretsFile string
	outFile     string
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(parent context.Context, args []string, stdout, stderr io.Writer) int {
	return runWithDependencies(parent, args, stdout, stderr, defaultCommandDependencies())
}

func runWithDependencies(parent context.Context, args []string, stdout, stderr io.Writer, deps commandDependencies) int {
	invocation, err := parseInvocation(args)
	if err != nil {
		fmt.Fprintln(stderr, "usage: hap-01c-fixture <seed|rotate-project-keycard|snapshot> required flags")
		return usageExitCode
	}
	if deps.open == nil || deps.workingDirectory == nil {
		fmt.Fprintln(stderr, "hap-01c-fixture: operation unavailable")
		return failureExitCode
	}
	if err := hap01cfixture.ValidateRunID(invocation.runID); err != nil {
		fmt.Fprintln(stderr, "hap-01c-fixture: boundary refusal")
		return boundaryExitCode
	}
	workingDirectory, err := deps.workingDirectory()
	if err != nil || !validateScratchPaths(workingDirectory, invocation) {
		fmt.Fprintln(stderr, "hap-01c-fixture: boundary refusal")
		return boundaryExitCode
	}

	fixture, err := deps.open(parent, invocation.dsnFile, invocation.runID)
	if err != nil {
		writeOperationFailure(stderr, err)
		return exitCodeFor(err)
	}
	defer func() { _ = fixture.Close() }()

	switch invocation.command {
	case "seed":
		receipt, err := fixture.Seed(parent, invocation.requestFile, invocation.secretsFile)
		if err != nil {
			writeOperationFailure(stderr, err)
			return exitCodeFor(err)
		}
		if err := writeJSON(stdout, receipt); err != nil {
			fmt.Fprintln(stderr, "hap-01c-fixture: receipt write failed")
			return failureExitCode
		}
		return 0
	case "rotate-project-keycard":
		receipt, err := fixture.RotateProjectKeycard(parent, invocation.secretsFile)
		if err != nil {
			writeOperationFailure(stderr, err)
			return exitCodeFor(err)
		}
		if err := writeJSON(stdout, receipt); err != nil {
			fmt.Fprintln(stderr, "hap-01c-fixture: receipt write failed")
			return failureExitCode
		}
		return 0
	case "snapshot":
		receipt, err := fixture.Snapshot(parent, invocation.requestFile)
		if err != nil {
			writeOperationFailure(stderr, err)
			return exitCodeFor(err)
		}
		if err := writePrivateJSON(invocation.outFile, receipt); err != nil {
			fmt.Fprintln(stderr, "hap-01c-fixture: receipt write failed")
			return failureExitCode
		}
		return 0
	default:
		fmt.Fprintln(stderr, "usage: hap-01c-fixture <seed|rotate-project-keycard|snapshot> required flags")
		return usageExitCode
	}
}

func parseInvocation(args []string) (invocation, error) {
	if len(args) == 0 {
		return invocation{}, fmt.Errorf("missing command")
	}
	parsed := invocation{command: args[0]}
	var required map[string]struct{}
	switch parsed.command {
	case "seed":
		required = map[string]struct{}{
			"--run-id": {}, "--dsn-file": {}, "--request-file": {}, "--secrets-out": {},
		}
	case "rotate-project-keycard":
		required = map[string]struct{}{
			"--run-id": {}, "--dsn-file": {}, "--secrets-file": {},
		}
	case "snapshot":
		required = map[string]struct{}{
			"--run-id": {}, "--dsn-file": {}, "--request-file": {}, "--out": {},
		}
	default:
		return invocation{}, fmt.Errorf("unknown command")
	}
	if (len(args)-1)%2 != 0 {
		return invocation{}, fmt.Errorf("flag value required")
	}
	seen := make(map[string]struct{}, len(required))
	for index := 1; index < len(args); index += 2 {
		flag, value := args[index], args[index+1]
		if !strings.HasPrefix(flag, "--") || strings.Contains(flag, "=") || value == "" || strings.HasPrefix(value, "--") {
			return invocation{}, fmt.Errorf("invalid argument")
		}
		if _, allowed := required[flag]; !allowed {
			return invocation{}, fmt.Errorf("unknown flag")
		}
		if _, duplicate := seen[flag]; duplicate {
			return invocation{}, fmt.Errorf("duplicate flag")
		}
		seen[flag] = struct{}{}
		switch flag {
		case "--run-id":
			parsed.runID = value
		case "--dsn-file":
			parsed.dsnFile = value
		case "--request-file":
			parsed.requestFile = value
		case "--secrets-out", "--secrets-file":
			parsed.secretsFile = value
		case "--out":
			parsed.outFile = value
		}
	}
	if len(seen) != len(required) {
		return invocation{}, fmt.Errorf("missing required flag")
	}
	return parsed, nil
}

func validateScratchPaths(workingDirectory string, invocation invocation) bool {
	if workingDirectory == "" {
		return false
	}
	if !filepath.IsAbs(workingDirectory) {
		absolute, err := filepath.Abs(workingDirectory)
		if err != nil {
			return false
		}
		workingDirectory = absolute
	}
	root := filepath.Join(
		filepath.Clean(workingDirectory),
		".agent",
		"tmp",
		"hap-01c",
		invocation.runID,
		"fixture",
		"hap01c-"+invocation.runID,
	)
	rootRelative, err := filepath.Rel(filepath.Clean(workingDirectory), root)
	if err != nil || rootRelative == "." || strings.HasPrefix(rootRelative, ".."+string(filepath.Separator)) || !existingPathComponentsAreSafe(filepath.Clean(workingDirectory), rootRelative) {
		return false
	}
	paths := []string{invocation.dsnFile}
	if invocation.secretsFile != "" {
		paths = append(paths, invocation.secretsFile)
	}
	if invocation.requestFile != "" {
		paths = append(paths, invocation.requestFile)
	}
	if invocation.outFile != "" {
		paths = append(paths, invocation.outFile)
	}
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		absolute, ok := containedScratchPath(workingDirectory, root, path)
		if !ok {
			return false
		}
		if _, duplicate := seen[absolute]; duplicate {
			return false
		}
		seen[absolute] = struct{}{}
	}
	return true
}

func containedScratchPath(workingDirectory, root, candidate string) (string, bool) {
	if candidate == "" {
		return "", false
	}
	absolute := candidate
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(workingDirectory, absolute)
	}
	absolute = filepath.Clean(absolute)
	root = filepath.Clean(root)
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	if !existingPathComponentsAreSafe(root, relative) {
		return "", false
	}
	return absolute, true
}

func existingPathComponentsAreSafe(root, relative string) bool {
	info, err := os.Lstat(root)
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	if err != nil && !os.IsNotExist(err) {
		return false
	}
	current := root
	parts := strings.Split(relative, string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return true
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return false
		}
		if index < len(parts)-1 && !info.IsDir() {
			return false
		}
	}
	return true
}

func writeJSON(writer io.Writer, value interface{}) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writePrivateJSON(path string, value interface{}) error {
	content, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("unsafe receipt path")
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	temporary, err := os.CreateTemp(filepath.Dir(path), ".hap01c-snapshot-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	written, err := temporary.Write(content)
	if err != nil || written != len(content) {
		_ = temporary.Close()
		return fmt.Errorf("receipt write failed")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func writeOperationFailure(stderr io.Writer, err error) {
	if hap01cfixture.IsBoundaryError(err) {
		fmt.Fprintln(stderr, "hap-01c-fixture: boundary refusal")
		return
	}
	fmt.Fprintln(stderr, "hap-01c-fixture: operation failed")
}

func exitCodeFor(err error) int {
	if hap01cfixture.IsBoundaryError(err) {
		return boundaryExitCode
	}
	return failureExitCode
}
