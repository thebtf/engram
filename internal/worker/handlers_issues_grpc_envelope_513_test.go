package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	"github.com/thebtf/engram/internal/issuefixture"
	"github.com/thebtf/engram/internal/mcp"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	gormlib "gorm.io/gorm"
)

// This is local synthetic gRPC-envelope coverage, not the separate 1 MiB mux
// IPC journey or installed-host acceptance. Historical records are seeded via
// the existing store, without changing current write limits or database guards.
func TestIssue513GRPCEnvelopeHistoricalRecord(t *testing.T) {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		t.Skip("DATABASE_DSN not set; local PostgreSQL envelope proof not exercised")
	}
	parsed, err := url.Parse(dsn)
	require.True(t, err == nil && parsed != nil, "test requires a local PostgreSQL URL; DSN withheld")
	require.True(t, parsed.Scheme == "postgres" || parsed.Scheme == "postgresql", "test requires a PostgreSQL URL; DSN withheld")
	host := parsed.Hostname()
	require.True(t, host == "localhost" || host == "127.0.0.1" || host == "::1", "test refuses non-loopback database targets; DSN withheld")

	// Verify the existing extension before the shared fixture creates a schema
	// or runs migrations. This preflight never installs or moves pgvector.
	func() {
		config, err := pgx.ParseConfig(dsn)
		require.True(t, err == nil, "database preflight configuration must parse; DSN withheld")
		isLoopback := func(host string) bool { return host == "localhost" || host == "127.0.0.1" || host == "::1" }
		require.True(t, isLoopback(config.Host), "database preflight refuses non-loopback resolved targets; DSN withheld")
		for _, fallback := range config.Fallbacks {
			require.True(t, isLoopback(fallback.Host), "database preflight refuses non-loopback fallback targets; DSN withheld")
		}
		pool := stdlib.OpenDB(*config)
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(0)
		defer func() { assert.True(t, pool.Close() == nil, "database preflight pool must close") }()
		ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
		defer cancel()
		var vectorExists bool
		err = pool.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_extension e JOIN pg_namespace n ON n.oid=e.extnamespace
			WHERE e.extname='vector' AND n.nspname='public'
		)`).Scan(&vectorExists)
		require.True(t, err == nil, "read-only pgvector preflight must succeed; database details withheld")
		require.True(t, vectorExists, "local disposable database must already have pgvector in public before fixture migrations")
	}()

	fixture := newIssue513ActorFixture(t)
	validator := auth.NewValidator("", fixture.service.tokenStore)
	identity, err := validator.Validate(t.Context(), fixture.client.bearer)
	require.True(t, err == nil, "persisted synthetic bearer must authenticate; credential withheld")
	require.Equal(t, auth.SourceClient, identity.Source)
	require.Equal(t, auth.RoleReadWrite, identity.Role)
	require.True(t, identity.KeycardID != "", "synthetic client must have a persisted keycard")
	native := mcp.NewServer(mcp.ServerOptions{})
	native.SetIssueStore(fixture.service.issueStore)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	transport, _ := grpcserver.New(&mcpHandlerAdapter{mcpServer: native}, validator)
	served := make(chan error, 1)
	go func() { served <- transport.Serve(listener) }()
	t.Cleanup(func() {
		transport.Stop()
		_ = listener.Close()
		select {
		case err := <-served:
			assert.True(t, err == nil || errors.Is(err, grpc.ErrServerStopped) || errors.Is(err, net.ErrClosed), "owned gRPC listener must stop cleanly")
		case <-time.After(5 * time.Second):
			t.Error("owned gRPC listener did not stop")
		}
	})
	// grpc-go's unset receive default is 4 MiB. Match the ordinary native
	// engramcore client (16 MiB), never an enlarged test-only receive ceiling.
	const receiveBytes = 16 << 20
	connection, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithNoProxy(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(receiveBytes), grpc.MaxCallSendMsgSize(receiveBytes)))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, connection.Close()) })
	client := pb.NewEngramServiceClient(connection)
	outgoing := metadata.AppendToOutgoingContext(t.Context(), "authorization", "Bearer "+fixture.client.bearer)
	seedContext := auth.WithIdentity(t.Context(), identity)
	seed := func(body, comment string) int64 {
		t.Helper()
		var id int64
		err := issuefixture.SeedHistorical(fixture.service.store.GetDB(), func(tx *gormlib.DB) error {
			store := gormdb.NewIssueStore(tx)
			var err error
			id, err = store.CreateIssue(seedContext, &gormdb.Issue{
				Title: "Synthetic historical gRPC envelope record", Body: body,
				SourceProject: issue513Source, TargetProject: issue513Source, CreatorKeycardID: identity.KeycardID,
			})
			if err != nil {
				return err
			}
			_, err = store.AddComment(seedContext, id, &gormdb.IssueComment{AuthorProject: issue513Source, AuthorAgent: "synthetic-envelope-author", Body: comment})
			return err
		})
		require.True(t, err == nil, "historical issue/comment seed must persist; record withheld")
		return id
	}
	invoke := func(id int64) (*pb.CallToolResponse, error) {
		arguments, err := json.Marshal(map[string]any{
			"action": "get", "id": id, "format": "json", "detail_mode": "paged",
			"comments_limit": 1, "history_limit": 1,
		})
		require.NoError(t, err)
		ctx, cancel := context.WithTimeout(outgoing, 30*time.Second)
		defer cancel()
		return client.CallTool(ctx, &pb.CallToolRequest{ToolName: "issues", Project: issue513Source, ArgumentsJson: arguments})
	}
	controlID := seed("Synthetic small issue control", "Synthetic small comment control")
	control := func(t *testing.T) {
		t.Helper()
		response, err := invoke(controlID)
		require.True(t, err == nil, "same-connection control must complete; grpc_status=%s", status.Code(err))
		require.NotNil(t, response)
		require.False(t, response.IsError, "small authenticated control must return an intact result")
		require.Equal(t, issue513Source, response.CanonicalProject)
		var envelope struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		require.True(t, json.Unmarshal(response.ContentJson, &envelope) == nil, "control must contain valid MCP JSON")
		require.Len(t, envelope.Content, 1)
		require.Equal(t, "text", envelope.Content[0].Type)
		var detail gormdb.IssueTaskDetail
		require.True(t, json.Unmarshal([]byte(envelope.Content[0].Text), &detail) == nil, "control must contain valid issue detail")
		require.Equal(t, controlID, detail.Issue.ID)
		require.True(t, detail.Issue.Body == "Synthetic small issue control", "control issue body must be intact")
		require.Len(t, detail.Comments, 1)
		require.True(t, detail.Comments[0].Body == "Synthetic small comment control", "control comment body must be intact")
	}

	for _, record := range []string{"issue_body", "comment_body"} {
		t.Run(record, func(t *testing.T) {
			// Raw ASCII alone exceeds the actual 16 MiB envelope. No inferred
			// escaping multiplier, copied wire encoder or mock echo is needed.
			large := "Synthetic historical envelope start\n" + strings.Repeat("x", receiveBytes+1) + "\nSynthetic historical envelope end"
			body, comment := "Synthetic ordinary issue body", "Synthetic ordinary comment body"
			if record == "issue_body" {
				body = large
			} else {
				comment = large
			}
			require.Greater(t, len(large), receiveBytes)
			id := seed(body, comment)
			before, beforeComments, err := fixture.service.issueStore.GetIssue(t.Context(), id)
			require.True(t, err == nil, "historical baseline must be readable; record withheld")
			require.True(t, before.Body == body, "issue seed must retain every byte")
			require.Len(t, beforeComments, 1)
			require.True(t, beforeComments[0].Body == comment, "comment seed must retain every byte")
			counts := issue513IssueDomainCounts(t, fixture.service)

			if !t.Run("control_before", control) {
				return
			}
			response, callErr := invoke(id)
			// A failed post-control must not hide independent REST integrity evidence.
			t.Run("control_after", control)
			assert.True(t, callErr == nil, "oversized detail needs a tool refusal, not a dropped/status response; grpc_status=%s", status.Code(callErr))
			assert.NotEqual(t, codes.ResourceExhausted, status.Code(callErr), "the real transport must not exhaust its ordinary receive limit")
			if callErr == nil && assert.NotNil(t, response) {
				assert.True(t, response.IsError, "no oversized success or partial record may escape")
				assert.Equal(t, issue513Source, response.CanonicalProject)
				assert.Less(t, len(response.ContentJson), 2048, "safe refusal must be bounded and contain no partial record")
				var failure struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
					Data    struct {
						Error struct {
							Code    string `json:"code"`
							Message string `json:"message"`
						} `json:"error"`
					} `json:"data"`
				}
				if assert.True(t, json.Unmarshal(response.ContentJson, &failure) == nil, "refusal must contain a typed MCP error object") {
					assert.Equal(t, -32000, failure.Code)
					assert.True(t, failure.Data.Error.Code == "TRANSPORT_RECORD_TOO_LARGE", "refusal must expose the stable domain code")
					for _, guidance := range []string{"REST", "detail_mode=paged", "comments/history"} {
						assert.True(t, strings.Contains(failure.Data.Error.Message, guidance), "refusal must explain authorized REST paging")
					}
				}
				for _, marker := range []string{"Synthetic historical envelope start", "Synthetic historical envelope end"} {
					assert.False(t, bytes.Contains(response.ContentJson, []byte(marker)), "refusal must not contain partial historical content")
				}
				for _, forbidden := range []string{"creator_keycard_id", "token_hash", "password_hash", "receipt_payload", "receipt_digest", "request_key", "payload_hash"} {
					assert.False(t, bytes.Contains(response.ContentJson, []byte(`"`+forbidden+`"`)), "gRPC refusal must exclude private credential/receipt fields")
				}
				for _, secret := range fixture.privateValues {
					assert.False(t, bytes.Contains(response.ContentJson, []byte(secret)), "gRPC refusal must exclude synthetic credentials and hashes")
				}
			}

			// Exercise the registered HTTP auth middleware and actual REST detail
			// route, not the store reader alone. No additional product server.
			path := fmt.Sprintf("http://localhost:37777/api/issues/%d?project=%s&detail_mode=paged&comments_limit=1&history_limit=1", id, issue513Source)
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("Authorization", "Bearer "+fixture.client.bearer)
			request.RemoteAddr = "192.0.2.51:43500"
			rest := httptest.NewRecorder()
			fixture.service.router.ServeHTTP(rest, request)
			require.Equal(t, http.StatusOK, rest.Code, "authorized REST must retain access to the intact historical record")
			require.Greater(t, rest.Body.Len(), receiveBytes, "actual serialized REST detail must exceed the gRPC envelope")
			var detail gormdb.IssueTaskDetail
			require.True(t, json.Unmarshal(rest.Body.Bytes(), &detail) == nil, "REST must return a complete JSON record, not truncated bytes")
			// Normalize only JSON's public projection (private creator keycard is
			// intentionally omitted) and time encoding; never print values/diffs.
			publicBytes, err := json.Marshal(struct {
				Issue    gormdb.Issue          `json:"issue"`
				Comments []gormdb.IssueComment `json:"comments"`
			}{*before, beforeComments})
			require.True(t, err == nil, "baseline public projection must encode")
			var expected gormdb.IssueTaskDetail
			require.True(t, json.Unmarshal(publicBytes, &expected) == nil, "baseline public projection must decode")
			assert.True(t, reflect.DeepEqual(expected.Issue, detail.Issue), "REST must preserve every public issue value, including the full body")
			assert.True(t, reflect.DeepEqual(expected.Comments, detail.Comments), "REST must preserve every comment value, including the full body")
			assert.EqualValues(t, 1, detail.CommentCount)
			assert.True(t, detail.CommentsComplete && detail.CommentsNextOffset == nil, "one whole comment must remain complete")
			assert.Equal(t, record == "comment_body", detail.CommentsOversized, "oversized single comment must be explicitly marked, never split")
			for _, forbidden := range []string{"creator_keycard_id", "token_hash", "password_hash", "receipt_payload", "receipt_digest", "request_key", "payload_hash"} {
				assert.False(t, bytes.Contains(rest.Body.Bytes(), []byte(`"`+forbidden+`"`)), "REST must exclude private credential/receipt fields")
			}
			for _, secret := range fixture.privateValues {
				assert.False(t, bytes.Contains(rest.Body.Bytes(), []byte(secret)), "REST must exclude synthetic credentials and hashes")
			}
			after, afterComments, err := fixture.service.issueStore.GetIssue(t.Context(), id)
			require.True(t, err == nil, "post-transport baseline must remain readable; record withheld")
			assert.True(t, reflect.DeepEqual(before, after), "gRPC refusal and REST readback must leave the entire stored issue unchanged")
			assert.True(t, reflect.DeepEqual(beforeComments, afterComments), "gRPC refusal and REST readback must leave all stored comments unchanged")
			assert.Equal(t, counts, issue513IssueDomainCounts(t, fixture.service), "read-only transport proof must not change domain row counts")
			assert.False(t, strings.Contains(fixture.capturedLogs.String(), large), "operational logs must not contain the synthetic historical content")
		})
	}
}
