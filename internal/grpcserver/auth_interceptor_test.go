package grpcserver

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

// stubReader is a minimal auth.TokenStoreReader for interceptor tests.
type stubReader struct{ rows map[string][]gormdb.APIToken }

func (s *stubReader) FindByPrefix(_ context.Context, prefix string) ([]gormdb.APIToken, error) {
	return s.rows[prefix], nil
}

func bearerCtx(t *testing.T, raw string) context.Context {
	t.Helper()
	md := metadata.Pairs("authorization", "Bearer "+raw)
	return metadata.NewIncomingContext(context.Background(), md)
}

func makeKeycardRow(t *testing.T, id, raw, scope string) gormdb.APIToken {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.MinCost)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(raw, "engram_"))
	require.GreaterOrEqual(t, len(raw), 15)
	return gormdb.APIToken{
		ID:          id,
		Name:        "test-" + id,
		TokenHash:   string(hash),
		TokenPrefix: raw[7:15],
		Scope:       scope,
	}
}

// echoUnaryHandler returns the request unchanged. Used to confirm the
// interceptor lets a valid call through.
func echoUnaryHandler(_ context.Context, req any) (any, error) {
	return req, nil
}

func TestAuthInterceptor_PingSkipsAuth(t *testing.T) {
	t.Parallel()
	srv := &Server{validator: auth.NewValidator("master", &stubReader{})}

	info := &grpc.UnaryServerInfo{FullMethod: pb.EngramService_Ping_FullMethodName}
	resp, err := srv.authInterceptor(context.Background(), "ping-payload", info, echoUnaryHandler)

	require.NoError(t, err)
	assert.Equal(t, "ping-payload", resp,
		"Ping must reach handler regardless of credentials (monitoring path)")
}

func TestAuthInterceptor_AuthDisabledIdentity(t *testing.T) {
	t.Parallel()
	srv := &Server{}

	var capturedID auth.Identity
	handler := func(ctx context.Context, req any) (any, error) {
		var ok bool
		capturedID, ok = auth.IdentityFrom(ctx)
		require.True(t, ok, "auth-disabled calls must carry an explicit identity")
		return req, nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/engram.v1.EngramService/CallTool"}
	resp, err := srv.authInterceptor(context.Background(), "payload", info, handler)

	require.NoError(t, err)
	assert.Equal(t, "payload", resp)
	assert.Equal(t, auth.SourceAuthDisabled, capturedID.Source)
	assert.True(t, capturedID.IsAdmin(), "ordinary admin-authorized RPCs must remain available")
	assert.False(t, capturedID.IsSessionAdmin(), "session-admin-only routes must reject disabled-auth identity")
}

func TestAuthInterceptor_MissingMetadata(t *testing.T) {
	t.Parallel()
	srv := &Server{validator: auth.NewValidator("master", &stubReader{})}

	info := &grpc.UnaryServerInfo{FullMethod: "/engram.v1.EngramService/CallTool"}
	_, err := srv.authInterceptor(context.Background(), nil, info, echoUnaryHandler)

	st, ok := status.FromError(err)
	require.True(t, ok)
	assert.Equal(t, codes.Unauthenticated, st.Code())
	assert.Equal(t, "missing metadata", st.Message())
}

func TestAuthInterceptor_MissingAuthorizationHeader(t *testing.T) {
	t.Parallel()
	srv := &Server{validator: auth.NewValidator("master", &stubReader{})}

	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{})
	info := &grpc.UnaryServerInfo{FullMethod: "/engram.v1.EngramService/CallTool"}
	_, err := srv.authInterceptor(ctx, nil, info, echoUnaryHandler)

	st, _ := status.FromError(err)
	assert.Equal(t, codes.Unauthenticated, st.Code())
	assert.Equal(t, "missing authorization header", st.Message())
}

func TestAuthInterceptor_MasterToken(t *testing.T) {
	t.Parallel()
	srv := &Server{validator: auth.NewValidator("master-secret", &stubReader{})}

	var captured context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		captured = ctx
		return req, nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/engram.v1.EngramService/CallTool"}
	_, err := srv.authInterceptor(bearerCtx(t, "master-secret"), nil, info, handler)

	require.NoError(t, err)
	require.NotNil(t, captured)
	id, ok := auth.IdentityFrom(captured)
	require.True(t, ok, "identity must be propagated to handler ctx")
	assert.Equal(t, auth.SourceMaster, id.Source)
	assert.Equal(t, auth.RoleAdmin, id.Role)
}

func TestAuthInterceptor_ValidKeycard(t *testing.T) {
	t.Parallel()
	raw := "engram_aaaa111100000000000000000000beef"
	row := makeKeycardRow(t, "uuid-w", raw, "read-write")
	srv := &Server{
		validator: auth.NewValidator("master-secret", &stubReader{
			rows: map[string][]gormdb.APIToken{"aaaa1111": {row}},
		}),
	}

	var captured context.Context
	handler := func(ctx context.Context, req any) (any, error) {
		captured = ctx
		return req, nil
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/engram.v1.EngramService/CallTool"}
	_, err := srv.authInterceptor(bearerCtx(t, raw), nil, info, handler)

	require.NoError(t, err)
	id, ok := auth.IdentityFrom(captured)
	require.True(t, ok)
	assert.Equal(t, auth.SourceClient, id.Source)
	assert.Equal(t, auth.RoleReadWrite, id.Role)
}

func TestAuthInterceptor_InvalidToken(t *testing.T) {
	t.Parallel()
	srv := &Server{validator: auth.NewValidator("master-secret", &stubReader{})}

	info := &grpc.UnaryServerInfo{FullMethod: "/engram.v1.EngramService/CallTool"}
	_, err := srv.authInterceptor(bearerCtx(t, "wrong-secret"), nil, info, echoUnaryHandler)

	st, _ := status.FromError(err)
	assert.Equal(t, codes.Unauthenticated, st.Code())
	assert.Equal(t, "invalid token", st.Message())
}

func TestAuthInterceptor_RawBearerWithoutPrefix(t *testing.T) {
	t.Parallel()
	srv := &Server{validator: auth.NewValidator("master-secret", &stubReader{})}

	// "Bearer " prefix stripping; here we send a raw token (no Bearer prefix)
	// — should still authenticate against master.
	md := metadata.Pairs("authorization", "master-secret")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	info := &grpc.UnaryServerInfo{FullMethod: "/engram.v1.EngramService/CallTool"}
	_, err := srv.authInterceptor(ctx, nil, info, echoUnaryHandler)

	require.NoError(t, err)
}

// stub ServerStream for streaming interceptor tests.
type stubStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *stubStream) Context() context.Context { return s.ctx }

func TestStreamAuthInterceptor_ValidKeycard(t *testing.T) {
	t.Parallel()
	raw := "engram_bbbb222200000000000000000000feed"
	row := makeKeycardRow(t, "uuid-s", raw, "read-write")
	srv := &Server{
		validator: auth.NewValidator("master-secret", &stubReader{
			rows: map[string][]gormdb.APIToken{"bbbb2222": {row}},
		}),
	}

	var capturedID auth.Identity
	streamHandler := func(_ any, ss grpc.ServerStream) error {
		id, ok := auth.IdentityFrom(ss.Context())
		require.True(t, ok)
		capturedID = id
		return nil
	}

	ss := &stubStream{ctx: bearerCtx(t, raw)}
	info := &grpc.StreamServerInfo{FullMethod: "/engram.v1.EngramService/ProjectEvents"}
	err := srv.streamAuthInterceptor(nil, ss, info, streamHandler)

	require.NoError(t, err)
	assert.Equal(t, auth.SourceClient, capturedID.Source)
	assert.Equal(t, "uuid-s", capturedID.KeycardID)
}

func TestStreamAuthInterceptor_AuthDisabledIdentity(t *testing.T) {
	t.Parallel()
	srv := &Server{}

	var capturedID auth.Identity
	streamHandler := func(_ any, ss grpc.ServerStream) error {
		var ok bool
		capturedID, ok = auth.IdentityFrom(ss.Context())
		require.True(t, ok, "auth-disabled streams must carry an explicit identity")
		return nil
	}

	ss := &stubStream{ctx: context.Background()}
	info := &grpc.StreamServerInfo{FullMethod: "/engram.v1.EngramService/ProjectEvents"}
	err := srv.streamAuthInterceptor(nil, ss, info, streamHandler)

	require.NoError(t, err)
	assert.Equal(t, auth.SourceAuthDisabled, capturedID.Source)
	assert.True(t, capturedID.IsAdmin())
	assert.False(t, capturedID.IsSessionAdmin())
}

func TestStreamAuthInterceptor_MissingBearer(t *testing.T) {
	t.Parallel()
	srv := &Server{validator: auth.NewValidator("master-secret", &stubReader{})}

	streamHandler := func(_ any, _ grpc.ServerStream) error {
		t.Fatal("handler must not run on auth failure")
		return nil
	}

	ss := &stubStream{ctx: metadata.NewIncomingContext(context.Background(), metadata.MD{})}
	info := &grpc.StreamServerInfo{FullMethod: "/engram.v1.EngramService/ProjectEvents"}
	err := srv.streamAuthInterceptor(nil, ss, info, streamHandler)

	st, _ := status.FromError(err)
	assert.Equal(t, codes.Unauthenticated, st.Code())
	assert.Equal(t, "missing authorization header", st.Message())
}

func TestStreamAuthInterceptor_RevokedKeycardAtOpen(t *testing.T) {
	t.Parallel()
	// "Revocation" is modelled by the reader returning an empty candidate
	// set for the prefix — TokenStore.FindByPrefix already filters
	// "AND NOT revoked", so this matches production wiring.
	raw := "engram_cccc333300000000000000000000dead"
	srv := &Server{
		validator: auth.NewValidator("master-secret", &stubReader{
			rows: map[string][]gormdb.APIToken{"cccc3333": {}}, // empty — revoked filtered out
		}),
	}

	streamHandler := func(_ any, _ grpc.ServerStream) error {
		t.Fatal("handler must not run on auth failure")
		return nil
	}

	ss := &stubStream{ctx: bearerCtx(t, raw)}
	info := &grpc.StreamServerInfo{FullMethod: "/engram.v1.EngramService/ProjectEvents"}
	err := srv.streamAuthInterceptor(nil, ss, info, streamHandler)

	st, _ := status.FromError(err)
	assert.Equal(t, codes.Unauthenticated, st.Code())
	assert.Equal(t, "invalid token", st.Message(),
		"revoked-equivalent path collapses to invalid token at the wire to avoid leaking revocation state")
}
