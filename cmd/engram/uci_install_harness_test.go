package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	uciInstallHarnessHelperTest            = "^TestUCIInstallHarnessProcessHelper$"
	uciInstallHarnessHelperAuditDir        = "ENGRAM_UCI_INSTALL_HARNESS_TEST_AUDIT_DIR"
	uciInstallHarnessHelperProbeEnv        = "ENGRAM_UCI_INSTALL_HARNESS_TEST_PROBE"
	uciInstallHarnessHelperProbeValue      = "value with spaces Кириллица"
	uciInstallHarnessReadinessRaceHeadroom = 2 * time.Second
	uciInstallHarnessWindowsStartupTimeout = 15 * time.Second
)

var (
	uciInstallHarnessHelperRole    = flag.String("uci-install-helper-role", "", "UCI install-harness child role")
	uciInstallHarnessHelperMode    = flag.String("uci-install-helper-mode", "", "UCI install-harness child mode")
	uciInstallHarnessHelperLiteral = flag.String("uci-install-helper-literal", "", "UCI install-harness argv probe")
)

var (
	_ func(context.Context, uciInstallHarnessRequest) (uciInstallHarnessResult, error) = runUCIInstallHarness
	_ uciStandardMCPDriver                                                             = uciInstallHarnessMCPProbe{}
)

// The harness is Windows-native proof. Keeping these tests compiled elsewhere
// preserves the RED API contract while avoiding a claim that another OS proves
// the Windows installation path.
func TestUCIInstallHarnessRunsBuiltComponentsThroughExternalStdio(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows installation proof")
	}

	installRoot := filepath.Join(t.TempDir(), "UCI install Кириллица")
	auditDir := t.TempDir()
	request := uciInstallHarnessTestRequest(t, installRoot, auditDir, "serve", 4*time.Second)

	result, err := runUCIInstallHarness(context.Background(), request)
	if err != nil {
		t.Fatalf("run built UCI installation harness: %v", err)
	}
	if !reflect.DeepEqual(result.ToolNames, []string{"uci_install_probe"}) {
		t.Fatalf("MCP tools/list names = %#v, want the actual stdio probe tool", result.ToolNames)
	}

	audits := map[string]uciInstallHarnessChildAudit{}
	for _, component := range []struct {
		role string
		want uciInstallHarnessCommand
	}{
		{role: "server", want: request.Server},
		{role: "daemon", want: request.Daemon},
		{role: "parser", want: request.Parser},
	} {
		uciWaitForInstallHarnessAudit(t, auditDir, component.role)
		audit := uciReadInstallHarnessAudit(t, auditDir, component.role)
		if audit.PID <= 0 || audit.PID == os.Getpid() {
			t.Fatalf("%s process PID = %d; harness must launch an external child", component.role, audit.PID)
		}
		if !reflect.DeepEqual(audit.Args, component.want.Args) {
			t.Fatalf("%s argv = %#v, want %#v", component.role, audit.Args, component.want.Args)
		}
		if audit.Probe != uciInstallHarnessHelperProbeValue {
			t.Fatalf("%s environment probe = %q, want %q", component.role, audit.Probe, uciInstallHarnessHelperProbeValue)
		}
		uciRequireInstalledExecutable(t, installRoot, component.role, audit.Executable)
		audits[component.role] = audit
	}
	if audits["server"].Executable == audits["daemon"].Executable ||
		audits["server"].Executable == audits["parser"].Executable ||
		audits["daemon"].Executable == audits["parser"].Executable {
		t.Fatalf("materialized component executables must be distinct: %#v", audits)
	}
	if !reflect.DeepEqual(audits["daemon"].Methods, []string{"initialize", "notifications/initialized", "tools/list"}) {
		t.Fatalf("daemon stdio methods = %#v, want initialize/initialized/tools/list", audits["daemon"].Methods)
	}
	uciRequireInstallRootRemoved(t, installRoot)
}

func TestUCIInstallHarnessBoundsReadinessAndPropagatesCancellation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows installation proof")
	}

	t.Run("readiness deadline", func(t *testing.T) {
		installRoot := filepath.Join(t.TempDir(), "UCI readiness deadline Кириллица")
		auditDir := t.TempDir()
		request := uciInstallHarnessTestRequest(t, installRoot, auditDir, "stall", 2*time.Second)
		readinessElapsed := make(chan time.Duration, 1)
		request.MCPDriver = uciInstallHarnessDeadlineProbe{elapsed: readinessElapsed}
		outer, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()

		_, err := runUCIInstallHarness(outer, request)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unready stdio harness error = %v, want wrapped context deadline", err)
		}
		// The driver boundary is the readiness operation. Materialization and
		// process-tree teardown are outside that budget and slower under -race.
		select {
		case elapsed := <-readinessElapsed:
			if maximum := request.ReadinessTimeout + uciInstallHarnessReadinessRaceHeadroom; elapsed > maximum {
				t.Fatalf("readiness deadline took %s, want no more than %s", elapsed, maximum)
			}
		default:
			t.Fatal("harness returned before its readiness driver started")
		}
		audit := uciReadInstallHarnessAudit(t, auditDir, "daemon")
		if !reflect.DeepEqual(audit.Methods, []string{"initialize"}) {
			t.Fatalf("stalled daemon methods = %#v, want actual initialize before deadline", audit.Methods)
		}
		uciRequireInstallRootRemoved(t, installRoot)
	})

	t.Run("caller cancellation", func(t *testing.T) {
		installRoot := filepath.Join(t.TempDir(), "UCI caller cancellation Кириллица")
		auditDir := t.TempDir()
		request := uciInstallHarnessTestRequest(t, installRoot, auditDir, "stall", 4*time.Second)
		driverStarted := make(chan struct{})
		request.MCPDriver = uciInstallHarnessStartedProbe{started: driverStarted}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		completed := false
		t.Cleanup(func() {
			if completed {
				return
			}
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
			}
		})
		go func() {
			_, err := runUCIInstallHarness(ctx, request)
			done <- err
		}()

		// These are three coverage-shaped copies of the Windows test binary,
		// launched serially before the driver owns its readiness deadline.
		// Keep startup bounded independently from the post-driver audit.
		select {
		case <-driverStarted:
		case err := <-done:
			completed = true
			t.Fatalf("install harness stopped before MCP driver started: %v", err)
		case <-time.After(uciInstallHarnessWindowsStartupTimeout):
			t.Fatal("install harness did not reach its MCP driver")
		}
		uciWaitForInstallHarnessAudit(t, auditDir, "daemon")
		cancel()
		select {
		case err := <-done:
			completed = true
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled stdio harness error = %v, want wrapped context cancellation", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled install harness did not stop its process tree")
		}
		uciRequireInstallRootRemoved(t, installRoot)
	})
}

func TestUCIInstallHarnessRejectsUnversionedOrDriverlessRequest(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows installation proof")
	}

	for _, test := range []struct {
		name   string
		mutate func(*uciInstallHarnessRequest)
	}{
		{
			name: "unversioned request",
			mutate: func(request *uciInstallHarnessRequest) {
				request.Version = "uci-install-harness/v0"
			},
		},
		{
			name: "missing standard stdio driver",
			mutate: func(request *uciInstallHarnessRequest) {
				request.MCPDriver = nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			installRoot := filepath.Join(t.TempDir(), "UCI rejected request Кириллица")
			request := uciInstallHarnessTestRequest(t, installRoot, t.TempDir(), "serve", time.Second)
			test.mutate(&request)

			if _, err := runUCIInstallHarness(context.Background(), request); err == nil {
				t.Fatal("invalid request was accepted; an in-process/package-direct substitute must not bypass the versioned stdio driver")
			}
			uciRequireInstallRootRemoved(t, installRoot)
		})
	}
}

// TestUCIInstallHarnessProcessHelper turns an already-built test executable
// into three independently launched fixture binaries. It models only process
// plumbing: parser input or language support is intentionally not exercised;
// T046 owns parser behavior and JS/TS/TSX claims.
func TestUCIInstallHarnessProcessHelper(t *testing.T) {
	if *uciInstallHarnessHelperRole == "" {
		return
	}

	audit := uciInstallHarnessChildAudit{
		Role:       *uciInstallHarnessHelperRole,
		PID:        os.Getpid(),
		Executable: os.Args[0],
		Args:       append([]string(nil), os.Args[1:]...),
		Probe:      os.Getenv(uciInstallHarnessHelperProbeEnv),
	}
	uciWriteInstallHarnessAudit(t, audit)

	switch audit.Role {
	case "server", "parser":
		for {
			time.Sleep(time.Hour)
		}
	case "daemon":
		uciServeInstallHarnessMCP(t, &audit)
	default:
		t.Fatalf("unknown UCI install-harness helper role %q", audit.Role)
	}
}

// uciInstallHarnessMCPProbe is intentionally a wire client, not an in-process
// module or service substitute. Its PID and pipes checks make a direct call
// unable to satisfy the installed-path contract.
type uciInstallHarnessMCPProbe struct{}

// uciInstallHarnessDeadlineProbe observes only the driver's readiness window.
type uciInstallHarnessDeadlineProbe struct {
	elapsed chan<- time.Duration
}

type uciInstallHarnessStartedProbe struct {
	started chan<- struct{}
}

func (probe uciInstallHarnessStartedProbe) InitializeAndList(ctx context.Context, process uciMCPStdioProcess) ([]string, error) {
	close(probe.started)
	return (uciInstallHarnessMCPProbe{}).InitializeAndList(ctx, process)
}

func (probe uciInstallHarnessDeadlineProbe) InitializeAndList(ctx context.Context, process uciMCPStdioProcess) ([]string, error) {
	started := time.Now()
	tools, err := (uciInstallHarnessMCPProbe{}).InitializeAndList(ctx, process)
	probe.elapsed <- time.Since(started)
	return tools, err
}

func (uciInstallHarnessMCPProbe) InitializeAndList(ctx context.Context, process uciMCPStdioProcess) ([]string, error) {
	if process.PID <= 0 || process.PID == os.Getpid() {
		return nil, fmt.Errorf("MCP driver received non-child PID %d", process.PID)
	}
	if process.Stdin == nil || process.Stdout == nil {
		return nil, errors.New("MCP driver received no child stdio pipes")
	}

	writer := bufio.NewWriter(process.Stdin)
	scanner := bufio.NewScanner(process.Stdout)
	if err := uciWriteMCPFrame(writer, map[string]any{
		"jsonrpc": "2.0",
		"id":      "initialize",
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]string{"name": "uci-install-harness", "version": "1"},
		},
	}); err != nil {
		return nil, fmt.Errorf("write initialize: %w", err)
	}
	initialize, err := uciReadMCPResponse(ctx, scanner, "initialize")
	if err != nil {
		return nil, fmt.Errorf("read initialize: %w", err)
	}
	var initialized struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(initialize, &initialized); err != nil {
		return nil, fmt.Errorf("decode initialize result: %w", err)
	}
	if initialized.ProtocolVersion != "2025-11-25" {
		return nil, fmt.Errorf("initialize protocol version = %q", initialized.ProtocolVersion)
	}

	if err := uciWriteMCPFrame(writer, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	}); err != nil {
		return nil, fmt.Errorf("write initialized notification: %w", err)
	}
	if err := uciWriteMCPFrame(writer, map[string]any{
		"jsonrpc": "2.0",
		"id":      "tools/list",
		"method":  "tools/list",
		"params":  map[string]any{},
	}); err != nil {
		return nil, fmt.Errorf("write tools/list: %w", err)
	}
	listed, err := uciReadMCPResponse(ctx, scanner, "tools/list")
	if err != nil {
		return nil, fmt.Errorf("read tools/list: %w", err)
	}
	var toolList struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listed, &toolList); err != nil {
		return nil, fmt.Errorf("decode tools/list result: %w", err)
	}
	tools := make([]string, len(toolList.Tools))
	for i, tool := range toolList.Tools {
		tools[i] = tool.Name
	}
	return tools, nil
}

type uciInstallHarnessChildAudit struct {
	Role       string   `json:"role"`
	PID        int      `json:"pid"`
	Executable string   `json:"executable"`
	Args       []string `json:"args"`
	Probe      string   `json:"probe"`
	Methods    []string `json:"methods"`
}

func uciInstallHarnessTestRequest(t *testing.T, installRoot, auditDir, daemonMode string, readinessTimeout time.Duration) uciInstallHarnessRequest {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := func(role, mode string) uciInstallHarnessCommand {
		return uciInstallHarnessCommand{
			Executable: executable,
			Args: []string{
				"-test.run=" + uciInstallHarnessHelperTest,
				"-test.count=1",
				"-test.v=false",
				"-uci-install-helper-role=" + role,
				"-uci-install-helper-mode=" + mode,
				"-uci-install-helper-literal=spaced value Кириллица & | ; \"quoted\"",
			},
		}
	}
	return uciInstallHarnessRequest{
		Version:     uciInstallHarnessVersionV1,
		InstallRoot: installRoot,
		Server:      command("server", "serve"),
		Daemon:      command("daemon", daemonMode),
		Parser:      command("parser", "serve"),
		Environment: []string{
			uciInstallHarnessHelperAuditDir + "=" + auditDir,
			uciInstallHarnessHelperProbeEnv + "=" + uciInstallHarnessHelperProbeValue,
		},
		ReadinessTimeout: readinessTimeout,
		MCPDriver:        uciInstallHarnessMCPProbe{},
	}
}

func uciServeInstallHarnessMCP(t *testing.T, audit *uciInstallHarnessChildAudit) {
	t.Helper()
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Fatalf("decode MCP request: %v", err)
		}
		if request.JSONRPC != "2.0" {
			t.Fatalf("MCP request version = %q", request.JSONRPC)
		}
		audit.Methods = append(audit.Methods, request.Method)
		uciWriteInstallHarnessAudit(t, *audit)

		if *uciInstallHarnessHelperMode == "stall" && request.Method == "initialize" {
			for {
				time.Sleep(time.Hour)
			}
		}
		switch request.Method {
		case "initialize":
			uciWriteMCPResponse(t, request.ID, map[string]any{
				"protocolVersion": "2025-11-25",
				"capabilities":    map[string]any{},
				"serverInfo":      map[string]string{"name": "uci-install-harness-child", "version": "1"},
			})
		case "notifications/initialized":
		case "tools/list":
			uciWriteMCPResponse(t, request.ID, map[string]any{
				"tools": []map[string]any{{"name": "uci_install_probe", "description": "fixture stdio probe"}},
			})
		default:
			t.Fatalf("unexpected MCP method %q", request.Method)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read MCP request: %v", err)
	}
}

func uciWriteMCPFrame(writer *bufio.Writer, frame any) error {
	if err := json.NewEncoder(writer).Encode(frame); err != nil {
		return err
	}
	return writer.Flush()
}

func uciReadMCPResponse(ctx context.Context, scanner *bufio.Scanner, wantID string) (json.RawMessage, error) {
	type readResult struct {
		result json.RawMessage
		err    error
	}
	responses := make(chan readResult, 1)
	go func() {
		for scanner.Scan() {
			var response struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      string          `json:"id"`
				Result  json.RawMessage `json:"result"`
				Error   *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
				continue
			}
			if response.ID != wantID {
				continue
			}
			if response.JSONRPC != "2.0" {
				responses <- readResult{err: fmt.Errorf("response %s protocol = %q", wantID, response.JSONRPC)}
				return
			}
			if response.Error != nil {
				responses <- readResult{err: fmt.Errorf("response %s failed with %d: %s", wantID, response.Error.Code, response.Error.Message)}
				return
			}
			responses <- readResult{result: response.Result}
			return
		}
		if err := scanner.Err(); err != nil {
			responses <- readResult{err: err}
			return
		}
		responses <- readResult{err: fmt.Errorf("missing MCP response %s", wantID)}
	}()

	select {
	case response := <-responses:
		return response.result, response.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func uciWriteMCPResponse(t *testing.T, id json.RawMessage, result any) {
	t.Helper()
	response := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		t.Fatalf("write MCP response: %v", err)
	}
}

func uciWriteInstallHarnessAudit(t *testing.T, audit uciInstallHarnessChildAudit) {
	t.Helper()
	dir := os.Getenv(uciInstallHarnessHelperAuditDir)
	if dir == "" {
		t.Fatal("install-harness child audit directory is empty")
	}
	payload, err := json.Marshal(audit)
	if err != nil {
		t.Fatalf("marshal child audit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, audit.Role+".json"), payload, 0o600); err != nil {
		t.Fatalf("write child audit: %v", err)
	}
}

func uciReadInstallHarnessAudit(t *testing.T, dir, role string) uciInstallHarnessChildAudit {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(dir, role+".json"))
	if err != nil {
		t.Fatalf("read %s child audit: %v", role, err)
	}
	var audit uciInstallHarnessChildAudit
	if err := json.Unmarshal(payload, &audit); err != nil {
		t.Fatalf("decode %s child audit: %v", role, err)
	}
	return audit
}

func uciWaitForInstallHarnessAudit(t *testing.T, dir, role string) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		payload, err := os.ReadFile(filepath.Join(dir, role+".json"))
		if err == nil {
			var audit uciInstallHarnessChildAudit
			if json.Unmarshal(payload, &audit) == nil && audit.Role == role {
				return
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read %s child audit: %v", role, err)
		}
		select {
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s child audit", role)
		case <-tick.C:
		}
	}
}

func uciRequireInstalledExecutable(t *testing.T, installRoot, role, executable string) {
	t.Helper()
	relative, err := filepath.Rel(installRoot, executable)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("%s executable = %q, want a materialized path under %q", role, executable, installRoot)
	}
	if !strings.Contains(executable, " ") || !strings.Contains(executable, "Кириллица") {
		t.Fatalf("%s executable = %q, want spaces and Cyrillic in the launched path", role, executable)
	}
}

func uciRequireInstallRootRemoved(t *testing.T, installRoot string) {
	t.Helper()
	if _, err := os.Stat(installRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("install root %q remained after harness cleanup: %v", installRoot, err)
	}
}
