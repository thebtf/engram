package grpcserver

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskmemory"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

// ResolveTaskMemoryAuthority adapts gRPC V3 resolution and authenticated context to task-memory authority.
func (s *Server) ResolveTaskMemoryAuthority(ctx context.Context, evidence taskmemory.ProjectEvidenceV3) (taskmemory.AuthorizedTaskContext, error) {
	if s == nil {
		return taskmemory.AuthorizedTaskContext{}, status.Error(codes.Unavailable, "task memory authority unavailable")
	}
	if ctx == nil {
		return taskmemory.AuthorizedTaskContext{}, status.Error(codes.InvalidArgument, "task memory authority context required")
	}
	if evidence.Anchor.Version != evidence.Descriptor.Version ||
		evidence.Anchor.ProjectID != evidence.Descriptor.AnchorProjectID ||
		evidence.Anchor.Name != evidence.Descriptor.Name ||
		evidence.Anchor.Scope != evidence.Descriptor.Scope {
		return taskmemory.AuthorizedTaskContext{}, status.Error(codes.InvalidArgument, "task memory project evidence is invalid")
	}

	legacyIdentifiers := make([]*pb.ProjectLegacyIdentifierV3, len(evidence.Descriptor.LegacyIdentifiers))
	for index, identifier := range evidence.Descriptor.LegacyIdentifiers {
		legacyIdentifiers[index] = &pb.ProjectLegacyIdentifierV3{
			Scheme:     string(identifier.Scheme),
			Value:      identifier.Value,
			Provenance: string(identifier.Provenance),
		}
	}
	identity := &pb.ProjectIdentityV3{
		Version:              uint32(evidence.Anchor.Version),
		AnchorProjectId:      evidence.Anchor.ProjectID,
		Name:                 evidence.Anchor.Name,
		Scope:                evidence.Anchor.Scope,
		NormalizedGitRemotes: append([]string(nil), evidence.Descriptor.NormalizedGitRemotes...),
		LegacyIdentifiers:    legacyIdentifiers,
		ClientInstanceId:     evidence.Descriptor.ClientInstanceID,
	}
	resolution, err := s.resolveProjectIdentityV3(ctx, identity, projectidentity.ReadFilterIntentV3)
	if err != nil {
		return taskmemory.AuthorizedTaskContext{}, err
	}

	authenticated, ok := auth.IdentityFrom(ctx)
	if !ok {
		return taskmemory.AuthorizedTaskContext{}, status.Error(codes.Unauthenticated, "task memory authentication required")
	}
	principal, principalKind, hasPrincipal := authenticated.MemoryOwner()
	if !hasPrincipal {
		principal = ""
		principalKind = ""
	}
	caller, err := taskmemory.NewAuthenticatedCaller(
		string(authenticated.Source),
		string(authenticated.Role),
		authenticated.WorkstationID(),
		principal,
		principalKind,
		authenticated.ExpiresAt,
	)
	if err != nil {
		if errors.Is(err, taskmemory.ErrUnauthorized) {
			return taskmemory.AuthorizedTaskContext{}, status.Error(codes.PermissionDenied, "task memory authority denied")
		}
		return taskmemory.AuthorizedTaskContext{}, status.Error(codes.Unavailable, "task memory authority unavailable")
	}
	authority, err := taskmemory.NewAuthorizedTaskContext(caller, resolution, auditcontext.SourceSession(ctx))
	if err != nil {
		if errors.Is(err, taskmemory.ErrUnauthorized) {
			return taskmemory.AuthorizedTaskContext{}, status.Error(codes.PermissionDenied, "task memory authority denied")
		}
		return taskmemory.AuthorizedTaskContext{}, status.Error(codes.Unavailable, "task memory authority unavailable")
	}
	return authority, nil
}
