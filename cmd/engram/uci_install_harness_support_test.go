package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"
)

const uciInstallHarnessVersionV1 = "uci-install-harness/v1"

type uciInstallHarnessCommand struct {
	Executable string
	Args       []string
}

type uciMCPStdioProcess struct {
	PID    int
	Stdin  io.WriteCloser
	Stdout io.ReadCloser
}

type uciStandardMCPDriver interface {
	InitializeAndList(context.Context, uciMCPStdioProcess) ([]string, error)
}

// uciInstallHarnessScenario selects a lifecycle without creating another
// installer. The zero value remains the original T017 standard-MCP proof.
type uciInstallHarnessScenario string

const (
	uciInstallHarnessScenarioStandardMCP uciInstallHarnessScenario = "standard_mcp"
	uciInstallHarnessScenarioMaterialize uciInstallHarnessScenario = "materialize"
)

type uciInstallHarnessRequest struct {
	Version          string
	Scenario         uciInstallHarnessScenario
	InstallRoot      string
	Server           uciInstallHarnessCommand
	Daemon           uciInstallHarnessCommand
	Parser           uciInstallHarnessCommand
	Environment      []string
	ReadinessTimeout time.Duration
	MCPDriver        uciStandardMCPDriver
}

type uciInstallHarnessResult struct {
	ToolNames    []string
	Installation *uciInstallHarnessInstallation
}

// uciInstallHarnessInstallation is the materialized T017 installer state used
// by the installed acceptance driver. It owns the Windows child tree and must
// be closed by a materialize caller.
type uciInstallHarnessInstallation struct {
	installRoot string
	components  map[string]uciInstallHarnessComponent
	environment []string
	tree        *uciInstallHarnessProcessTree
	launchMu    sync.Mutex
	processesMu sync.Mutex
	processes   []*uciStartedInstallHarnessProcess
	closeOnce   sync.Once
	closeErr    error
}

type uciInstalledHarnessLaunchRequest struct {
	Role             string
	Args             []string
	WorkingDirectory string
	Environment      []string
	WithStdio        bool
}

type uciInstallHarnessComponent struct {
	role                string
	command             uciInstallHarnessCommand
	installedExecutable string
}

type uciStartedInstallHarnessProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	stdout  io.ReadCloser

	closePipesOnce sync.Once
	closePipesErr  error
	waitOnce       sync.Once
	waitErr        error
}

// runUCIInstallHarness proves the installed Windows execution path by copying
// each component to a disposable root and exercising the daemon through its
// real standard-I/O MCP channel. Materialize mode intentionally stops after
// the same install step so the installed acceptance driver can own the normal
// shim/daemon election without creating a second installer.
func runUCIInstallHarness(ctx context.Context, request uciInstallHarnessRequest) (result uciInstallHarnessResult, err error) {
	if err := uciValidateInstallHarnessRequest(ctx, request); err != nil {
		return uciInstallHarnessResult{}, err
	}

	scenario, err := uciInstallHarnessRequestScenario(request)
	if err != nil {
		return uciInstallHarnessResult{}, err
	}
	installation, err := newUCIInstallHarnessInstallation(ctx, request)
	if err != nil {
		return uciInstallHarnessResult{}, err
	}
	if scenario == uciInstallHarnessScenarioMaterialize {
		return uciInstallHarnessResult{Installation: installation}, nil
	}
	defer func() {
		if cleanupErr := installation.Close(); cleanupErr != nil {
			err = errors.Join(err, cleanupErr)
		}
	}()

	var daemon *uciStartedInstallHarnessProcess
	for _, role := range []string{"server", "daemon", "parser"} {
		component, found := installation.components[role]
		if !found {
			return uciInstallHarnessResult{}, fmt.Errorf("UCI install harness %s component is unavailable", role)
		}
		process, startErr := installation.Start(ctx, uciInstalledHarnessLaunchRequest{
			Role:      role,
			Args:      component.command.Args,
			WithStdio: role == "daemon",
		})
		if startErr != nil {
			return uciInstallHarnessResult{}, startErr
		}
		if role == "daemon" {
			daemon = process
		}
	}
	if daemon == nil {
		return uciInstallHarnessResult{}, errors.New("UCI install harness did not launch a daemon")
	}
	if err := ctx.Err(); err != nil {
		return uciInstallHarnessResult{}, fmt.Errorf("UCI install harness context before MCP driver: %w", err)
	}

	readinessCtx, cancelReadiness := context.WithTimeout(ctx, request.ReadinessTimeout)
	defer cancelReadiness()
	toolNames, driverErr := request.MCPDriver.InitializeAndList(readinessCtx, uciMCPStdioProcess{
		PID:    daemon.command.Process.Pid,
		Stdin:  daemon.stdin,
		Stdout: daemon.stdout,
	})
	if driverErr != nil {
		if contextErr := readinessCtx.Err(); contextErr != nil {
			return uciInstallHarnessResult{}, contextErr
		}
		return uciInstallHarnessResult{}, fmt.Errorf("run UCI standard MCP driver: %w", driverErr)
	}
	if contextErr := readinessCtx.Err(); contextErr != nil {
		return uciInstallHarnessResult{}, contextErr
	}

	return uciInstallHarnessResult{ToolNames: append([]string(nil), toolNames...)}, nil
}

func uciInstallHarnessRequestScenario(request uciInstallHarnessRequest) (uciInstallHarnessScenario, error) {
	switch request.Scenario {
	case "", uciInstallHarnessScenarioStandardMCP:
		return uciInstallHarnessScenarioStandardMCP, nil
	case uciInstallHarnessScenarioMaterialize:
		return uciInstallHarnessScenarioMaterialize, nil
	default:
		return "", fmt.Errorf("UCI install harness scenario = %q", request.Scenario)
	}
}

func newUCIInstallHarnessInstallation(ctx context.Context, request uciInstallHarnessRequest) (_ *uciInstallHarnessInstallation, err error) {
	components, err := uciPrepareInstallHarnessComponents(request)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("UCI install harness context before installation: %w", err)
	}
	if err := os.Mkdir(request.InstallRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create UCI install root %q: %w", request.InstallRoot, err)
	}
	installed := false
	defer func() {
		if err != nil && installed {
			_ = os.RemoveAll(request.InstallRoot)
		}
	}()
	installed = true

	for _, component := range components {
		if err := uciMaterializeInstallHarnessComponent(component); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("UCI install harness context before launch: %w", err)
	}
	tree, err := newUCIInstallHarnessProcessTree()
	if err != nil {
		return nil, err
	}

	byRole := make(map[string]uciInstallHarnessComponent, len(components))
	for _, component := range components {
		byRole[component.role] = component
	}
	return &uciInstallHarnessInstallation{
		installRoot: request.InstallRoot,
		components:  byRole,
		environment: uciInstallHarnessEnvironment(request.Environment),
		tree:        tree,
	}, nil
}

func (installation *uciInstallHarnessInstallation) Executable(role string) (string, error) {
	if installation == nil {
		return "", errors.New("UCI install harness installation is nil")
	}
	component, found := installation.components[role]
	if !found {
		return "", fmt.Errorf("UCI install harness component %q is unavailable", role)
	}
	return component.installedExecutable, nil
}

func (installation *uciInstallHarnessInstallation) Start(ctx context.Context, request uciInstalledHarnessLaunchRequest) (*uciStartedInstallHarnessProcess, error) {
	if installation == nil || installation.tree == nil {
		return nil, errors.New("UCI install harness installation is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("UCI install harness context while launching %s: %w", request.Role, err)
	}
	installation.launchMu.Lock()
	defer installation.launchMu.Unlock()
	component, found := installation.components[request.Role]
	if !found {
		return nil, fmt.Errorf("UCI install harness component %q is unavailable", request.Role)
	}
	component.command.Args = append([]string(nil), request.Args...)
	environment := append([]string(nil), installation.environment...)
	environment = append(environment, request.Environment...)
	process, err := uciStartInstalledHarnessComponent(installation.tree, component, environment, request.WorkingDirectory, request.WithStdio)
	if process != nil {
		installation.processesMu.Lock()
		installation.processes = append(installation.processes, process)
		installation.processesMu.Unlock()
	}
	return process, err
}

func (installation *uciInstallHarnessInstallation) Close() error {
	if installation == nil {
		return nil
	}
	installation.closeOnce.Do(func() {
		installation.processesMu.Lock()
		processes := append([]*uciStartedInstallHarnessProcess(nil), installation.processes...)
		installation.processesMu.Unlock()
		installation.closeErr = uciCleanupInstallHarness(installation.tree, processes)
		if removeErr := os.RemoveAll(installation.installRoot); removeErr != nil {
			installation.closeErr = errors.Join(installation.closeErr, fmt.Errorf("remove UCI install root %q: %w", installation.installRoot, removeErr))
		}
	})
	return installation.closeErr
}

func (installation *uciInstallHarnessInstallation) ObservedPIDsForExecutable(executable string) []int {
	if installation == nil || installation.tree == nil {
		return nil
	}
	return installation.tree.ObservedPIDsForExecutable(executable)
}

func uciValidateInstallHarnessRequest(ctx context.Context, request uciInstallHarnessRequest) error {
	if uciInstallHarnessNilInterface(ctx) {
		return errors.New("UCI install harness context is nil")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("UCI install harness context: %w", err)
	}
	if request.Version != uciInstallHarnessVersionV1 {
		return fmt.Errorf("UCI install harness version = %q, want %q", request.Version, uciInstallHarnessVersionV1)
	}
	scenario, err := uciInstallHarnessRequestScenario(request)
	if err != nil {
		return err
	}
	if request.InstallRoot == "" {
		return errors.New("UCI install harness install root is empty")
	}
	if !filepath.IsAbs(request.InstallRoot) {
		return fmt.Errorf("UCI install harness install root %q is not absolute", request.InstallRoot)
	}
	if filepath.Clean(request.InstallRoot) != request.InstallRoot {
		return fmt.Errorf("UCI install harness install root %q is not clean", request.InstallRoot)
	}
	for _, component := range []struct {
		role    string
		command uciInstallHarnessCommand
	}{
		{role: "server", command: request.Server},
		{role: "daemon", command: request.Daemon},
		{role: "parser", command: request.Parser},
	} {
		if strings.TrimSpace(component.command.Executable) == "" {
			return fmt.Errorf("UCI install harness %s executable is empty", component.role)
		}
	}
	if request.ReadinessTimeout <= 0 {
		return fmt.Errorf("UCI install harness readiness timeout = %s, want a positive duration", request.ReadinessTimeout)
	}
	if scenario == uciInstallHarnessScenarioStandardMCP && uciInstallHarnessNilInterface(request.MCPDriver) {
		return errors.New("UCI install harness MCP driver is nil")
	}
	if _, err := os.Lstat(request.InstallRoot); err == nil {
		return fmt.Errorf("UCI install harness install root %q already exists", request.InstallRoot)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect UCI install root %q: %w", request.InstallRoot, err)
	}
	return nil
}

func uciPrepareInstallHarnessComponents(request uciInstallHarnessRequest) ([]uciInstallHarnessComponent, error) {
	components := make([]uciInstallHarnessComponent, 0, 3)
	for _, component := range []struct {
		role    string
		command uciInstallHarnessCommand
	}{
		{role: "server", command: request.Server},
		{role: "daemon", command: request.Daemon},
		{role: "parser", command: request.Parser},
	} {
		info, err := os.Stat(component.command.Executable)
		if err != nil {
			return nil, fmt.Errorf("inspect UCI %s executable %q: %w", component.role, component.command.Executable, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("UCI %s executable %q is not a regular file", component.role, component.command.Executable)
		}
		components = append(components, uciInstallHarnessComponent{
			role:                component.role,
			command:             component.command,
			installedExecutable: filepath.Join(request.InstallRoot, component.role, component.role+filepath.Ext(component.command.Executable)),
		})
	}
	return components, nil
}

func uciMaterializeInstallHarnessComponent(component uciInstallHarnessComponent) error {
	if err := os.Mkdir(filepath.Dir(component.installedExecutable), 0o700); err != nil {
		return fmt.Errorf("create UCI %s installation directory: %w", component.role, err)
	}

	source, err := os.Open(component.command.Executable)
	if err != nil {
		return fmt.Errorf("open UCI %s executable %q: %w", component.role, component.command.Executable, err)
	}
	defer source.Close()

	info, err := source.Stat()
	if err != nil {
		return fmt.Errorf("stat UCI %s executable %q: %w", component.role, component.command.Executable, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("UCI %s executable %q is not a regular file", component.role, component.command.Executable)
	}

	destination, err := os.OpenFile(component.installedExecutable, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create installed UCI %s executable %q: %w", component.role, component.installedExecutable, err)
	}
	if _, copyErr := io.Copy(destination, source); copyErr != nil {
		return errors.Join(
			fmt.Errorf("copy UCI %s executable to %q: %w", component.role, component.installedExecutable, copyErr),
			destination.Close(),
		)
	}
	if err := destination.Close(); err != nil {
		return fmt.Errorf("close installed UCI %s executable %q: %w", component.role, component.installedExecutable, err)
	}
	if err := os.Chmod(component.installedExecutable, info.Mode().Perm()); err != nil {
		return fmt.Errorf("set installed UCI %s executable mode: %w", component.role, err)
	}
	return nil
}

func uciInstallHarnessEnvironment(entries []string) []string {
	environment := make([]string, 0, len(os.Environ())+len(entries))
	environment = append(environment, os.Environ()...)
	environment = append(environment, entries...)
	return environment
}

func uciStartInstalledHarnessComponent(tree *uciInstallHarnessProcessTree, component uciInstallHarnessComponent, environment []string, workingDirectory string, withStdio bool) (started *uciStartedInstallHarnessProcess, err error) {
	if tree == nil {
		return nil, errors.New("UCI install harness process tree is nil")
	}

	arguments := append([]string(nil), component.command.Args...)
	command := exec.Command(component.installedExecutable, arguments...)
	if err := uciConfigureInstallHarnessCommand(command); err != nil {
		return nil, fmt.Errorf("configure installed UCI %s process tree: %w", component.role, err)
	}
	command.Dir = workingDirectory
	command.Env = append([]string(nil), environment...)
	command.Stderr = os.Stderr

	var stdin io.WriteCloser
	var stdout io.ReadCloser
	if withStdio {
		stdin, err = command.StdinPipe()
		if err != nil {
			return nil, fmt.Errorf("open UCI %s stdin pipe: %w", component.role, err)
		}
		stdout, err = command.StdoutPipe()
		if err != nil {
			return nil, errors.Join(
				fmt.Errorf("open UCI %s stdout pipe: %w", component.role, err),
				stdin.Close(),
			)
		}
	}
	if err := command.Start(); err != nil {
		return nil, errors.Join(
			fmt.Errorf("start installed UCI %s executable %q: %w", component.role, component.installedExecutable, err),
			uciCloseInstallHarnessPipe(stdin),
			uciCloseInstallHarnessPipe(stdout),
		)
	}

	started = &uciStartedInstallHarnessProcess{
		command: command,
		stdin:   stdin,
		stdout:  stdout,
	}
	if err := tree.Attach(command.Process); err != nil {
		return started, fmt.Errorf("attach installed UCI %s process tree: %w", component.role, err)
	}
	return started, nil
}

func uciCleanupInstallHarness(tree *uciInstallHarnessProcessTree, processes []*uciStartedInstallHarnessProcess) error {
	var cleanupErrors []error
	for _, process := range processes {
		if err := process.closePipes(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if tree != nil {
		if err := tree.Close(); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("terminate UCI install harness process trees: %w", err))
		}
	}
	for _, process := range processes {
		if err := process.wait(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	return errors.Join(cleanupErrors...)
}

func (process *uciStartedInstallHarnessProcess) closePipes() error {
	process.closePipesOnce.Do(func() {
		process.closePipesErr = errors.Join(
			uciCloseInstallHarnessPipe(process.stdin),
			uciCloseInstallHarnessPipe(process.stdout),
		)
	})
	return process.closePipesErr
}

func (process *uciStartedInstallHarnessProcess) wait() error {
	process.waitOnce.Do(func() {
		process.waitErr = process.command.Wait()
	})
	if process.waitErr == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(process.waitErr, &exitErr) {
		return nil
	}
	return fmt.Errorf("wait for installed UCI process %d: %w", process.command.Process.Pid, process.waitErr)
}

func uciCloseInstallHarnessPipe(pipe io.Closer) error {
	if pipe == nil {
		return nil
	}
	if err := pipe.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		return err
	}
	return nil
}

func uciInstallHarnessNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
