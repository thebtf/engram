package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/handlers/engramcore"
	"github.com/thebtf/engram/internal/issuefixture"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/module/dispatcher"
	"github.com/thebtf/engram/internal/module/registry"
	pb "github.com/thebtf/engram/proto/engram/v1"
	muxcore "github.com/thebtf/mcp-mux/muxcore"
	"github.com/thebtf/mcp-mux/muxcore/owner"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	gormlib "gorm.io/gorm"
)

// Uses the pinned real owner and resilient recipient, registered production
// dispatcher, engramcore ProxyHandleTool/buildInnerBlock, real gRPC bridge and
// isolated PostgreSQL. No copied envelope/scanner implementation or fake cap.
func issue513NativeIPCReviewCase(t *testing.T, fixture *issue513ActorFixture, native *mcp.Server, validator *auth.Validator) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	transport, _ := grpcserver.New(&mcpHandlerAdapter{mcpServer: native}, validator)
	go func() { _ = transport.Serve(listener) }()
	t.Cleanup(func() { transport.Stop(); _ = listener.Close() })
	identity, err := validator.Validate(t.Context(), fixture.client.bearer)
	require.NoError(t, err)
	ctx := auth.WithIdentity(t.Context(), identity)
	seed := func(body string) int64 {
		var id int64
		err := issuefixture.SeedHistorical(fixture.service.store.GetDB(), func(tx *gormlib.DB) error {
			var err error
			id, err = gormdb.NewIssueStore(tx).CreateIssue(ctx, &gormdb.Issue{Title: "Synthetic IPC historical record", Body: body, SourceProject: issue513Source, TargetProject: issue513Source, CreatorKeycardID: identity.KeycardID})
			return err
		})
		require.NoError(t, err)
		return id
	}
	smallID := seed("Synthetic small IPC control")
	largeBody := strings.Repeat("<", 120000)
	largeID := seed(largeBody)
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	requestBytes := func(id, issueID int64) []byte {
		raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": "issues", "arguments": map[string]any{"action": "get", "id": issueID, "format": "json", "detail_mode": "paged"}}})
		require.NoError(t, err)
		return raw
	}
	getArgs, err := json.Marshal(map[string]any{"action": "get", "id": largeID, "format": "json", "detail_mode": "paged"})
	require.NoError(t, err)
	probeContext, cancelProbe := context.WithTimeout(metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+fixture.client.bearer), 10*time.Second)
	defer cancelProbe()
	grpcProbe, err := pb.NewEngramServiceClient(connection).CallTool(probeContext, &pb.CallToolRequest{ToolName: "issues", ArgumentsJson: getArgs})
	require.NoError(t, err, "probe must cross the actual gRPC service, not a transport-size arithmetic model")

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	core := engramcore.NewModule()
	modules := registry.New()
	require.NoError(t, modules.Register(core))
	modules.Freeze()
	require.NoError(t, core.Init(t.Context(), module.ModuleDeps{Logger: quiet, DaemonCtx: t.Context(), Lookup: modules}))
	t.Cleanup(func() { assert.NoError(t, core.Shutdown(context.Background())) })
	dispatch := dispatcher.New(modules, quiet)
	cwd := t.TempDir()
	environment := map[string]string{config.EnvServerURL: "http://" + listener.Addr().String(), config.EnvWorkstationToken: fixture.client.bearer}
	project := muxcore.ProjectContext{ID: muxcore.ProjectContextID(cwd), Cwd: cwd, Env: environment}
	dispatchProbe, err := dispatch.HandleRequest(t.Context(), project, requestBytes(2, largeID))
	require.NoError(t, err)
	require.True(t, json.Valid(dispatchProbe))
	// These are observed production serialization lengths, never invented
	// escaping multipliers. The 1 MiB value is the pinned recipient's Buffer.
	t.Logf("observed grpc_content_json_bytes=%d dispatcher_response_bytes=%d pinned_mux_version=v0.29.1 pinned_ipc_scanner_max_bytes=1048576", len(grpcProbe.ContentJson), len(dispatchProbe))

	ipcPath := filepath.Join(cwd, "native-review.sock")
	muxOwner, err := owner.NewOwner(owner.OwnerConfig{IPCPath: ipcPath, ServerID: "issue513-native-review", Cwd: cwd, Env: environment, SessionHandler: dispatch, TokenHandshake: true, Logger: log.New(io.Discard, "", 0)})
	require.NoError(t, err)
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	require.True(t, muxOwner.PreRegisterInitial(token, cwd, environment))
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	frames := make(chan []byte, 8)
	go func() {
		defer close(frames)
		scanner := bufio.NewScanner(stdoutReader)
		scanner.Buffer(make([]byte, 64*1024), 16<<20)
		for scanner.Scan() {
			frames <- bytes.Clone(scanner.Bytes())
		}
	}()
	var reconnects atomic.Int64
	reconnecting := make(chan struct{}, 1)
	shimDone := make(chan struct{})
	go func() {
		defer close(shimDone)
		_ = owner.RunResilientClient(owner.ResilientClientConfig{
			Stdin: stdinReader, Stdout: stdoutWriter, InitialIPCPath: ipcPath, Token: token,
			Reconnect: func() (string, string, error) {
				reconnects.Add(1)
				select {
				case reconnecting <- struct{}{}:
				default:
				}
				return "", "", errors.New("synthetic unexpected IPC reconnect")
			},
			ReconnectTimeout: time.Second, Logger: log.New(io.Discard, "", 0), EnginePrefix: "engram",
		})
	}()
	t.Cleanup(func() {
		_ = stdinWriter.Close()
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		muxOwner.Shutdown()
		_ = stdinReader.Close()
		select {
		case <-shimDone:
		case <-time.After(5 * time.Second):
			t.Error("owned resilient test shim did not stop")
		}
	})
	send := func(raw []byte) { _, err := fmt.Fprintf(stdinWriter, "%s\n", raw); require.NoError(t, err) }
	receive := func(want int64) ([]byte, string) {
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case frame, ok := <-frames:
				if !ok {
					return nil, "stdout closed"
				}
				var head struct {
					ID *int64 `json:"id"`
				}
				if json.Unmarshal(frame, &head) == nil && head.ID != nil && *head.ID == want {
					return frame, "reply"
				}
			case <-reconnecting:
				return nil, "recipient entered reconnect"
			case <-shimDone:
				return nil, "shim exited"
			case <-deadline.C:
				return nil, "reply dropped or not delivered within bounded observation"
			}
		}
	}
	send([]byte(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"issue513-wire-review","version":"test"}}}`))
	initialized, event := receive(0)
	require.Equal(t, "reply", event, "ordinary shim initialization must succeed before the large-record oracle")
	require.True(t, json.Valid(initialized))
	send(requestBytes(1, smallID))
	control, event := receive(1)
	require.Equal(t, "reply", event, "small record must traverse the same owner, IPC reader, module proxy and gRPC server")
	var controlEnvelope struct {
		Result struct {
			IsError bool `json:"isError"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	require.NoError(t, json.Unmarshal(control, &controlEnvelope))
	require.Len(t, controlEnvelope.Error, 0)
	require.False(t, controlEnvelope.Result.IsError)
	require.Zero(t, reconnects.Load())

	send(requestBytes(2, largeID))
	result, event := receive(2)
	assert.Equal(t, "reply", event, "large historical record needs a safe explicit refusal, not a dropped frame or reconnect")
	assert.Zero(t, reconnects.Load(), "known response size must not break the actual pinned IPC recipient")
	if len(result) != 0 {
		assert.True(t, bytes.Contains(result, []byte("TRANSPORT_RECORD_TOO_LARGE")), "ordinary-path refusal must retain a stable safe code")
		assert.True(t, bytes.Contains(result, []byte("REST")), "ordinary-path refusal must retain authorized REST guidance")
	}
	if len(result) != 0 && bytes.Contains(result, []byte("REST")) && reconnects.Load() == 0 {
		send([]byte(`{"jsonrpc":"2.0","id":3,"method":"ping"}`))
		_, event = receive(3)
		assert.Equal(t, "reply", event, "safe refusal leaves the same ordinary connection usable")
	}
	t.Logf("ordinary_path_observation=%s reconnect_count=%d", event, reconnects.Load())
	row, _, err := fixture.service.issueStore.GetIssue(t.Context(), largeID)
	require.NoError(t, err)
	assert.True(t, row.Body == largeBody, "transport failure cannot trim or rewrite historical stored bytes")

	for _, test := range []struct {
		name      string
		id        int64
		arguments string
		wantError bool
	}{
		{"project_free_small_list_control", 4, `{"action":"list","format":"json","q":"Synthetic IPC historical record","limit":1}`, false},
		{"project_free_duplicate_q_retains_typed_invalid_argument", 5, `{"action":"list","format":"json","q":"first","q":"second","limit":1}`, true},
		{"project_free_unsafe_number_retains_typed_invalid_argument", 6, `{"action":"list","format":"json","limit":9007199254740993}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := issue513IssueDomainCounts(t, fixture.service)
			// Keep the received argument bytes intact, including duplicate members
			// and unsafe numeric tokens, through the real ordinary shim stdin.
			send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"issues","arguments":%s}}`, test.id, test.arguments)))
			frame, event := receive(test.id)
			require.Equal(t, "reply", event, "ordinary issue admission must return a correlated response")
			var response struct {
				Result *struct {
					IsError bool `json:"isError"`
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
				Error *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(frame, &response))
			protocolCode := 0
			if response.Error != nil {
				protocolCode = response.Error.Code
			}
			// Prove the same connection remains usable even if admission was
			// incorrectly converted into a generic protocol error.
			send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, test.id+10)))
			_, pingEvent := receive(test.id + 10)
			assert.Equal(t, "reply", pingEvent)
			assert.Zero(t, reconnects.Load(), "typed admission refusal must not reconnect the ordinary recipient")
			assert.Equal(t, before, issue513IssueDomainCounts(t, fixture.service), "list admission/control must not write issues/comments/history/receipts")
			t.Logf("ordinary_admission_protocol_error_code=%d same_connection_ping=%s reconnect_count=%d", protocolCode, pingEvent, reconnects.Load())
			if !assert.True(t, response.Error == nil, "issue validation must remain a structured tool refusal, not a generic JSON-RPC protocol error") {
				return
			}
			require.NotNil(t, response.Result)
			assert.Equal(t, test.wantError, response.Result.IsError)
			require.Len(t, response.Result.Content, 1)
			if test.wantError {
				var failure struct {
					Data struct {
						Error struct {
							Code string `json:"code"`
						} `json:"error"`
					} `json:"data"`
				}
				require.NoError(t, json.Unmarshal([]byte(response.Result.Content[0].Text), &failure))
				assert.Equal(t, "INVALID_ARGUMENT", failure.Data.Error.Code)
				return
			}
			var nativeResult struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			}
			require.NoError(t, json.Unmarshal([]byte(response.Result.Content[0].Text), &nativeResult))
			require.Len(t, nativeResult.Content, 1)
			var page struct {
				Issues []json.RawMessage `json:"issues"`
				Total  int               `json:"total"`
				Limit  int               `json:"limit"`
			}
			require.NoError(t, json.Unmarshal([]byte(nativeResult.Content[0].Text), &page))
			assert.Len(t, page.Issues, 1)
			assert.Equal(t, 2, page.Total)
			assert.Equal(t, 1, page.Limit)
		})
	}
}
