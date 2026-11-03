package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/taskcontext"
	muxcontrol "github.com/thebtf/mcp-mux/muxcore/control"
	"github.com/thebtf/mcp-mux/muxcore/ipc"
	"github.com/thebtf/mcp-mux/muxcore/serverid"
)

func runTaskContext(parent context.Context, started time.Time, input io.Reader, output io.Writer) error {
	defer traceTaskContextPhase("cli_total", started)
	ctx, cancel := context.WithDeadline(parent, started.Add(taskcontext.MaxDuration))
	defer cancel()
	if closer, ok := input.(io.Closer); ok {
		stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer stop()
	}
	request := taskcontext.Request{}
	response := taskcontext.Closed("unavailable", "INVALID_REQUEST")
	limit := taskcontext.DefaultMaxBytes
	if err := taskcontext.Decode(input, &request); err == nil && request.Validate() == nil {
		limit = request.OutputLimit()
		operation, done := request.Context(ctx, started)
		response = nativeTaskContext(operation, request)
		done()
	} else if ctx.Err() != nil {
		response = taskcontext.Closed("deadline_exceeded", "DEADLINE_EXCEEDED")
	}
	raw, err := taskcontext.Encode(response, limit)
	if err != nil {
		return err
	}
	_, err = output.Write(raw)
	return err
}

func nativeTaskContext(ctx context.Context, request taskcontext.Request) taskcontext.Response {
	closed := func(status, reason string) taskcontext.Response {
		if ctx.Err() != nil {
			return taskcontext.Closed("deadline_exceeded", "DEADLINE_EXCEEDED")
		}
		return taskcontext.Closed(status, reason)
	}
	if ctx.Err() != nil {
		return closed("deadline_exceeded", "DEADLINE_EXCEEDED")
	}
	phaseStarted := time.Now()
	instance, err := existingDirectClientInstanceID()
	traceTaskContextPhase("installation_identity", phaseStarted)
	if err != nil {
		return closed("not_ready", "INSTALLATION_NOT_READY")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return closed("context_required", "CONTEXT_REQUIRED")
	}
	// One namespace and ordinary owner identity, never a per-prompt owner.
	namespace := muxcoreInstallationNamespace(instance)
	connection, err := taskContextAdmission(ctx, namespace, cwd, instance)
	if err != nil {
		return closed("not_ready", "DAEMON_NOT_READY")
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		return closed("unavailable", "TRANSPORT_UNAVAILABLE")
	}
	reader := bufio.NewReaderSize(connection, 4096)
	phaseStarted = time.Now()
	if err := taskContextWrite(connection, 1, "initialize", map[string]any{"protocolVersion": "2024-11-05", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "engram-task-context", "version": daemonVersion}}); err != nil {
		return closed("unavailable", "TRANSPORT_UNAVAILABLE")
	}
	if _, err := taskContextRead(reader, "1"); err != nil {
		return closed("not_ready", "DAEMON_NOT_READY")
	}
	if err := taskContextWrite(connection, 0, "notifications/initialized", nil); err != nil {
		return closed("unavailable", "TRANSPORT_UNAVAILABLE")
	}
	traceTaskContextPhase("mcp_initialize", phaseStarted)
	request.DeadlineUnixMS = deadline.UnixMilli()
	phaseStarted = time.Now()
	if err := taskContextWrite(connection, 2, "tools/call", map[string]any{"name": "codebase_task_context", "arguments": request}); err != nil {
		return closed("unavailable", "TRANSPORT_UNAVAILABLE")
	}
	raw, err := taskContextRead(reader, "2")
	traceTaskContextPhase("mcp_tool_call", phaseStarted)
	if err != nil {
		return closed("not_ready", "OPERATION_NOT_READY")
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(raw, &result) != nil || result.IsError || len(result.Content) != 1 || result.Content[0].Type != "text" {
		return closed("not_ready", "OPERATION_NOT_READY")
	}
	var response taskcontext.Response
	if json.Unmarshal([]byte(result.Content[0].Text), &response) != nil || response.Validate() != nil {
		return closed("unavailable", "INVALID_RESPONSE")
	}
	if ctx.Err() != nil {
		return closed("deadline_exceeded", "DEADLINE_EXCEEDED")
	}
	return response
}

func taskContextDial(ctx context.Context, path string) (net.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) <= 0 {
		return nil, context.DeadlineExceeded
	}
	connection, err := ipc.DialTimeout(path, time.Until(deadline))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		connection.Close()
		return nil, err
	}
	return connection, nil
}

func taskContextControl(ctx context.Context, path string, request muxcontrol.Request, expectedPID int) (*muxcontrol.Response, error) {
	connection, err := taskContextDial(ctx, path)
	if err != nil {
		return nil, err
	}
	defer connection.Close()
	stop := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stop()
	if expectedPID != 0 {
		pid, err := muxcoreControlPeerPID(connection)
		if err != nil || pid != expectedPID {
			return nil, errors.New("daemon peer mismatch")
		}
	}
	return sendMuxcoreControlRequestAndReadResponse(ctx, connection, request)
}

func taskContextAdmission(ctx context.Context, namespace, cwd, instance string) (net.Conn, error) {
	path := serverid.DaemonControlPath("", namespace)
	phaseStarted := time.Now()
	marker, err := readMuxcoreDaemonVersionMarker(path + ".marker.json")
	if err != nil {
		return nil, err
	}
	client, err := clientDaemonIdentity()
	if err != nil {
		return nil, err
	}
	action, err := classifyDaemonConvergence(client, daemonConvergenceIdentity{ProductVersion: marker.ProductVersion, DaemonCompatEpoch: marker.DaemonCompatEpoch})
	if err != nil || action != daemonConvergenceJoin {
		return nil, errors.New("daemon is not compatible")
	}
	traceTaskContextPhase("daemon_identity", phaseStarted)
	phaseStarted = time.Now()
	status, err := taskContextControl(ctx, path, muxcontrol.Request{Cmd: "status"}, marker.PID)
	traceTaskContextPhase("control_status", phaseStarted)
	if err != nil || status == nil || !status.OK {
		return nil, errors.New("daemon status unavailable")
	}
	var identity muxcoreDaemonStatusIdentity
	if json.Unmarshal(status.Data, &identity) != nil || identity.PID != marker.PID || identity.DaemonGeneration != marker.DaemonGeneration || identity.ShuttingDown {
		return nil, errors.New("daemon generation mismatch")
	}
	mode := "global"
	switch selected := strings.ToLower(strings.TrimSpace(os.Getenv("MCP_MUX_DEFAULT_MODE"))); selected {
	case "cwd", "git", "global", "isolated":
		mode = selected
	}
	if os.Getenv("MCP_MUX_STATELESS") == "1" {
		mode = "global"
	}
	if os.Getenv("MCP_MUX_ISOLATED") == "1" {
		mode = "isolated"
	}
	environment := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			environment[key] = value
		}
	}
	environment[config.EnvClientInstanceID] = instance
	phaseStarted = time.Now()
	admission, err := taskContextControl(ctx, path, muxcontrol.Request{Cmd: "spawn", Cwd: cwd, Mode: mode, Env: environment}, marker.PID)
	traceTaskContextPhase("control_spawn", phaseStarted)
	if err != nil || admission == nil || !admission.OK || admission.Token == "" || admission.IPCPath == "" {
		return nil, errors.New("daemon admission unavailable")
	}
	phaseStarted = time.Now()
	connection, err := taskContextDial(ctx, admission.IPCPath)
	traceTaskContextPhase("ipc_connect", phaseStarted)
	if err != nil {
		return nil, err
	}
	deadline, _ := ctx.Deadline()
	if err := connection.SetDeadline(deadline); err != nil {
		connection.Close()
		return nil, err
	}
	if _, err := io.WriteString(connection, admission.Token+"\n"); err != nil {
		connection.Close()
		return nil, err
	}
	return connection, nil
}

func traceTaskContextPhase(stage string, started time.Time) {
	_ = json.NewEncoder(os.Stderr).Encode(struct {
		Diagnostic string `json:"diagnostic"`
		Stage      string `json:"stage"`
		ElapsedUS  int64  `json:"elapsed_us"`
	}{"native_task_phase", stage, time.Since(started).Microseconds()})
}

func taskContextWrite(writer io.Writer, id int, method string, params any) error {
	frame := struct {
		JSONRPC string `json:"jsonrpc"`
		ID      *int   `json:"id,omitempty"`
		Method  string `json:"method"`
		Params  any    `json:"params,omitempty"`
	}{JSONRPC: "2.0", Method: method, Params: params}
	if id != 0 {
		frame.ID = &id
	}
	return json.NewEncoder(writer).Encode(frame)
}

func taskContextRead(reader *bufio.Reader, want string) (json.RawMessage, error) {
	for range 16 {
		var raw []byte
		for {
			part, prefix, err := reader.ReadLine()
			if err != nil {
				return nil, err
			}
			if len(raw)+len(part) > 4*taskcontext.MaxOutputBytes {
				return nil, errors.New("oversized daemon frame")
			}
			raw = append(raw, part...)
			if !prefix {
				break
			}
		}
		var frame struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Result  json.RawMessage `json:"result"`
			Error   json.RawMessage `json:"error"`
		}
		if json.Unmarshal(raw, &frame) != nil || frame.JSONRPC != "2.0" {
			return nil, errors.New("invalid daemon frame")
		}
		if len(frame.ID) == 0 {
			continue
		}
		if string(frame.ID) != want || len(frame.Error) != 0 || len(frame.Result) == 0 {
			return nil, errors.New("daemon request refused")
		}
		return frame.Result, nil
	}
	return nil, errors.New("daemon notification bound exceeded")
}
