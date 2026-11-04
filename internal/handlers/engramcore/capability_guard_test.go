package engramcore

import (
	"context"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/module"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
)

func TestAdvertisedFieldsCoversOpenStateAndUnknownFields(t *testing.T) {
	var stateSchema []byte
	for _, tool := range mcp.SupportedToolCatalog() {
		if tool.Name == "set_state" {
			var err error
			stateSchema, err = json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if len(stateSchema) == 0 {
		t.Fatal("set_state schema missing")
	}
	for _, tc := range []struct {
		name, args string
		covered    bool
	}{
		{"open state slots", `{"action":"session","session_id":"s","state":{"focus":{"arbitrary":1},"execution":{},"horizons":{}}}`, true},
		{"unadvertised nested state", `{"action":"session","session_id":"s","state":{"unadvertised":true}}`, false},
		{"unadvertised action", `{"action":"future","state":{}}`, false},
		{"unadvertised top-level", `{"action":"project","new_field":true}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := advertisedFields(stateSchema, json.RawMessage(tc.args), false)
			if (err == nil) != tc.covered {
				t.Fatalf("error=%v, covered=%t", err, tc.covered)
			}
		})
	}
}

func TestPooledCapabilityNotSentAndUnknownOutcome(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"action":{"type":"string","enum":["write"]},"nested":{"type":"object","properties":{"value":{"type":"string"}}}}}`)
	for _, tc := range []struct {
		name, args, wantError string
		haveCatalog, failCall bool
		wantCalls             int
	}{
		{"first call discovers capability", `{"action":"write"}`, "", false, false, 1},
		{"unknown nested field", `{"action":"write","nested":{"new_field":true}}`, "CAPABILITY_NOT_SENT", true, false, 0},
		{"unsupported action", `{"action":"future"}`, "CAPABILITY_NOT_SENT", true, false, 0},
		{"current write", ` { "action":"write", "nested":{"value":"ok"} } `, "", true, false, 1},
		{"response lost", `{"action":"write"}`, "OUTCOME_UNKNOWN", true, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := &mockEngramServer{initResp: &pb.InitializeResponse{ServerVersion: "fixture-version", Tools: []*pb.ToolDefinition{{Name: "write_fixture", InputSchemaJson: schema}}}, callResp: &pb.CallToolResponse{ContentJson: []byte(`{"ok":true}`)}}
			if tc.failCall {
				fixture.callErr = status.Error(codes.Unavailable, "response lost")
			}
			_, mod, project := buildContractDispatcher(t, startMockGRPC(t, fixture))
			key, err := advisorProofKeyFor(project.Env["ENGRAM_URL"], "")
			if err != nil {
				t.Fatal(err)
			}
			if tc.haveCatalog {
				mod.refreshMu.Lock()
				mod.liveCatalog[key] = discoveredCatalog{version: "fixture-version", tools: []module.ToolDef{{Name: "write_fixture", InputSchema: schema}}}
				mod.refreshMu.Unlock()
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			_, err = mod.ProxyHandleTool(ctx, project, "write_fixture", json.RawMessage(tc.args))
			if tc.wantError == "" && err != nil {
				t.Fatalf("call: %v", err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("error=%v, want %s", err, tc.wantError)
			}
			fixture.mu.Lock()
			count, request := fixture.callCount, fixture.callReq
			fixture.mu.Unlock()
			if count != tc.wantCalls {
				t.Fatalf("CallTool count=%d, want %d", count, tc.wantCalls)
			}
			if request != nil && string(request.ArgumentsJson) != tc.args {
				t.Fatalf("arguments changed: %q", request.ArgumentsJson)
			}
		})
	}
}

func TestPooledCapabilityWaitsForInitializeAndHonorsCancellation(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"action":{"type":"string"}}}`)
	for _, cancelCall := range []bool{false, true} {
		name := "discovery completes"
		if cancelCall {
			name = "caller cancels"
		}
		t.Run(name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			fixture := &mockEngramServer{initStarted: started, initRelease: release, initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{Name: "write_fixture", InputSchemaJson: schema}}}}
			_, mod, project := buildContractDispatcher(t, startMockGRPC(t, fixture))
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := make(chan error, 1)
			go func() {
				_, err := mod.ProxyHandleTool(ctx, project, "write_fixture", json.RawMessage(`{"action":"write"}`))
				result <- err
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				close(release)
				t.Fatal("Initialize did not start")
			}
			select {
			case err := <-result:
				close(release)
				t.Fatalf("call returned before Initialize completed: %v", err)
			default:
			}
			if cancelCall {
				cancel()
			} else {
				close(release)
			}
			select {
			case err := <-result:
				if cancelCall && (err == nil || !strings.Contains(err.Error(), "CAPABILITY_NOT_SENT")) {
					t.Fatalf("cancel error=%v", err)
				}
				if !cancelCall && err != nil {
					t.Fatalf("first call: %v", err)
				}
			case <-time.After(5 * time.Second):
				if cancelCall {
					close(release)
				}
				t.Fatal("call did not finish")
			}
			if cancelCall {
				close(release)
			}
			fixture.mu.Lock()
			calls := fixture.callCount
			fixture.mu.Unlock()
			if cancelCall && calls != 0 || !cancelCall && calls != 1 {
				t.Fatalf("CallTool count=%d", calls)
			}
		})
	}
}

func TestPooledCapabilityUnresponsiveHandshakeFailsBeforeRecovery(t *testing.T) {
	fixture := &mockEngramServer{initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{Name: "write_fixture", InputSchemaJson: []byte(`{"type":"object"}`)}}}}
	addr, release, _ := startDeferredMockGRPC(t, fixture)
	_, mod, project := buildContractDispatcher(t, "")
	project.Env["ENGRAM_URL"] = "http://" + addr
	notifier := &discoveryNotifier{notifications: make(chan string, 1)}
	mod.SetNotifier(notifier)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := mod.ProxyHandleTool(ctx, project, "write_fixture", json.RawMessage(`{}`))
		result <- err
	}()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "CAPABILITY_NOT_SENT") {
			t.Fatalf("unresponsive backend error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("unresponsive backend blocked the caller")
	}
	fixture.mu.Lock()
	calls := fixture.callCount
	fixture.mu.Unlock()
	if calls != 0 {
		t.Fatalf("unresponsive backend received %d tool calls", calls)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-notifier.notifications:
	case <-time.After(10 * time.Second):
		t.Fatal("discovery did not recover after the handshake resumed")
	}
	if _, err := mod.ProxyHandleTool(t.Context(), project, "write_fixture", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("first call after recovery: %v", err)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.callCount != 1 {
		t.Fatalf("backend received %d calls, want one after recovery", fixture.callCount)
	}
}

func TestPooledCapabilitySlowFirstDiscoveryPreservesOriginalCall(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	fixture := &mockEngramServer{initStarted: started, initRelease: release, initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{Name: "write_fixture", InputSchemaJson: []byte(`{"type":"object"}`)}}}}
	_, mod, project := buildContractDispatcher(t, startMockGRPC(t, fixture))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := mod.ProxyHandleTool(ctx, project, "write_fixture", json.RawMessage(`{}`))
		result <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		close(release)
		t.Fatal("Initialize did not start")
	}
	select {
	case err := <-result:
		close(release)
		t.Fatalf("first request returned before discovery: %v", err)
	case <-time.After(4 * time.Second):
	}
	fixture.mu.Lock()
	calls := fixture.callCount
	fixture.mu.Unlock()
	if calls != 0 {
		close(release)
		t.Fatalf("dispatched %d calls before discovery", calls)
	}
	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("original first request failed after discovery: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("original first request did not complete")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.callCount != 1 {
		t.Fatalf("backend received %d calls, want original request once", fixture.callCount)
	}
}

func TestPooledCapabilityTransientFailurePreservesOriginalCall(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	_, mod, project := buildContractDispatcher(t, "")
	project.Env["ENGRAM_URL"] = "http://" + addr
	conn, err := mod.pool.getOrDialGRPC(project.Env["ENGRAM_URL"], mod.envFor(project, config.EnvWorkstationToken))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := mod.ProxyHandleTool(ctx, project, "write_fixture", json.RawMessage(`{}`))
		result <- err
	}()
	for state := conn.GetState(); state != connectivity.TransientFailure; state = conn.GetState() {
		if !conn.WaitForStateChange(ctx, state) {
			t.Fatal("transport did not report transient failure")
		}
	}
	select {
	case err := <-result:
		t.Fatalf("transient transport failure ended original call: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	listener, err = net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &mockEngramServer{initResp: &pb.InitializeResponse{Tools: []*pb.ToolDefinition{{Name: "write_fixture", InputSchemaJson: []byte(`{"type":"object"}`)}}}}
	server := grpc.NewServer()
	pb.RegisterEngramServiceServer(server, fixture)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("original call after transport recovery: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("original call did not recover within caller deadline")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.callCount != 1 {
		t.Fatalf("backend received %d calls, want original request once", fixture.callCount)
	}
}
