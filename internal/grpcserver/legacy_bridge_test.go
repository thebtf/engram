package grpcserver

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGenericGRPCRejectsEveryHAPCredentialClass(t *testing.T) {
	server := &Server{handler: staticMCPHandler{}}
	registration := auth.ClientWithPrincipal("read-write", "registration-keycard", auth.RegistrationServicePrincipal("workstation-a"), auth.PrincipalKindService)
	response, err := server.CallTool(auth.WithIdentity(context.Background(), registration), &pb.CallToolRequest{
		ToolName: "recall_memory", ArgumentsJson: []byte(`{}`), Project: grpcV3ProjectKey,
	})
	require.Nil(t, response)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
	negotiated, err := server.NegotiateVersion(auth.WithIdentity(context.Background(), registration), &pb.NegotiateVersionRequest{ClientVersion: "v1.0.0"})
	require.Nil(t, negotiated)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	future := time.Now().UTC().Add(time.Hour)
	legacy := auth.ClientWithPrincipalExpiry("read-write", "legacy-keycard", auth.LegacyDirectPrincipal(grpcV3ProjectKey), auth.PrincipalKindAgent, &future)
	initialized, err := server.Initialize(auth.WithIdentity(context.Background(), legacy), &pb.InitializeRequest{})
	require.Nil(t, initialized)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	wrongProject := auth.ClientWithPrincipal("read-write", "project-keycard", auth.ProjectServicePrincipal("22222222-2222-4222-8222-222222222222"), auth.PrincipalKindService)
	response, err = server.CallTool(auth.WithIdentity(context.Background(), wrongProject), &pb.CallToolRequest{
		ToolName: "recall_memory", ArgumentsJson: []byte(`{}`), Project: grpcV3ProjectKey,
	})
	require.Nil(t, response)
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	matchingProject := auth.ClientWithPrincipal("read-write", "project-keycard", auth.ProjectServicePrincipal(grpcV3ProjectKey), auth.PrincipalKindService)
	response, err = server.CallTool(auth.WithIdentity(context.Background(), matchingProject), &pb.CallToolRequest{
		ToolName: "vault", ArgumentsJson: []byte(`{"action":"get","name":"global-secret","scope":"global"}`), Project: grpcV3ProjectKey,
	})
	require.Nil(t, response)
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}
