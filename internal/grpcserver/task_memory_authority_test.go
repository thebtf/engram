package grpcserver

import (
	"context"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	gormlib "gorm.io/gorm"

	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskmemory"
)

func TestResolveTaskMemoryAuthorityUsesReadFilterAndAuthenticatedContext(t *testing.T) {
	evidence := taskMemoryEvidence()
	calls := 0
	server := &Server{identityResolverV3: func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		calls++
		if request.Intent != projectidentity.ReadFilterIntentV3 || request.ReadFilter == nil || request.ReadFilter.Authorization() == "" || request.ReadFilter.Correlation() != request.Correlation {
			t.Fatalf("request=%#v", request)
		}
		if !reflect.DeepEqual(request.Anchor, evidence.Anchor) || !reflect.DeepEqual(request.Descriptor, evidence.Descriptor) {
			t.Fatalf("anchor=%#v descriptor=%#v", request.Anchor, request.Descriptor)
		}
		return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
	}}
	identity := auth.ClientWithPrincipal("read-write", "task-memory-keycard", "agent/alice", auth.PrincipalKindAgent)
	ctx := auth.WithIdentity(auditcontext.WithSourceSession(context.Background(), " source-session "), identity)
	authority, err := server.ResolveTaskMemoryAuthority(ctx, evidence)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || authority.CanonicalProject() != projectidentity.ProjectKeyV3(grpcV3ProjectKey) {
		t.Fatalf("calls=%d authority=%#v", calls, authority)
	}
	if caller := authority.KeycardContext(); caller.WorkstationID != "task-memory-keycard" || caller.SessionID != "source-session" || caller.Principal != "agent/alice" || caller.PrincipalKind != "agent" {
		t.Fatalf("caller=%#v", caller)
	}
}

func TestResolveTaskMemoryAuthorityRejectsAuthenticationFailures(t *testing.T) {
	evidence := taskMemoryEvidence()
	future := time.Now().UTC().Add(time.Hour)
	past := time.Now().UTC().Add(-time.Hour)
	for _, test := range []struct {
		name string
		ctx  context.Context
		code codes.Code
	}{
		{name: "missing identity", ctx: context.Background(), code: codes.Unauthenticated},
		{name: "read only client", ctx: auth.WithIdentity(context.Background(), auth.Client("read-only", "read-only-keycard")), code: codes.PermissionDenied},
		{name: "master identity", ctx: auth.WithIdentity(context.Background(), auth.Admin()), code: codes.PermissionDenied},
		{name: "expired client", ctx: auth.WithIdentity(context.Background(), auth.ClientWithPrincipalExpiry("read-write", "expired-keycard", "agent/alice", auth.PrincipalKindAgent, &past)), code: codes.PermissionDenied},
		{name: "invalid principal kind", ctx: auth.WithIdentity(context.Background(), auth.ClientWithPrincipalExpiry("read-write", "invalid-kind-keycard", "agent/alice", auth.PrincipalKind("invalid"), &future)), code: codes.PermissionDenied},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			server := &Server{identityResolverV3: func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
				calls++
				return grpcV3Result(t, request.Intent, projectidentity.ProjectResolvedOutcomeV3)
			}}
			_, err := server.ResolveTaskMemoryAuthority(test.ctx, evidence)
			if status.Code(err) != test.code || calls != 1 {
				t.Fatalf("error=%v code=%v calls=%d", err, status.Code(err), calls)
			}
		})
	}
}

func TestResolveTaskMemoryAuthorityPreservesV3Refusal(t *testing.T) {
	server := &Server{identityResolverV3: func(_ context.Context, _ *gormlib.DB, request projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		return grpcV3Result(t, request.Intent, projectidentity.ProjectOnboardingRequiredOutcomeV3)
	}}
	ctx := auth.WithIdentity(context.Background(), auth.Client("read-write", "task-memory-keycard"))
	_, err := server.ResolveTaskMemoryAuthority(ctx, taskMemoryEvidence())
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("error=%v code=%v", err, status.Code(err))
	}
}

func TestResolveTaskMemoryAuthorityRejectsMismatchedEvidence(t *testing.T) {
	evidence := taskMemoryEvidence()
	evidence.Descriptor.Name = "different"
	calls := 0
	server := &Server{identityResolverV3: func(_ context.Context, _ *gormlib.DB, _ projectidentity.ResolveProjectRequestV3) (projectidentity.ResolutionResultV3, error) {
		calls++
		return projectidentity.ResolutionResultV3{}, nil
	}}
	ctx := auth.WithIdentity(context.Background(), auth.Client("read-write", "task-memory-keycard"))
	_, err := server.ResolveTaskMemoryAuthority(ctx, evidence)
	if status.Code(err) != codes.InvalidArgument || calls != 0 {
		t.Fatalf("error=%v code=%v calls=%d", err, status.Code(err), calls)
	}
}

func taskMemoryEvidence() taskmemory.ProjectEvidenceV3 {
	identity := grpcV3Identity()
	anchor, descriptor := grpcProjectIdentityV3Evidence(identity)
	return taskmemory.ProjectEvidenceV3{Anchor: anchor, Descriptor: descriptor}
}
