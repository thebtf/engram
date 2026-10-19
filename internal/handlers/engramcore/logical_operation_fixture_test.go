package engramcore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	"github.com/thebtf/engram/internal/crypto"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"gorm.io/gorm/logger"
)

func TestLogicalOperationScopeRealStores(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("DATABASE_DSN", "")
	t.Setenv("ENGRAM_ENCRYPTION_KEY_FILE", "")
	t.Setenv("ENGRAM_ENCRYPTION_KEY", "")
	t.Setenv("ENGRAM_VAULT_KEY", "")
	store, dsn := openLogicalOperationFixture(t)
	t.Setenv("DATABASE_DSN", dsn)
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal("generate test-owned in-memory vault key")
	}
	t.Setenv("ENGRAM_VAULT_KEY", hex.EncodeToString(key[:]))
	cfg, _, err := config.Reload()
	if err != nil {
		t.Fatal("load isolated native operation configuration")
	}
	vault, err := crypto.NewVault(cfg)
	if err != nil {
		t.Fatal("initialize test-owned in-memory vault")
	}
	projectA, projectB := uuid.NewString(), uuid.NewString()
	for _, id := range []string{projectA, projectB} {
		if err := store.GetDB().Create(&gormstore.Project{ID: id}).Error; err != nil {
			t.Fatal("seed isolated canonical project")
		}
	}
	issues := gormstore.NewIssueStore(store.GetDB())
	first := &gormstore.Issue{Title: "native operation issue A", SourceProject: projectB, TargetProject: projectA, SourceAgent: "native-fixture"}
	second := &gormstore.Issue{Title: "native operation issue B", SourceProject: projectA, TargetProject: projectB, SourceAgent: "native-fixture"}
	for _, row := range []*gormstore.Issue{first, second} {
		id, err := issues.CreateIssue(t.Context(), row)
		if err != nil {
			t.Fatal("seed real issue store")
		}
		row.ID = id
	}
	catalog := mcp.NewServer(mcp.ServerOptions{Version: daemonClientVersion})
	catalog.SetIssueStore(issues)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	token := uuid.NewString()
	backend, internal := grpcserver.New(anchorDiscoveryHandler{catalog}, auth.NewValidator(token, nil))
	internal.SetDB(store.GetDB())
	served := make(chan error, 1)
	go func() { served <- backend.Serve(listener) }()
	t.Cleanup(func() { backend.Stop(); _ = listener.Close(); <-served })
	_, mod, unbound := buildContractDispatcher(t, listener.Addr().String())
	unbound.Env[config.EnvWorkstationToken] = token
	command := exec.CommandContext(t.Context(), "git", "-C", unbound.Cwd, "init", "--quiet", "--template=")
	command.Env = projectidentity.RepositoryGitEnvironmentV3()
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("initialize unbound native operation caller: %v: %s", err, output)
	}
	bound := unbound
	bound.Cwd = t.TempDir()
	writeContractProjectMarker(t, bound.Cwd, projectA)
	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	client := pb.NewEngramServiceClient(connection)
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+token))
	for _, route := range []struct {
		name string
		call func(bool, string, []byte) ([]byte, error)
	}{
		{"direct-grpc", func(scoped bool, tool string, args []byte) ([]byte, error) {
			request := &pb.CallToolRequest{ToolName: tool, ArgumentsJson: args}
			if scoped {
				request.Project = projectA
			}
			response, err := client.CallTool(ctx, request)
			if err != nil {
				return nil, err
			}
			return response.ContentJson, nil
		}},
		{"native-proxy", func(scoped bool, tool string, args []byte) ([]byte, error) {
			project := unbound
			if scoped {
				project = bound
			}
			block, err := mod.ProxyHandleTool(t.Context(), project, tool, json.RawMessage(args))
			if err != nil {
				return nil, err
			}
			var result struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(block, &result); err != nil {
				return nil, err
			}
			return []byte(result.Text), nil
		}},
	} {
		t.Run(route.name, func(t *testing.T) {
			call := func(t *testing.T, scoped bool, tool string, args map[string]any) string {
				t.Helper()
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				body, err := route.call(scoped, tool, raw)
				if err != nil {
					t.Fatalf("real %s %v transport refused: %v", tool, args["action"], err)
				}
				var result struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
					IsError bool `json:"isError"`
				}
				if err := json.Unmarshal(body, &result); err != nil || result.IsError || len(result.Content) != 1 {
					t.Fatal("real handler returned a malformed/error MCP result, not success")
				}
				return result.Content[0].Text
			}
			for _, tc := range []struct {
				name         string
				scoped       bool
				args         map[string]any
				want, absent string
			}{
				{"unbound list", false, map[string]any{"action": "list"}, first.Title, ""},
				{"unbound get", false, map[string]any{"action": "get", "id": first.ID}, first.Title, second.Title},
				{"unbound target filter", false, map[string]any{"action": "list", "project": projectB}, second.Title, first.Title},
				{"unbound source filter", false, map[string]any{"action": "list", "source_project": projectB}, first.Title, second.Title},
				{"bound explicit foreign filter", true, map[string]any{"action": "list", "project": projectB}, second.Title, first.Title},
			} {
				t.Run(tc.name, func(t *testing.T) {
					text := call(t, tc.scoped, "issues", tc.args)
					if !strings.Contains(text, tc.want) || tc.absent != "" && strings.Contains(text, tc.absent) {
						t.Fatal("real issue query lost its explicit filter or returned another project's issue")
					}
					if tc.name == "unbound list" && !strings.Contains(text, second.Title) {
						t.Error("cross-project list omitted the second project")
					}
				})
			}
			t.Run("same-name Vault namespaces", func(t *testing.T) {
				name := "native-operation-" + uuid.NewString()
				credentials := gormstore.NewCredentialStore(store)
				for _, seed := range []struct{ project, scope, value string }{{"", "global", "fixture-global-value"}, {projectA, "project", "fixture-project-value"}} {
					encrypted, err := vault.Encrypt(seed.value)
					if err != nil {
						t.Fatal("encrypt isolated seed")
					}
					if _, err := credentials.Create(t.Context(), &models.Credential{Project: seed.project, Scope: seed.scope, Key: name, EncryptedSecret: encrypted, EncryptionKeyFingerprint: vault.Fingerprint()}); err != nil {
						t.Fatal("seed real credential namespace")
					}
				}
				get := func(t *testing.T, scoped bool, args map[string]any, want string) {
					t.Helper()
					text := call(t, scoped, "vault", args)
					var credential struct {
						Value string `json:"value"`
					}
					if err := json.Unmarshal([]byte(text), &credential); err != nil || credential.Value != want {
						t.Fatal("real Vault get crossed namespace or returned no decrypted value")
					}
				}
				get(t, false, map[string]any{"action": "get", "name": name}, "fixture-global-value")
				get(t, false, map[string]any{"action": "get", "scope": "global", "project": projectA, "name": name}, "fixture-global-value")
				get(t, true, map[string]any{"action": "get", "project": projectA, "name": name}, "fixture-project-value")
				listed := call(t, true, "vault", map[string]any{"action": "list", "scope": "global", "project": projectA})
				var list []struct {
					Name  string `json:"name"`
					Scope string `json:"scope"`
				}
				if err := json.Unmarshal([]byte(listed), &list); err != nil {
					t.Fatal("decode real Vault list")
				}
				seen := map[string]bool{}
				for _, item := range list {
					if item.Name == name {
						seen[item.Scope] = true
					}
				}
				if !seen["project"] || !seen["global"] {
					t.Fatal("list scope=global incorrectly discarded its explicit project selector or merged same-name namespaces")
				}
				globalList := call(t, false, "vault", map[string]any{"action": "list", "scope": "project"})
				if err := json.Unmarshal([]byte(globalList), &list); err != nil {
					t.Fatal("decode real unbound Vault list")
				}
				globalSeen := false
				for _, item := range list {
					if item.Name == name {
						if item.Scope != "global" {
							t.Fatal("unbound list scope field selected project credentials")
						}
						globalSeen = true
					}
				}
				if !globalSeen {
					t.Fatal("unbound list omitted its global namespace")
				}
				call(t, false, "vault", map[string]any{"action": "delete", "name": name})
				get(t, true, map[string]any{"action": "get", "project": projectA, "name": name}, "fixture-project-value")
				if _, err := credentials.Get(t.Context(), "", name); err == nil {
					t.Fatal("global delete did not delete the exact global credential")
				}
				call(t, true, "vault", map[string]any{"action": "delete", "project": projectA, "name": name})
				newName := name + "-new"
				stored := call(t, false, "vault", map[string]any{"action": "store", "scope": "global", "name": newName, "value": "fixture-new-global-value"})
				if stored == "" {
					t.Fatal("explicit global store returned no real result")
				}
				get(t, false, map[string]any{"action": "get", "name": newName}, "fixture-new-global-value")
				call(t, false, "vault", map[string]any{"action": "delete", "scope": "global", "project": projectA, "name": newName})
			})
		})
	}
	for _, path := range []string{filepath.Join(unbound.Cwd, ".engram-project"), filepath.Join(unbound.Cwd, ".git", "engram-project.json"), filepath.Join(home, ".engram", "vault.key")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("unbound operations invented local identity/key state: %s", path)
		}
	}
}

func openLogicalOperationFixture(t *testing.T) (*gormstore.Store, string) {
	t.Helper()
	path := os.Getenv("ENGRAM_NATIVE_OPERATION_FIXTURE")
	if path == "" {
		t.Skip("native operation store proof requires its distinct fresh physical-target admission")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("read native operation admission (setup failure, not RED)")
	}
	var binding struct {
		Admitted         bool      `json:"admitted"`
		DSNFile          string    `json:"dsn_file"`
		Database         string    `json:"database"`
		DatabaseOID      string    `json:"database_oid"`
		SystemIdentifier string    `json:"system_identifier"`
		ContainerID      string    `json:"container_id"`
		Schema           string    `json:"schema"`
		ExpiresAt        time.Time `json:"expires_at"`
	}
	if json.Unmarshal(raw, &binding) != nil || !binding.Admitted || time.Until(binding.ExpiresAt) <= 0 || time.Until(binding.ExpiresAt) > 15*time.Minute || binding.Database != "document_first_write_fixture" || binding.DatabaseOID != "16391" || binding.SystemIdentifier != "7697507119866282022" || binding.ContainerID != "f51dc506fd93988be213482365c1c8d8c56cc36ffc9f5e2d3e8c1b3217bb904c" || binding.Schema != "native_operation_scope_b13dfe0dd3c94f5286790c1fd9c1c5e3" {
		t.Fatal("native operation admission is stale, unassigned or not the exact fixture (setup failure, not RED)")
	}
	if filepath.ToSlash(binding.DSNFile) != "D:/Dev/engram/.agent/artifacts/data/pr568-stream-integrity-20261017/.private/document-first-write/postgres.dsn" {
		t.Fatal("DSN file is outside exact assigned private fixture")
	}
	private, err := os.ReadFile(binding.DSNFile)
	if err != nil {
		t.Fatal("read assigned private DSN (not logged)")
	}
	dsn := strings.TrimSpace(string(private))
	pg, err := pgx.ParseConfig(dsn)
	if err != nil || pg.Host != "127.0.0.1" || pg.Port != 22417 || pg.Database != binding.Database {
		t.Fatal("private DSN does not select the exact assigned target")
	}
	pg.RuntimeParams["application_name"] = binding.Schema
	pg.RuntimeParams["statement_timeout"] = "10000"
	admin := stdlib.OpenDB(*pg)
	admin.SetMaxOpenConns(2)
	admin.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = admin.Close() })
	ctx, cancel := context.WithDeadline(t.Context(), binding.ExpiresAt)
	t.Cleanup(cancel)
	var actual string
	if err := admin.QueryRowContext(ctx, `SELECT (SELECT system_identifier::text FROM pg_control_system())||'|'||oid::text||'|'||datname FROM pg_database WHERE datname=current_database()`).Scan(&actual); err != nil || actual != "7697507119866282022|16391|document_first_write_fixture" {
		t.Fatal("physical native operation target identity mismatch (setup failure, not RED)")
	}
	var extensionSchema string
	if err := admin.QueryRowContext(ctx, `SELECT n.nspname FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace WHERE e.extname='vector'`).Scan(&extensionSchema); err != nil || extensionSchema != "public" {
		t.Fatal("assigned fixture vector extension is not in public")
	}
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+binding.Schema); err != nil {
		t.Fatal("create unique native operation schema (setup failure, not RED)")
	}
	var store *gormstore.Store
	t.Cleanup(func() {
		if store != nil {
			if err := store.Close(); err != nil {
				t.Error("close native operation injected store")
			}
		}
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		var ownConnections int
		if err := admin.QueryRowContext(cleanup, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1 AND pid<>pg_backend_pid()", binding.Schema).Scan(&ownConnections); err != nil {
			t.Error("read native operation connection closure")
		} else {
			t.Logf("native operation remaining own client connections after store/client closure=%d", ownConnections)
			if ownConnections != 0 {
				t.Error("real native operation consumer retained an open fixture connection")
			}
		}
		if _, err := admin.ExecContext(cleanup, "DROP SCHEMA "+binding.Schema+" CASCADE"); err != nil {
			t.Error("drop exact test-owned native operation schema")
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("parse assigned DSN")
		}
		query := u.Query()
		query.Set("search_path", binding.Schema+",public")
		query.Set("application_name", binding.Schema)
		query.Set("statement_timeout", "10000")
		u.RawQuery = query.Encode()
		dsn = u.String()
	} else {
		dsn += " application_name=" + binding.Schema + " options='-c search_path=" + binding.Schema + ",public -c statement_timeout=10000'"
	}
	store, err = gormstore.NewStore(gormstore.Config{DSN: dsn, MaxConns: 2, LogLevel: logger.Silent})
	if err != nil {
		t.Fatal("open migrated native operation schema (setup failure, not RED; DSN not logged)")
	}
	var schema string
	if err := store.GetDB().Raw("SELECT current_schema()").Scan(&schema).Error; err != nil || schema != binding.Schema {
		t.Fatal("native operation store escaped assigned schema")
	}
	t.Logf("fixture identity=%s schema=%s helper/injected maxopen=2 idle=1; real MCP Vault constructor maxopen=10 idle=5 (Root's narrow serial-consumer exception)", actual, schema)
	return store, dsn
}
