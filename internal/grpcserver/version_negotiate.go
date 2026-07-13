package grpcserver

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/thebtf/engram/internal/module/obs"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// NegotiateVersion validates MAJOR-version compatibility between a client and the server.
// Versions may optionally start with a leading "v" and must include at least a major segment.
func (s *Server) NegotiateVersion(ctx context.Context, req *pb.NegotiateVersionRequest) (*pb.NegotiateVersionResponse, error) {
	if req.GetClientVersion() == "" {
		obs.RecordRuntimeEvent(ctx, "client_version", "missing")
		return nil, status.Error(codes.InvalidArgument, "client_version must not be empty")
	}
	if s.handler == nil {
		obs.RecordRuntimeEvent(ctx, "client_version", "server_info_unavailable")
		return nil, status.Error(codes.Unavailable, "server info unavailable")
	}

	_, serverVersion := s.handler.ServerInfo()
	clientMajor, err := parseMajorVersion(req.GetClientVersion())
	if err != nil {
		obs.RecordRuntimeEvent(ctx, "client_version", "invalid")
		return nil, status.Errorf(codes.InvalidArgument, "invalid client_version %q: %v", req.GetClientVersion(), err)
	}
	serverMajor, err := parseMajorVersion(serverVersion)
	if err != nil {
		obs.RecordRuntimeEvent(ctx, "client_version", "server_version_invalid")
		return nil, status.Errorf(codes.Internal, "invalid server version %q: %v", serverVersion, err)
	}

	compatible := clientMajor == serverMajor
	response := &pb.NegotiateVersionResponse{
		Compatible:    compatible,
		ServerVersion: serverVersion,
	}
	if compatible {
		return response, nil
	}
	obs.RecordRuntimeEvent(ctx, "client_version", "incompatible")

	response.IncompatReason = fmt.Sprintf(
		"client major version %d is incompatible with server major version %d; upgrade or downgrade the client to match server version %s",
		clientMajor,
		serverMajor,
		serverVersion,
	)
	return response, nil
}

func parseMajorVersion(version string) (int, error) {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return 0, fmt.Errorf("version must not be empty")
	}
	trimmed = strings.TrimPrefix(trimmed, "v")
	parts := strings.Split(trimmed, ".")
	if len(parts) == 0 || parts[0] == "" {
		return 0, fmt.Errorf("missing major version")
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, fmt.Errorf("major version must be numeric")
	}
	if major < 0 {
		return 0, fmt.Errorf("major version must be non-negative")
	}
	return major, nil
}
