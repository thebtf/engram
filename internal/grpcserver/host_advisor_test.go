package grpcserver

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/hostadvisor"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

type hostAdvisorMCPHandler struct{}

func (hostAdvisorMCPHandler) HandleToolCall(context.Context, string, []byte) ([]byte, bool, error) {
	return nil, false, nil
}

func (hostAdvisorMCPHandler) ToolDefinitions() []ToolDef {
	return []ToolDef{{Name: "recall_memory", Description: "recall"}}
}

func (hostAdvisorMCPHandler) ServerInfo() (string, string) {
	return "engram", "test-version"
}

func TestHostAdvisorInitializeAndBindShareAuthenticatedSubjectProof(t *testing.T) {
	profile := grpcAdvisorProfile(t)
	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	registry := grpcAdvisorRegistry(t, profile, &clock)
	server := &Server{handler: hostAdvisorMCPHandler{}}
	server.SetHostAdvisorRegistry(registry)

	expiresAt := clock.Add(time.Hour)
	identity := auth.ClientWithPrincipalExpiry("read-write", "workstation-keycard", "agent/example", auth.PrincipalKindAgent, &expiresAt)
	ctx := auth.WithIdentity(context.Background(), identity)
	initialized, err := server.Initialize(ctx, &pb.InitializeRequest{})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if len(initialized.GetAuthenticatedSubjectProofSha256()) != 32 {
		t.Fatalf("Initialize proof length = %d", len(initialized.GetAuthenticatedSubjectProofSha256()))
	}

	bound, err := server.Bind(ctx, &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if bound.GetBinding() == nil || !bytes.Equal(initialized.GetAuthenticatedSubjectProofSha256(), bound.GetBinding().GetAuthenticatedSubjectProofSha256()) {
		t.Fatalf("Initialize proof %x and Bind proof %#v differ", initialized.GetAuthenticatedSubjectProofSha256(), bound.GetBinding())
	}
	if bound.GetBinding().GetBindingId() == "" || bound.GetBinding().GetCapabilitySnapshot() == nil || len(bound.GetBinding().GetCapabilitySnapshot().GetContractSha256()) != 32 {
		t.Fatalf("binding response = %#v", bound.GetBinding())
	}
	capabilities := bound.GetBinding().GetCapabilitySnapshot().GetCapabilities()
	if len(capabilities) != 1 || len(capabilities[0].GetAllowedActions()) != 2 || capabilities[0].GetAllowedActions()[0] != pb.HostAdvisorAction_HOST_ADVISOR_ACTION_EMIT_ADVICE || capabilities[0].GetAllowedActions()[1] != pb.HostAdvisorAction_HOST_ADVISOR_ACTION_ADAPTER_ATTESTATION {
		t.Fatalf("accepted capability = %#v", capabilities)
	}
}

func TestHostAdvisorInitializeProofIsDarkForIneligibleIdentity(t *testing.T) {
	server := &Server{handler: hostAdvisorMCPHandler{}}
	for name, ctx := range map[string]context.Context{
		"missing":        context.Background(),
		"auth disabled":  auth.WithIdentity(context.Background(), auth.AuthDisabled()),
		"master":         auth.WithIdentity(context.Background(), auth.Admin()),
		"session":        auth.WithIdentity(context.Background(), auth.Session("admin")),
		"read only":      auth.WithIdentity(context.Background(), auth.Client("read-only", "readonly-keycard")),
		"no workstation": auth.WithIdentity(context.Background(), auth.Client("read-write", "")),
	} {
		t.Run(name, func(t *testing.T) {
			response, err := server.Initialize(ctx, &pb.InitializeRequest{})
			if err != nil {
				t.Fatalf("Initialize: %v", err)
			}
			if got := response.GetAuthenticatedSubjectProofSha256(); len(got) != 0 {
				t.Fatalf("ineligible identity received proof %x", got)
			}
		})
	}
}

func TestHostAdvisorBindRejectsEveryIneligibleIdentity(t *testing.T) {
	profile := grpcAdvisorProfile(t)
	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	server := &Server{handler: hostAdvisorMCPHandler{}}
	server.SetHostAdvisorRegistry(grpcAdvisorRegistry(t, profile, &clock))
	request := &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")}
	if _, err := server.Bind(context.Background(), request); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing identity status = %v, want Unauthenticated", status.Code(err))
	}
	expiresAt := time.Now().UTC().Add(time.Hour)
	for name, identity := range map[string]auth.Identity{
		"auth disabled":            auth.AuthDisabled(),
		"master":                   auth.Admin(),
		"session":                  auth.Session("admin"),
		"read only":                auth.Client("read-only", "readonly-keycard"),
		"empty keycard":            auth.Client("read-write", ""),
		"HAP registration":         auth.ClientWithPrincipal("read-write", "registration-keycard", auth.RegistrationServicePrincipal("workstation-a"), auth.PrincipalKindService),
		"HAP project service":      auth.ClientWithPrincipal("read-write", "project-keycard", auth.ProjectServicePrincipal("canonical-project"), auth.PrincipalKindService),
		"HAP legacy direct":        auth.ClientWithPrincipalExpiry("read-write", "legacy-keycard", auth.LegacyDirectPrincipal("canonical-project"), auth.PrincipalKindAgent, &expiresAt),
		"HAP namespace wrong kind": auth.ClientWithPrincipal("read-write", "wrong-kind-keycard", auth.ProjectServicePrincipal("canonical-project"), auth.PrincipalKindAgent),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := server.Bind(auth.WithIdentity(context.Background(), identity), request)
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("status = %v, error = %v", status.Code(err), err)
			}
		})
	}
}

func TestHostAdvisorBindDefaultDarkAndNullableRegistry(t *testing.T) {
	profile := grpcAdvisorProfile(t)
	ctx := auth.WithIdentity(context.Background(), auth.Client("read-write", "workstation-keycard"))
	request := &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")}
	server := &Server{handler: hostAdvisorMCPHandler{}}
	if _, err := server.Bind(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("default-dark status = %v, error = %v", status.Code(err), err)
	}

	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	server.SetHostAdvisorRegistry(grpcAdvisorRegistry(t, profile, &clock))
	if _, err := server.Bind(ctx, request); err != nil {
		t.Fatalf("Bind after registry injection: %v", err)
	}
	server.SetHostAdvisorRegistry(nil)
	if _, err := server.Bind(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("cleared registry status = %v, error = %v", status.Code(err), err)
	}
}

func TestHostAdvisorBindMapsStrictBoundaryAndCatalogErrors(t *testing.T) {
	profile := grpcAdvisorProfile(t)
	clock := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	server := &Server{handler: hostAdvisorMCPHandler{}}
	server.SetHostAdvisorRegistry(grpcAdvisorRegistry(t, profile, &clock))
	ctx := auth.WithIdentity(context.Background(), auth.Client("read-write", "workstation-keycard"))

	for name, request := range map[string]*pb.HostAdvisorBindRequest{
		"missing hello": {},
		"bad digest": {
			Hello: func() *pb.HostHello {
				hello := grpcAdvisorHello(profile, "runtime-one")
				hello.EvidenceRef.Artifact.DigestSha256 = []byte{1}
				return hello
			}(),
		},
		"unknown wire field": func() *pb.HostAdvisorBindRequest {
			request := &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")}
			request.ProtoReflect().SetUnknown([]byte{0x18, 0x01})
			return request
		}(),
		"too many capabilities": func() *pb.HostAdvisorBindRequest {
			hello := grpcAdvisorHello(profile, "runtime-one")
			capability := hello.RequestedCapabilities[0]
			hello.RequestedCapabilities = make([]*pb.HostCapability, maxHostAdvisorCapabilities+1)
			for index := range hello.RequestedCapabilities {
				hello.RequestedCapabilities[index] = proto.Clone(capability).(*pb.HostCapability)
			}
			return &pb.HostAdvisorBindRequest{Hello: hello}
		}(),
		"too many actions": func() *pb.HostAdvisorBindRequest {
			hello := grpcAdvisorHello(profile, "runtime-one")
			hello.RequestedCapabilities[0].AllowedActions = make([]pb.HostAdvisorAction, maxHostAdvisorActions+1)
			return &pb.HostAdvisorBindRequest{Hello: hello}
		}(),
		"too many injection modes": func() *pb.HostAdvisorBindRequest {
			hello := grpcAdvisorHello(profile, "runtime-one")
			hello.RequestedCapabilities[0].ContextInjectionModes = make([]pb.HostAdvisorContextInjectionMode, maxHostAdvisorInjectionModes+1)
			return &pb.HostAdvisorBindRequest{Hello: hello}
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := server.Bind(ctx, request)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("status = %v, error = %v", status.Code(err), err)
			}
		})
	}

	unsupported := grpcAdvisorHello(profile, "runtime-one")
	unsupported.RequestedCapabilities[0].AllowedActions = []pb.HostAdvisorAction{
		pb.HostAdvisorAction_HOST_ADVISOR_ACTION_EMIT_ADVICE,
		pb.HostAdvisorAction_HOST_ADVISOR_ACTION_ALLOW,
	}
	if _, err := server.Bind(ctx, &pb.HostAdvisorBindRequest{Hello: unsupported}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unsupported catalog status = %v, error = %v", status.Code(err), err)
	}
}

func TestHostAdvisorDescriptorsStayOnOneServiceAndDarkMethodsUnimplemented(t *testing.T) {
	file := pb.File_proto_engram_v1_engram_proto
	services := file.Services()
	if services.Len() != 1 {
		t.Fatalf("services = %d, want one", services.Len())
	}
	service := services.Get(0)
	if service.FullName() != "engram.v1.EngramService" {
		t.Fatalf("service = %s", service.FullName())
	}
	for _, methodName := range []protoreflect.Name{"Bind", "Advise", "Observe"} {
		if service.Methods().ByName(methodName) == nil {
			t.Fatalf("missing %s descriptor", methodName)
		}
	}
	initialize := file.Messages().ByName("InitializeResponse")
	proof := initialize.Fields().ByName("authenticated_subject_proof_sha256")
	if proof == nil || proof.Number() != 6 || proof.Kind() != protoreflect.BytesKind {
		t.Fatalf("Initialize proof descriptor = %#v", proof)
	}
	for _, messageName := range []protoreflect.Name{"HostAdvisorBindRequest", "HostAdvisorBindResponse", "HostAdvisorAdviseRequest", "HostAdvisorAdviseResponse", "HostAdvisorObserveRequest", "HostAdvisorObserveResponse"} {
		message := file.Messages().ByName(messageName)
		if message == nil {
			t.Fatalf("missing %s message", messageName)
		}
		for fieldIndex := range message.Fields().Len() {
			fieldName := string(message.Fields().Get(fieldIndex).Name())
			for _, forbidden := range []string{"packet", "iep", "task_memory", "delivery_receipt", "occurrence", "observation"} {
				if fieldName == forbidden {
					t.Fatalf("%s unexpectedly has %s", messageName, fieldName)
				}
			}
		}
	}

	server := &Server{}
	if _, err := server.Advise(context.Background(), &pb.HostAdvisorAdviseRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Advise status = %v, error = %v", status.Code(err), err)
	}
	if _, err := server.Observe(context.Background(), &pb.HostAdvisorObserveRequest{}); status.Code(err) != codes.Unimplemented {
		t.Fatalf("Observe status = %v, error = %v", status.Code(err), err)
	}
}

func TestAuthenticatedSubjectDigestUsesOnlyIdentityFacts(t *testing.T) {
	expiresAt := time.Date(2026, time.August, 31, 13, 0, 0, 0, time.UTC)
	identity := auth.ClientWithPrincipalExpiry("read-write", "keycard-one", "agent/example", auth.PrincipalKindAgent, &expiresAt)
	first, eligible := authenticatedSubjectFromIdentity(identity)
	if !eligible {
		t.Fatal("expected eligible identity")
	}
	second, eligible := authenticatedSubjectFromIdentity(identity)
	if !eligible || !bytes.Equal(first.ProofBytes(), second.ProofBytes()) {
		t.Fatalf("same identity proof differs: %x %x", first.ProofBytes(), second.ProofBytes())
	}
	changedPrincipal := identity
	changedPrincipal.Principal = "agent/other"
	third, eligible := authenticatedSubjectFromIdentity(changedPrincipal)
	if !eligible || bytes.Equal(first.ProofBytes(), third.ProofBytes()) {
		t.Fatal("principal change did not change proof")
	}
	changedKind := identity
	changedKind.PrincipalKind = auth.PrincipalKindService
	fourth, eligible := authenticatedSubjectFromIdentity(changedKind)
	if !eligible || bytes.Equal(first.ProofBytes(), fourth.ProofBytes()) {
		t.Fatal("principal kind change did not change proof")
	}
	changedExpiry := identity
	otherExpiry := expiresAt.Add(time.Minute)
	changedExpiry.ExpiresAt = &otherExpiry
	fifth, eligible := authenticatedSubjectFromIdentity(changedExpiry)
	if !eligible || bytes.Equal(first.ProofBytes(), fifth.ProofBytes()) {
		t.Fatal("expiry change did not change proof")
	}
}

func grpcAdvisorProfile(t *testing.T) hostadvisor.AcceptedProfile {
	t.Helper()
	profile, err := hostadvisor.NewOMPAdvisor1Profile(hostadvisor.OMPAdvisor1ProfileSpec{
		HostVersion:             "omp-1.0.0",
		AdapterID:               "omp-adapter",
		AdapterVersion:          "adapter-1.0.0",
		InstalledArtifactDigest: grpcAdvisorDigest(1),
		RuntimeProbeReceiptID:   "probe-receipt-one",
		SnapshotID:              "snapshot-one",
		SnapshotRevision:        1,
		CallbackDeadline:        250 * time.Millisecond,
		BindingTTL:              time.Minute,
	})
	if err != nil {
		t.Fatalf("NewOMPAdvisor1Profile: %v", err)
	}
	return profile
}

func grpcAdvisorRegistry(t *testing.T, profile hostadvisor.AcceptedProfile, clock *time.Time) *hostadvisor.Registry {
	t.Helper()
	registry, err := hostadvisor.NewRegistry(hostadvisor.RegistryConfig{
		Profiles:     []hostadvisor.AcceptedProfile{profile},
		MaxBindings:  4,
		Now:          func() time.Time { return *clock },
		NewBindingID: grpcAdvisorBindingIDs("binding-one", "binding-two", "binding-three", "binding-four"),
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return registry
}

func grpcAdvisorHello(profile hostadvisor.AcceptedProfile, runtimeRef string) *pb.HostHello {
	capability := profile.Capabilities[0]
	actions := make([]pb.HostAdvisorAction, len(capability.Actions))
	for index, action := range capability.Actions {
		switch action {
		case hostadvisor.ActionEmitAdvice:
			actions[index] = pb.HostAdvisorAction_HOST_ADVISOR_ACTION_EMIT_ADVICE
		case hostadvisor.ActionAdapterAttestation:
			actions[index] = pb.HostAdvisorAction_HOST_ADVISOR_ACTION_ADAPTER_ATTESTATION
		}
	}
	modes := make([]pb.HostAdvisorContextInjectionMode, len(capability.InjectionModes))
	for index, mode := range capability.InjectionModes {
		if mode == hostadvisor.InjectionModeHiddenUntrustedMessage {
			modes[index] = pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_HIDDEN_UNTRUSTED_MESSAGE
		}
	}
	return &pb.HostHello{
		ProtocolRange: &pb.HostAdvisorProtocolRange{MinVersion: profile.Protocol.Min, MaxVersion: profile.Protocol.Max},
		Host: &pb.HostAdvisorHost{
			Family:             pb.HostAdvisorHostFamily_HOST_ADVISOR_HOST_FAMILY_OMP,
			HostVersion:        profile.HostVersion,
			AdapterId:          profile.AdapterID,
			AdapterVersion:     profile.AdapterVersion,
			RuntimeInstanceRef: runtimeRef,
		},
		RequestedCapabilities: []*pb.HostCapability{{
			Semantic:              pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_BEFORE_AGENT_START,
			AllowedActions:        actions,
			ContextInjectionModes: modes,
			Correlation: &pb.HostAdvisorCorrelation{
				Session:           capability.Correlation.Session,
				Turn:              capability.Correlation.Turn,
				ToolAction:        capability.Correlation.ToolAction,
				StablePhaseAnchor: capability.Correlation.StablePhaseAnchor,
			},
			Callback: &pb.HostAdvisorCallback{
				Awaited:    capability.Callback.Awaited,
				DeadlineMs: uint32(capability.Callback.Deadline / time.Millisecond),
				Ordering:   pb.HostAdvisorCallbackOrdering_HOST_ADVISOR_CALLBACK_ORDERING_BEFORE_FIRST_ACTION,
			},
			Acknowledgement: pb.HostAdvisorAcknowledgement_HOST_ADVISOR_ACKNOWLEDGEMENT_ADAPTER_ATTESTED,
		}},
		EvidenceRef: &pb.HostAdvisorEvidenceRef{
			Artifact: &pb.HostAdvisorArtifact{
				Kind:         pb.HostAdvisorArtifactKind_HOST_ADVISOR_ARTIFACT_KIND_INSTALLED,
				DigestSha256: append([]byte(nil), profile.Evidence.ArtifactSHA256[:]...),
			},
			RuntimeProbeReceiptId: profile.Evidence.RuntimeProbeReceiptID,
		},
	}
}

func grpcAdvisorDigest(value byte) hostadvisor.Digest {
	var digest hostadvisor.Digest
	for index := range digest {
		digest[index] = value + byte(index)
	}
	return digest
}

func grpcAdvisorBindingIDs(ids ...hostadvisor.BindingID) func() (hostadvisor.BindingID, error) {
	index := 0
	return func() (hostadvisor.BindingID, error) {
		if index == len(ids) {
			return "", errors.New("test binding IDs exhausted")
		}
		bindingID := ids[index]
		index++
		return bindingID, nil
	}
}
