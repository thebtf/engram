package grpcserver

import (
	"context"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/worker/ambientcore"
	"github.com/thebtf/engram/pkg/cognitive"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	maxRelayHostSessionRefBytes = 256
	maxRelayRevisionBytes       = 128
	maxAmbientQueryTextBytes    = 12 * 1024
)

type hapRelayRegistrationContextKey struct{}

func withHAPRelayRegistration(ctx context.Context) context.Context {
	return context.WithValue(ctx, hapRelayRegistrationContextKey{}, struct{}{})
}

func isHAPRelayRegistration(ctx context.Context) bool {
	_, ok := ctx.Value(hapRelayRegistrationContextKey{}).(struct{})
	return ok
}

func (s *Server) authorizeRegistrationIdentity(ctx context.Context, req *pb.RegisterProjectIdentityV3Request) error {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "project identity authentication required")
	}
	if req != nil && req.GetRelayRevision() != "" {
		if err := requireRelayRevision(req.GetRelayRevision()); err != nil {
			return err
		}
		if !identity.CanHAPRegisterProjectIdentity() {
			return status.Error(codes.PermissionDenied, "relay registration requires read-write registration service keycard")
		}
		return nil
	}
	if identity.Source == auth.SourceAuthDisabled {
		return status.Error(codes.PermissionDenied, "project identity registration unavailable when authentication is disabled")
	}
	if identity.Source != auth.SourceMaster || identity.Role != auth.RoleAdmin {
		return status.Error(codes.PermissionDenied, "project identity registration requires master admin identity")
	}
	return nil
}

func isRelaySessionStartRequest(req *pb.GetSessionStartContextRequest) bool {
	return req != nil && (req.GetHostSessionRef() != "" || req.GetRelayRevision() != "")
}

func validateRelaySessionStart(ctx context.Context, req *pb.GetSessionStartContextRequest) (bool, error) {
	if req == nil {
		return false, status.Error(codes.InvalidArgument, "session-start request required")
	}
	if !isRelaySessionStartRequest(req) {
		return false, nil
	}
	if err := requireRelayRevision(req.GetRelayRevision()); err != nil {
		return false, err
	}
	if err := requireRelayHostSessionRef(req.GetHostSessionRef()); err != nil {
		return false, err
	}
	if req.GetProject() != "" {
		return false, status.Error(codes.InvalidArgument, "relay session-start forbids raw project")
	}
	if req.GetProjectIdentityV3() == nil {
		return false, status.Error(codes.InvalidArgument, "relay session-start requires project_identity_v3")
	}
	if err := requireProjectServiceClass(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func requireRelayRevision(revision string) error {
	if revision == "" || strings.TrimSpace(revision) != revision || len(revision) > maxRelayRevisionBytes {
		return status.Error(codes.InvalidArgument, "relay revision required")
	}
	if !config.HAP01BRelayRevisionEnabled(revision) {
		return status.Error(codes.FailedPrecondition, "relay revision is not enabled")
	}
	return nil
}

func requireRelayHostSessionRef(hostSessionRef string) error {
	if hostSessionRef == "" || len(hostSessionRef) > maxRelayHostSessionRefBytes || !utf8.ValidString(hostSessionRef) || strings.TrimSpace(hostSessionRef) != hostSessionRef {
		return status.Error(codes.InvalidArgument, "relay host_session_ref required")
	}
	for _, character := range hostSessionRef {
		if unicode.IsControl(character) || unicode.IsSpace(character) || character == '/' || character == '\\' || character == '@' {
			return status.Error(codes.InvalidArgument, "relay host_session_ref is invalid")
		}
	}
	return nil
}

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

// GetAmbientCandidates is the one private ambient facade on the existing
// EngramService. It accepts only a relay-bound V3 descriptor and returns an
// empty response on ambient subsystem failure, matching the existing fail-open
// behavior.
func (s *Server) GetAmbientCandidates(ctx context.Context, req *pb.GetAmbientCandidatesRequest) (*pb.GetAmbientCandidatesResponse, error) {
	if req == nil || len(req.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "ambient request required")
	}
	if err := requireRelayRevision(req.GetRelayRevision()); err != nil {
		return nil, err
	}
	if err := requireRelayHostSessionRef(req.GetHostSessionRef()); err != nil {
		return nil, err
	}
	identity := req.GetProjectIdentityV3()
	if identity == nil || len(identity.ProtoReflect().GetUnknown()) != 0 {
		return nil, status.Error(codes.InvalidArgument, "ambient requires project_identity_v3")
	}
	if req.GetQueryText() == "" || len(req.GetQueryText()) > maxAmbientQueryTextBytes {
		return nil, status.Error(codes.InvalidArgument, "ambient query_text is invalid")
	}
	if err := requireProjectServiceClass(ctx); err != nil {
		return nil, err
	}

	resolution, err := s.resolveProjectIdentityV3(ctx, identity, projectidentity.ReadFilterIntentV3)
	if err != nil {
		return nil, err
	}
	canonicalProject := string(resolution.CanonicalProjectKey())
	if err := requireProjectServiceMatch(ctx, canonicalProject); err != nil {
		return nil, err
	}

	result := ambientcore.Deliver(ctx, s.currentAmbientDependencies(), ambientcore.Request{
		SessionID:  req.GetHostSessionRef(),
		Project:    canonicalProject,
		PromptText: req.GetQueryText(),
		Limit:      int(req.GetLimit()),
		Surface:    cognitive.HintSurfaceUserPromptSubmit,
	})
	return &pb.GetAmbientCandidatesResponse{AdditionalContext: ambientcore.BoundedAdditionalContext(result.AdditionalContext)}, nil
}
