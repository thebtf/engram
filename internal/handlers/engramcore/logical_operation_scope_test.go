package engramcore

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/module"
	"github.com/thebtf/engram/internal/projectidentity"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestLogicalOperationScopeRealTransports(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, name := range []string{"DATABASE_DSN", "ENGRAM_VAULT_KEY", "ENGRAM_ENCRYPTION_KEY", "ENGRAM_ENCRYPTION_KEY_FILE"} {
		t.Setenv(name, "")
	}
	cfg, _, err := config.Reload()
	if err != nil || cfg.DatabaseDSN != "" || cfg.EncryptionKey != "" || cfg.EncryptionKeyFile != "" {
		t.Fatalf("status fixture is not isolated from owner database/key configuration: %v", err)
	}
	catalog := mcp.NewServer(mcp.ServerOptions{Version: daemonClientVersion})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	token := uuid.NewString()
	backend, _ := grpcserver.New(anchorDiscoveryHandler{catalog}, auth.NewValidator(token, nil))
	served := make(chan error, 1)
	go func() { served <- backend.Serve(listener) }()
	t.Cleanup(func() {
		backend.Stop()
		_ = listener.Close()
		<-served
	})
	_, mod, project := buildContractDispatcher(t, listener.Addr().String())
	project.Env[config.EnvWorkstationToken] = token
	command := exec.CommandContext(t.Context(), "git", "-C", project.Cwd, "init", "--quiet", "--template=")
	command.Env = projectidentity.RepositoryGitEnvironmentV3()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize unbound caller fixture: %v: %s", err, output)
	}
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := pb.NewEngramServiceClient(connection)
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+token))
	type transport struct {
		name string
		call func(string, []byte) ([]byte, error)
	}
	for _, route := range []transport{
		{"direct-grpc", func(tool string, arguments []byte) ([]byte, error) {
			response, err := client.CallTool(ctx, &pb.CallToolRequest{ToolName: tool, ArgumentsJson: arguments})
			if err != nil {
				return nil, err
			}
			if response.IsError {
				t.Errorf("real handler returned isError: %s", response.ContentJson)
			}
			return response.ContentJson, nil
		}},
		{"native-proxy", func(tool string, arguments []byte) ([]byte, error) {
			block, err := mod.ProxyHandleTool(t.Context(), project, tool, json.RawMessage(arguments))
			if err != nil {
				return nil, err
			}
			var decoded struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(block, &decoded); err != nil {
				return nil, err
			}
			return []byte(decoded.Text), nil
		}},
	} {
		t.Run(route.name, func(t *testing.T) {
			t.Run("unbound-vault-status", func(t *testing.T) {
				body, err := route.call("vault", []byte(`{"action":"status"}`))
				if err != nil {
					t.Fatalf("authenticated project-free status was rejected before the real handler: %v", err)
				}
				var result struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
					IsError bool `json:"isError"`
				}
				if err := json.Unmarshal(body, &result); err != nil || result.IsError || len(result.Content) != 1 {
					t.Fatalf("real MCP status envelope = %s, error=%v", body, err)
				}
				var state struct {
					KeyConfigured   *bool `json:"key_configured"`
					CredentialCount *int  `json:"credential_count"`
				}
				if err := json.Unmarshal([]byte(result.Content[0].Text), &state); err != nil || state.KeyConfigured == nil || *state.KeyConfigured || state.CredentialCount == nil || *state.CredentialCount != 0 {
					t.Fatalf("real isolated status result = %s, error=%v", result.Content[0].Text, err)
				}
			})
			for _, tc := range []struct{ tool, arguments string }{
				{"issues", `{"action":"create","project":"11111111-1111-4111-8111-111111111111","title":"blocked","target_project":"22222222-2222-4222-8222-222222222222"}`},
				{"issues", `{"action":"update","project":"11111111-1111-4111-8111-111111111111","id":1,"status":"resolved"}`},
				{"issues", `{"action":"comment","project":"11111111-1111-4111-8111-111111111111","id":1,"body":"blocked"}`},
				{"issues", `{"action":"reopen","project":"11111111-1111-4111-8111-111111111111","id":1}`},
				{"issues", `{"action":"close","project":"11111111-1111-4111-8111-111111111111","id":1}`},
				{"issues", `{"action":[],"project":"11111111-1111-4111-8111-111111111111"}`},
				{"issues", `{"action":"list","project":false}`},
				{"issues", `{"action":"list"} {}`},
				{"issues", `[{"action":"list"}]`},
				{"issues", `{"action":"list"`},
				{"issues", `{"action":"unknown"}`},
				{"vault", `{"action":"store","name":"blocked","value":"fixture-only"}`},
				{"vault", `{"action":"get","name":"blocked","scope":"project"}`},
				{"vault", `{"action":"delete","name":"blocked","scope":"project"}`},
				{"vault", `{"action":"get","name":"blocked","project":"11111111-1111-4111-8111-111111111111"}`},
				{"vault", `{"action":"delete","name":"blocked","project":"11111111-1111-4111-8111-111111111111"}`},
				{"vault", `{"action":"list","scope":"global","project":"11111111-1111-4111-8111-111111111111"}`},
				{"vault", `{"action":"store","scope":"global","project":42}`},
				{"vault", `{"action":"store","scope":"global","action":false}`},
				{"vault", `{"action":"store","scope":true}`},
				{"vault", `{"action":"unknown","scope":"global"}`},
				{"vault", `{"action":[],"scope":"global"}`},
				{"vault", `{"action":"store","scope":"global"} {}`},
				{"vault", `[{"action":"store","scope":"global"}]`},
				{"vault", `{"action":"store","scope":"global"`},
			} {
				t.Run(tc.tool+"/"+tc.arguments, func(t *testing.T) {
					_, err := route.call(tc.tool, []byte(tc.arguments))
					var moduleErr *module.ModuleError
					if errors.As(err, &moduleErr) && moduleErr.Code == "PROJECT_CONTEXT_REQUIRED" {
						return
					}
					for _, detail := range status.Convert(err).Details() {
						if info, ok := detail.(*errdetails.ErrorInfo); ok && info.Reason == "PROJECT_CONTEXT_REQUIRED" && status.Code(err) == codes.FailedPrecondition {
							return
						}
					}
					t.Fatalf("scoped/malformed request earned an unscoped exception: %v", err)
				})
			}
		})
	}
	if _, err := client.CallTool(context.Background(), &pb.CallToolRequest{ToolName: "vault", ArgumentsJson: []byte(`{"action":"status"}`)}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("project-free status bypassed authentication: %v", err)
	}
	for _, path := range []string{filepath.Join(project.Cwd, ".engram-project"), filepath.Join(project.Cwd, ".git", "engram-project.json"), filepath.Join(home, ".engram", "vault.key")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("project-free request invented marker/cache/key state at %s: %v", path, err)
		}
	}
}
