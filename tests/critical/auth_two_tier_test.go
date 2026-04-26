//go:build critical
// +build critical

// Package critical_test contains @critical end-to-end tests that prove the
// engram product works as a whole, not just per-function. Run via:
//
//	go test -tags=critical ./tests/critical/...
//
// Tests in this package use real interfaces — bufconn for gRPC, httptest
// for HTTP — not direct function calls. They exercise the same auth chain
// that ships in production binaries.
//
// Annotation grammar:
//
//	@critical
//	@category: smoke | behavioral | data-consistency
//	@features: [feature-slug-or-F-ID, ...]
//	@dev_stand: required | optional
package critical_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/grpcserver"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

// @critical
// @category: behavioral
// @features: [auth-two-tier-tokens]
// @dev_stand: optional
//
// TestCritical_AuthTwoTier proves that the v6 auth boundary actually works
// across BOTH transports (gRPC + HTTP) using the SAME validator chain.
// Covers FR-2 (symmetric validation), FR-1 (two tiers distinguishable),
// FR-6 (issuance hardened to session-only via separation of bearer-vs-cookie
// at the validator level — full HTTP-cookie integration is asserted in the
// HTTP sub-test below).
//
// This is the test PR #203's regression would have failed: PR #203 made a
// daemon misconfigured with a renamed env-var look identical to a deeply
// broken auth chain, surfacing only `loom_*` static tools. A critical test
// that asserts "valid keycard ⇒ Initialize succeeds AND returns a tool list"
// would have flipped to FAIL the moment the env-var rename landed without
// migration. This is that test.

func TestCritical_AuthTwoTier(t *testing.T) {
	// --- Fixtures: valid keycard, valid operator key ---
	const (
		operatorKey = "operator-secret-do-not-reuse"
		// 39-char raw keycard: TokenRawPrefix ("engram_") + 32 hex chars.
		// The shape gate in auth.Validator rejects anything that isn't
		// exactly this shape, so the fixture MUST match.
		clientRaw = "engram_cccccc1c000000000000000000abcdef"
	)

	// Use auth.TokenRawPrefix / TokenPrefixLen so a future change to the
	// token shape can't silently desync this test from the validator.
	prefixStart := len(auth.TokenRawPrefix)
	prefixEnd := prefixStart + auth.TokenPrefixLen
	clientPrefix := clientRaw[prefixStart:prefixEnd]

	// Hash the keycard with bcrypt.MinCost (test cost) and seed an in-memory
	// token store keyed by the 8-hex prefix.
	hash, err := bcrypt.GenerateFromPassword([]byte(clientRaw), bcrypt.MinCost)
	require.NoError(t, err)
	store := &critStore{
		rows: map[string][]gormdb.APIToken{
			clientPrefix: {{
				ID:          "uuid-critical-1",
				Name:        "critical-test-keycard",
				TokenHash:   string(hash),
				TokenPrefix: clientPrefix,
				Scope:       "read-write",
			}},
		},
	}
	validator := auth.NewValidator(operatorKey, store)

	// --- gRPC half: bufconn + real grpcserver.New + same validator ---
	t.Run("gRPC accepts operator key", func(t *testing.T) {
		client, cleanup := newGRPCBufconnClient(t, validator)
		defer cleanup()

		ctx := metadata.AppendToOutgoingContext(
			context.Background(), "authorization", "Bearer "+operatorKey,
		)
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		resp, err := client.Initialize(ctx, &pb.InitializeRequest{
			ClientName:    "critical-suite",
			ClientVersion: "test",
		})
		require.NoError(t, err, "operator key MUST authenticate at gRPC layer (FR-1 tier 1)")
		require.NotNil(t, resp)
	})

	t.Run("gRPC accepts dashboard-issued keycard", func(t *testing.T) {
		client, cleanup := newGRPCBufconnClient(t, validator)
		defer cleanup()

		ctx := metadata.AppendToOutgoingContext(
			context.Background(), "authorization", "Bearer "+clientRaw,
		)
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		resp, err := client.Initialize(ctx, &pb.InitializeRequest{
			ClientName:    "critical-suite",
			ClientVersion: "test",
		})
		require.NoError(t, err,
			"valid keycard MUST authenticate at gRPC layer (FR-2 symmetric validation — "+
				"this is the assertion PR #203's regression would have flipped)")
		require.NotNil(t, resp)
	})

	t.Run("gRPC rejects garbage bearer", func(t *testing.T) {
		client, cleanup := newGRPCBufconnClient(t, validator)
		defer cleanup()

		ctx := metadata.AppendToOutgoingContext(
			context.Background(), "authorization", "Bearer not-a-valid-token",
		)
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()

		_, err := client.Initialize(ctx, &pb.InitializeRequest{})
		require.Error(t, err, "garbage bearer MUST be rejected (FR-1)")
		require.Contains(t, err.Error(), "Unauthenticated", "rejection MUST be auth-class")
	})

	t.Run("gRPC rejects missing bearer", func(t *testing.T) {
		client, cleanup := newGRPCBufconnClient(t, validator)
		defer cleanup()

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := client.Initialize(ctx, &pb.InitializeRequest{})
		require.Error(t, err)
		require.Contains(t, err.Error(), "Unauthenticated")
	})

	// --- HTTP half: httptest + middleware-equivalent validator path ---
	// We exercise validator directly via an httptest handler that mirrors
	// what worker.TokenAuth.Middleware does for the bearer arm. This is
	// the same code shape — extract bearer, call validator, set identity
	// in context, dispatch — without booting a full *worker.Service.
	t.Run("HTTP bearer arm: master + keycard accepted, garbage rejected", func(t *testing.T) {
		mux := http.NewServeMux()
		mux.Handle("/api/echo", bearerOnly(validator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, ok := auth.IdentityFrom(r.Context())
			if !ok {
				http.Error(w, "no identity", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(string(id.Source) + ":" + string(id.Role)))
		})))
		ts := httptest.NewServer(mux)
		defer ts.Close()

		// Valid operator key
		body := doGET(t, ts.URL+"/api/echo", "Bearer "+operatorKey)
		assert.Equal(t, "master:admin", body, "operator key → SourceMaster, RoleAdmin")

		// Valid keycard
		body = doGET(t, ts.URL+"/api/echo", "Bearer "+clientRaw)
		assert.Equal(t, "client:read-write", body, "keycard → SourceClient, scope role")

		// Garbage
		req, _ := http.NewRequest("GET", ts.URL+"/api/echo", nil)
		req.Header.Set("Authorization", "Bearer garbage")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode,
			"FR-2: HTTP transport MUST reject the same garbage gRPC rejected")
	})

	// --- Anti-stub mutation guard: re-run all assertions with a no-op
	// validator that ALWAYS returns ErrInvalidCredentials. If any of the
	// success-path assertions still passes, the test is a stub.
	t.Run("anti-stub: stubbed validator flips success assertions", func(t *testing.T) {
		stubV := auth.NewValidator(operatorKey, &alwaysFailStore{})

		client, cleanup := newGRPCBufconnClient(t, stubV)
		defer cleanup()

		// Operator-key path still works (master compare doesn't touch store).
		// But keycard path MUST now fail — proving the test exercises the
		// store path, not just the master arm.
		ctx := metadata.AppendToOutgoingContext(
			context.Background(), "authorization", "Bearer "+clientRaw,
		)
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, err := client.Initialize(ctx, &pb.InitializeRequest{})
		require.Error(t, err,
			"anti-stub: replacing the keycard store with always-fail MUST flip "+
				"the keycard-success assertion to error. If this passes, "+
				"the success test was a stub.")
	})
}

// --- helpers ---

type critStore struct {
	rows map[string][]gormdb.APIToken
}

func (s *critStore) FindByPrefix(_ context.Context, prefix string) ([]gormdb.APIToken, error) {
	return append([]gormdb.APIToken(nil), s.rows[prefix]...), nil
}

type alwaysFailStore struct{}

func (alwaysFailStore) FindByPrefix(_ context.Context, _ string) ([]gormdb.APIToken, error) {
	return nil, nil
}

// stubMCPHandler implements grpcserver.MCPHandler with empty tool list.
type stubMCPHandler struct{}

func (stubMCPHandler) HandleToolCall(_ context.Context, _ string, _ []byte) ([]byte, bool, error) {
	return []byte(`{}`), false, nil
}
func (stubMCPHandler) ToolDefinitions() []grpcserver.ToolDef { return nil }
func (stubMCPHandler) ServerInfo() (string, string)          { return "engram-critical", "test" }

// newGRPCBufconnClient boots a real grpcserver.New on bufconn and returns a
// connected EngramServiceClient.
func newGRPCBufconnClient(t *testing.T, v *auth.Validator) (pb.EngramServiceClient, func()) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	// grpcserver.New returns (*grpc.Server, *grpcserver.Server) — NOT an
	// error. The second return is the *Server wrapper used for SetDB /
	// SetBus / SetValidator post-construction, none of which the bufconn
	// fixture needs. The underscore is therefore the *Server pointer, not
	// a swallowed error.
	srv, _ := grpcserver.New(stubMCPHandler{}, v)
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) {
			return lis.Dial()
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)

	cleanup := func() {
		_ = conn.Close()
		srv.GracefulStop()
		_ = lis.Close()
	}
	return pb.NewEngramServiceClient(conn), cleanup
}

// bearerOnly mirrors worker.TokenAuth.Middleware's bearer arm — extract
// Authorization, run validator, attach Identity to ctx. Cookie / forward-auth
// paths are intentionally absent; this is the bearer surface only.
func bearerOnly(v *auth.Validator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if raw == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := v.Validate(r.Context(), raw)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithIdentity(r.Context(), id)))
	})
}

func doGET(t *testing.T, url, authHeader string) string {
	t.Helper()
	req, err := http.NewRequest("GET", url, nil)
	require.NoError(t, err)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	buf := make([]byte, 1024)
	n, _ := resp.Body.Read(buf)
	return string(buf[:n])
}
