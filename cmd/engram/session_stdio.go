package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/dispatcher"
	"github.com/thebtf/engram/internal/module/registry"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
)

const (
	sessionMemoryFlag        = "--session-memory"
	sessionMemoryFrameBytes  = 1024 * 1024
	sessionMemoryIDBytes     = 16 * 1024
	sessionMemoryInFlight    = 8
	sessionMemoryDescription = "Session-local MEMORY only; not Workspace, relay or Loom. Uses the configured gRPC backend and existing project identity."
)

// Only the callable surface changes here; the core owns auth, identity and handlers.
type sessionMemoryModule struct{ *engramcore.Module }

func (*sessionMemoryModule) Tools() []module.ToolDef { return nil }

func sessionMemoryTool(name string) bool {
	switch name {
	case "store", "recall", "recall_memory", "feedback", "store_rule", "list_rules",
		"get_memory_brief", "query_principal_memory", "continuity_slot":
		return true
	default:
		return false
	}
}

func (m *sessionMemoryModule) ProxyTools(ctx context.Context, p muxcore.ProjectContext) ([]module.ToolDef, error) {
	tools, err := m.Module.ProxyTools(ctx, p)
	if err != nil {
		return nil, &module.RequiredProxyToolsError{Cause: err}
	}
	memory := tools[:0]
	for _, tool := range tools {
		if sessionMemoryTool(tool.Name) {
			memory = append(memory, tool)
		}
	}
	return memory, nil
}

func (m *sessionMemoryModule) ProxyHandleTool(ctx context.Context, p muxcore.ProjectContext, name string, args json.RawMessage) (json.RawMessage, error) {
	if !sessionMemoryTool(name) {
		return nil, &module.ModuleError{Code: "tool_not_found", Message: sessionMemoryDescription}
	}
	return m.Module.ProxyHandleTool(ctx, p, name, args)
}

func runSessionMemory(ctx context.Context, input io.ReadCloser, output io.WriteCloser) error {
	var outputMu sync.Mutex
	return runSessionMemoryWithOutputLock(ctx, input, output, &outputMu)
}

func runSessionMemoryWithOutputLock(ctx context.Context, input io.ReadCloser, output io.WriteCloser, outputMu sync.Locker) error {
	clientID, err := directClientInstanceID()
	if err != nil {
		return fmt.Errorf("client identity initialization: %w", err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	core := &sessionMemoryModule{engramcore.NewModuleWithClientInstanceID(clientID)}
	if err := core.Init(ctx, module.ModuleDeps{Logger: logger, DaemonCtx: ctx}); err != nil {
		return err
	}
	defer core.Shutdown(context.Background())
	reg := registry.New()
	if err := reg.Register(core); err != nil {
		return err
	}
	reg.Freeze()
	disp := dispatcher.NewWithVersion(reg, logger, daemonVersion)
	project := muxcore.ProjectContext{ID: cwd, Cwd: cwd}
	disp.OnProjectConnect(project)
	defer disp.OnProjectDisconnect(project.ID)
	return serveSessionMemory(ctx, input, output, disp, project, outputMu)
}

type sessionMemoryRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type sessionMemoryOperation struct {
	cancel context.CancelFunc
	method string
}

// Numbers use exact decimal keys, like muxcore's correlation registry, not float64.
func sessionMemoryID(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > sessionMemoryIDBytes || !json.Valid(raw) {
		return "", false
	}
	if raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) != nil {
			return "", false
		}
		return "s:" + value, true
	}
	if raw[0] != '-' && (raw[0] < '0' || raw[0] > '9') {
		return "", false
	}
	mantissa, exponent := string(raw), new(big.Int)
	if index := strings.IndexAny(mantissa, "eE"); index >= 0 {
		if _, ok := exponent.SetString(mantissa[index+1:], 10); !ok {
			return "", false
		}
		mantissa = mantissa[:index]
	}
	negative := strings.HasPrefix(mantissa, "-")
	mantissa = strings.TrimPrefix(mantissa, "-")
	if index := strings.IndexByte(mantissa, '.'); index >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(mantissa)-index-1)))
		mantissa = mantissa[:index] + mantissa[index+1:]
	}
	mantissa = strings.TrimLeft(mantissa, "0")
	if mantissa == "" {
		return "n:0", true
	}
	trimmed := strings.TrimRight(mantissa, "0")
	exponent.Add(exponent, big.NewInt(int64(len(mantissa)-len(trimmed))))
	if negative {
		trimmed = "-" + trimmed
	}
	return "n:" + trimmed + "e" + exponent.String(), true
}

func sessionMemoryError(id json.RawMessage, code int, message string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	raw, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	})
	return raw
}

func sessionMemoryEnvelope(frame []byte, req *sessionMemoryRequest) bool {
	decoder := json.NewDecoder(bytes.NewReader(frame))
	opener, err := decoder.Token()
	if err != nil || opener != json.Delim('{') {
		return false
	}
	seen := make(map[string]bool)
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil || seen[key.(string)] {
			return false
		}
		seen[key.(string)] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
		if req != nil {
			switch key {
			case "jsonrpc":
				if json.Unmarshal(value, &req.JSONRPC) != nil {
					return false
				}
			case "id":
				req.ID = value
			case "method":
				if json.Unmarshal(value, &req.Method) != nil {
					return false
				}
			case "params":
				req.Params = value
			}
		}
	}
	return !seen["result"] && !seen["error"]
}

func serveSessionMemory(parent context.Context, input io.ReadCloser, output io.WriteCloser, disp *dispatcher.Dispatcher, project muxcore.ProjectContext, outputMu sync.Locker) error {
	ctx, cancel := context.WithCancel(parent)
	closed := make(chan struct{})
	context.AfterFunc(ctx, func() {
		defer close(closed)
		_ = input.Close()
		_ = output.Close()
	})
	var workers sync.WaitGroup
	defer func() { cancel(); <-closed; workers.Wait() }()
	var activeMu sync.Mutex
	active := make(map[string]sessionMemoryOperation)
	var failureOnce sync.Once
	var failure error
	// Callers serialize frames, but never hold activeMu across a blocking write.
	writeFrame := func(raw []byte) {
		if len(raw) == 0 || ctx.Err() != nil {
			return
		}
		n, err := output.Write(append(raw, '\n'))
		if err == nil && n != len(raw)+1 {
			err = io.ErrShortWrite
		}
		if err != nil {
			failureOnce.Do(func() { failure = fmt.Errorf("memory stdio output: %w", err) })
			cancel()
		}
	}
	write := func(raw []byte) {
		outputMu.Lock()
		defer outputMu.Unlock()
		writeFrame(raw)
	}
	respond := func(requestCtx context.Context, key string, raw []byte) {
		outputMu.Lock()
		defer outputMu.Unlock()
		activeMu.Lock()
		live := requestCtx.Err() == nil && ctx.Err() == nil
		// This is the response commit: cancellation either won above, or is now
		// too late. Release the ID before any response bytes become observable.
		delete(active, key)
		activeMu.Unlock()
		if live {
			writeFrame(raw)
		}
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), sessionMemoryFrameBytes+2)
	for scanner.Scan() {
		if ctx.Err() != nil {
			break
		}
		frame := scanner.Bytes()
		if len(frame) > sessionMemoryFrameBytes {
			return errors.New("memory stdio frame exceeds 1 MiB")
		}
		var req sessionMemoryRequest
		if !utf8.Valid(frame) || !json.Valid(frame) {
			write(sessionMemoryError(nil, -32700, "invalid JSON frame"))
			continue
		}
		if !sessionMemoryEnvelope(frame, &req) || req.JSONRPC != "2.0" || req.Method == "" {
			write(sessionMemoryError(nil, -32600, "invalid request"))
			continue
		}
		key, validID := sessionMemoryID(req.ID)
		if len(req.ID) != 0 && !validID {
			write(sessionMemoryError(nil, -32600, "ID must be a bounded string or number"))
			continue
		}
		if len(req.Params) != 0 && req.Params[0] != '{' && req.Params[0] != '[' {
			if validID {
				write(sessionMemoryError(req.ID, -32602, "params must be an object or array"))
			}
			continue
		}
		if strings.HasPrefix(req.Method, "notifications/") && validID {
			write(sessionMemoryError(req.ID, -32600, "notification must not have an ID"))
			continue
		}
		if req.Method == "notifications/cancelled" {
			var params map[string]json.RawMessage
			if sessionMemoryEnvelope(req.Params, nil) && json.Unmarshal(req.Params, &params) == nil {
				if target, ok := sessionMemoryID(params["requestId"]); ok {
					activeMu.Lock()
					if op, found := active[target]; found && op.method != "initialize" {
						op.cancel()
					}
					activeMu.Unlock()
				}
			}
			continue
		}
		if !validID {
			// Client notifications never invoke request handlers or produce responses.
			continue
		}
		requestCtx, requestCancel := context.WithCancel(ctx)
		activeMu.Lock()
		_, duplicate := active[key]
		full := len(active) >= sessionMemoryInFlight
		if !duplicate && !full {
			active[key] = sessionMemoryOperation{requestCancel, req.Method}
		}
		activeMu.Unlock()
		if duplicate || full {
			requestCancel()
			if duplicate {
				write(sessionMemoryError(nil, -32600, "duplicate active request ID"))
			} else {
				write(sessionMemoryError(req.ID, -32000, "memory in-flight limit reached"))
			}
			continue
		}
		// Canonical envelope prevents encoding/json's case-insensitive struct keys
		// from changing the ID or method admitted by this transport boundary.
		raw, _ := json.Marshal(req)
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer requestCancel()
			response, _ := disp.HandleRequest(requestCtx, project, raw)
			if req.Method == "initialize" {
				var envelope struct {
					JSONRPC string          `json:"jsonrpc"`
					ID      json.RawMessage `json:"id"`
					Result  map[string]any  `json:"result"`
				}
				if json.Unmarshal(response, &envelope) == nil && envelope.Result != nil {
					envelope.Result["instructions"] = sessionMemoryDescription
					response, _ = json.Marshal(envelope)
				}
			}
			if len(response) > sessionMemoryFrameBytes {
				response = sessionMemoryError(req.ID, -32603, "memory response exceeds 1 MiB")
			}
			respond(requestCtx, key, response)
		}()
	}
	cancel()
	<-closed
	workers.Wait()
	if failure != nil {
		return failure
	}
	if parent.Err() != nil {
		return nil
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, os.ErrClosed) {
		return fmt.Errorf("memory stdio input: %w", err)
	}
	return nil
}
