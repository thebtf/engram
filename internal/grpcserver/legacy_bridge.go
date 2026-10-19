package grpcserver

import (
	"context"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func requireProjectServiceClass(ctx context.Context) error {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "relay project authentication required")
	}
	principal, ok := identity.HAPPrincipal()
	if !ok || principal.Class != auth.HAPPrincipalProject {
		return status.Error(codes.PermissionDenied, "relay request requires project service keycard")
	}
	return nil
}

func requireProjectServiceMatch(ctx context.Context, canonicalProject string) error {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "relay project authentication required")
	}
	if !identity.IsHAPProjectServiceFor(canonicalProject) {
		return status.Error(codes.PermissionDenied, "relay keycard does not match resolved project")
	}
	return nil
}

func rejectHAPCredentialWithoutProject(ctx context.Context) error {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return nil
	}
	if _, isHAP := identity.HAPPrincipal(); isHAP {
		return status.Error(codes.PermissionDenied, "HAP keycard is not allowed on an unscoped gRPC method")
	}
	return nil
}

func (s *Server) requireLegacyDirectMatch(ctx context.Context, project string) error {
	identity, hasIdentity := auth.IdentityFrom(ctx)
	_, isHAP := identity.HAPPrincipal()
	if !config.HAP01BLegacyDirectEnforcementEnabled() && !isHAP {
		return nil
	}
	if s == nil || s.db == nil {
		return status.Error(codes.FailedPrecondition, "legacy direct project resolution unavailable")
	}
	canonicalProject, err := gormdb.ResolveProjectIDStrict(ctx, s.db, project)
	if err != nil {
		return status.Error(codes.PermissionDenied, "legacy direct project is missing or ambiguous")
	}
	if !hasIdentity {
		return status.Error(codes.Unauthenticated, "legacy direct authentication required")
	}
	if !identity.IsHAPLegacyDirectFor(canonicalProject, time.Now().UTC()) {
		return status.Error(codes.PermissionDenied, "legacy direct keycard does not match project")
	}
	return nil
}
