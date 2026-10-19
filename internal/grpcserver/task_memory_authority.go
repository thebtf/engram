package grpcserver

import (
	"context"
	"errors"

	"github.com/google/uuid"
	engramgorm "github.com/thebtf/engram/internal/db/gorm"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"gorm.io/gorm"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskmemory"
	pb "github.com/thebtf/engram/proto/engram/v1"
)

const taskMemoryAuthorityUnavailableMessage = "task memory authority unavailable"

const projectIdentityResolutionUnavailableMessage = "project identity resolution unavailable"

// ResolveTaskMemoryAuthority adapts authenticated context and gRPC V3 resolution to task-memory authority.
func (s *Server) ResolveTaskMemoryAuthority(ctx context.Context, evidence taskmemory.ProjectEvidenceV3) (taskmemory.AuthorizedTaskContext, error) {
	if s == nil {
		return taskmemory.AuthorizedTaskContext{}, status.Error(codes.Unavailable, taskMemoryAuthorityUnavailableMessage)
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
		return taskmemory.AuthorizedTaskContext{}, status.Error(codes.Unavailable, taskMemoryAuthorityUnavailableMessage)
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
	authority, err := taskmemory.NewAuthorizedTaskContext(caller, resolution, auditcontext.SourceSession(ctx))
	if err != nil {
		if errors.Is(err, taskmemory.ErrUnauthorized) {
			return taskmemory.AuthorizedTaskContext{}, status.Error(codes.PermissionDenied, "task memory authority denied")
		}
		return taskmemory.AuthorizedTaskContext{}, status.Error(codes.Unavailable, taskMemoryAuthorityUnavailableMessage)
	}
	return authority, nil
}

// grpcV3AuthorizationVerifier admits only the opaque reference generated in
// this transport after gRPC authentication has established an identity.
type grpcV3AuthorizationVerifier struct {
	authorization projectidentity.AuthorizationReferenceV3
}

func (verifier grpcV3AuthorizationVerifier) VerifyAuthorizationV3(ctx context.Context, request projectidentity.AuthorizationVerificationRequestV3) (projectidentity.AuthorizationVerificationV3, error) {
	if request.Authorization() != verifier.authorization {
		return projectidentity.AuthorizationVerificationV3{}, nil
	}
	_, ok := auth.IdentityFrom(ctx)
	if !ok {
		return projectidentity.AuthorizationVerificationV3{}, nil
	}
	switch request.Intent() {
	case projectidentity.ReadFilterIntentV3:
		return projectidentity.AuthorizationVerificationV3{Authorized: true}, nil
	}
	return projectidentity.AuthorizationVerificationV3{}, nil
}

func grpcProjectIdentityV3Request(identity *pb.ProjectIdentityV3, intent projectidentity.ResolutionIntentV3, correlation projectidentity.CorrelationV3) (projectidentity.ResolveProjectRequestV3, error) {
	if correlation == "" {
		generated, err := projectidentity.NewCorrelationV3(uuid.NewString())
		if err != nil {
			return projectidentity.ResolveProjectRequestV3{}, err
		}
		correlation = generated
	} else if _, err := projectidentity.NewCorrelationV3(string(correlation)); err != nil {
		return projectidentity.ResolveProjectRequestV3{}, err
	}
	authorization, err := projectidentity.NewAuthorizationReferenceV3(uuid.NewString())
	if err != nil {
		return projectidentity.ResolveProjectRequestV3{}, err
	}
	anchor, descriptor := grpcProjectIdentityV3Evidence(identity)
	request := projectidentity.ResolveProjectRequestV3{
		Intent:      intent,
		Anchor:      anchor,
		Descriptor:  descriptor,
		Correlation: correlation,
	}
	switch intent {
	case projectidentity.ReadFilterIntentV3:
		readFilter, err := projectidentity.NewReadFilterRequirementV3(authorization, correlation)
		if err != nil {
			return projectidentity.ResolveProjectRequestV3{}, err
		}
		request.ReadFilter = &readFilter
	default:
		return projectidentity.ResolveProjectRequestV3{}, errors.New("unsupported gRPC V3 resolution intent")
	}
	return request, nil
}

func grpcProjectIdentityV3Evidence(identity *pb.ProjectIdentityV3) (projectidentity.AnchorV3, projectidentity.DescriptorV3) {
	legacy := make([]projectidentity.LegacyIdentifierV3, len(identity.GetLegacyIdentifiers()))
	for index, identifier := range identity.GetLegacyIdentifiers() {
		legacy[index] = projectidentity.LegacyIdentifierV3{
			Scheme:     projectidentity.LegacyIdentifierSchemeV3(identifier.GetScheme()),
			Value:      identifier.GetValue(),
			Provenance: projectidentity.LegacyIdentifierProvenanceV3(identifier.GetProvenance()),
		}
	}
	anchor := projectidentity.AnchorV3{
		Version:   int(identity.GetVersion()),
		ProjectID: identity.GetAnchorProjectId(),
		Name:      identity.GetName(),
		Scope:     identity.GetScope(),
	}
	return anchor, projectidentity.DescriptorV3{
		Version:              int(identity.GetVersion()),
		AnchorProjectID:      identity.GetAnchorProjectId(),
		Name:                 identity.GetName(),
		Scope:                identity.GetScope(),
		NormalizedGitRemotes: identity.GetNormalizedGitRemotes(),
		LegacyIdentifiers:    legacy,
		ClientInstanceID:     identity.GetClientInstanceId(),
	}
}

func (s *Server) resolveProjectIdentityV3(ctx context.Context, identity *pb.ProjectIdentityV3, intent projectidentity.ResolutionIntentV3) (projectidentity.ResolutionResultV3, error) {
	request, err := grpcProjectIdentityV3Request(identity, intent, "")
	if err != nil {
		return projectidentity.ResolutionResultV3{}, status.Error(codes.Internal, projectIdentityResolutionUnavailableMessage)
	}
	resolver := s.identityResolverV3
	if resolver == nil {
		resolver = func(ctx context.Context, db *gorm.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
			authorization := request.ReadFilter.Authorization()
			resolved, err := projectidentity.NewResolverV3(&engramgorm.Store{DB: db}, grpcV3AuthorizationVerifier{authorization: authorization}).ResolveProjectV3(ctx, request)
			return resolved.Resolution(), err
		}
	}
	resolution, err := resolver(ctx, s.db, request)
	if err := projectIdentityV3Error(resolution, err); err != nil {
		return projectidentity.ResolutionResultV3{}, err
	}
	return resolution, nil
}

func projectIdentityV3Error(resolution projectidentity.ResolutionResultV3, resolverErr error) error {
	if resolution.IsRefusal() {
		return projectIdentityV3RefusalStatus(resolution.Outcome(), resolution.Correlation())
	}
	var refusal projectidentity.ResolutionErrorV3
	if errors.As(resolverErr, &refusal) {
		return projectIdentityV3RefusalStatus(refusal.Outcome(), refusal.Correlation())
	}
	if resolverErr != nil || !resolution.Outcome().IsSuccess() {
		return status.Error(codes.Unavailable, projectIdentityResolutionUnavailableMessage)
	}
	return nil
}

func projectIdentityV3RefusalStatus(outcome projectidentity.ResolutionOutcomeV3, correlation projectidentity.CorrelationV3) error {
	code := codes.FailedPrecondition
	switch outcome {
	case projectidentity.ProjectAnchorInvalidOutcomeV3,
		projectidentity.ProjectScopeMismatchOutcomeV3,
		projectidentity.ProjectDescriptorUnsupportedOutcomeV3,
		projectidentity.ProjectDescriptorInvalidOutcomeV3,
		projectidentity.ProjectKeyClientAssertionForbiddenOutcomeV3:
		code = codes.InvalidArgument
	}
	st := status.New(code, "project identity resolution refused")
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{
		Reason:   string(outcome),
		Domain:   "engram.project_identity.v3",
		Metadata: map[string]string{"correlation": string(correlation)},
	})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}
