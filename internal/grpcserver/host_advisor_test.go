package grpcserver

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/hostadvisor"
	"github.com/thebtf/engram/internal/intervention"
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

type recordingInterventionAdvisor struct {
	advise        func(context.Context, intervention.AdviseInput) (intervention.Decision, error)
	observe       func(context.Context, intervention.ObserveInput) (intervention.ObservationAck, error)
	adviseCalls   int
	observeCalls  int
	adviseInputs  []intervention.AdviseInput
	observeInputs []intervention.ObserveInput
}

func (a *recordingInterventionAdvisor) Advise(ctx context.Context, input intervention.AdviseInput) (intervention.Decision, error) {
	a.adviseCalls++
	a.adviseInputs = append(a.adviseInputs, input)
	if a.advise == nil {
		return intervention.Decision{}, errors.New("unexpected advise call")
	}
	return a.advise(ctx, input)
}

func (a *recordingInterventionAdvisor) Observe(ctx context.Context, input intervention.ObserveInput) (intervention.ObservationAck, error) {
	a.observeCalls++
	a.observeInputs = append(a.observeInputs, input)
	if a.observe == nil {
		return intervention.ObservationAck{}, errors.New("unexpected observe call")
	}
	return a.observe(ctx, input)
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

func TestHostAdvisorDescriptorsStayOnOneServiceAndUseFinalEnvelopes(t *testing.T) {
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

	requireField := func(messageName, fieldName protoreflect.Name, number protoreflect.FieldNumber, kind protoreflect.Kind) protoreflect.FieldDescriptor {
		t.Helper()
		message := file.Messages().ByName(messageName)
		if message == nil {
			t.Fatalf("missing %s", messageName)
		}
		field := message.Fields().ByName(fieldName)
		if field == nil || field.Number() != number || field.Kind() != kind {
			t.Fatalf("%s.%s descriptor = %#v", messageName, fieldName, field)
		}
		return field
	}

	requireField("HostAdvisorAdviseRequest", "binding_id", 1, protoreflect.StringKind)
	if field := requireField("HostAdvisorAdviseRequest", "project_evidence", 2, protoreflect.MessageKind); field.Message().FullName() != "engram.v1.ProjectIdentityV3" {
		t.Fatalf("project evidence descriptor = %#v", field)
	}
	if field := requireField("HostAdvisorAdviseRequest", "occurrence", 3, protoreflect.MessageKind); field.Message().FullName() != "engram.v1.HostAdvisorOccurrence" {
		t.Fatalf("occurrence descriptor = %#v", field)
	}
	advise := file.Messages().ByName("HostAdvisorAdviseResponse")
	if advise == nil || advise.Oneofs().ByName("decision") == nil {
		t.Fatalf("Advise response descriptor = %#v", advise)
	}
	for _, fieldName := range []protoreflect.Name{"emit", "abstain", "delivery_ambiguous", "unavailable"} {
		field := advise.Fields().ByName(fieldName)
		if field == nil || field.Kind() != protoreflect.MessageKind || field.ContainingOneof() != advise.Oneofs().ByName("decision") {
			t.Fatalf("Advise decision field %s = %#v", fieldName, field)
		}
	}

	requireField("HostAdvisorObserveRequest", "binding_id", 1, protoreflect.StringKind)
	observe := file.Messages().ByName("HostAdvisorObserveRequest")
	if observe == nil || observe.Oneofs().ByName("target") == nil {
		t.Fatalf("Observe request descriptor = %#v", observe)
	}
	for _, fieldName := range []protoreflect.Name{"receipt_bound", "channel_gap"} {
		field := observe.Fields().ByName(fieldName)
		if field == nil || field.Kind() != protoreflect.MessageKind || field.ContainingOneof() != observe.Oneofs().ByName("target") {
			t.Fatalf("Observe target field %s = %#v", fieldName, field)
		}
	}
	requireField("HostAdvisorObserveResponse", "state", 1, protoreflect.EnumKind)
	requireField("HostAdvisorObserveResponse", "observation_id", 2, protoreflect.StringKind)
	requireField("HostAdvisorObserveResponse", "reason", 3, protoreflect.EnumKind)
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

func TestHostAdvisorAdviseDelegatesFinalDecisionAndFailsClosed(t *testing.T) {
	clock := time.Now().UTC()
	profile := grpcAdvisorProfileWithCallbackDeadline(t, 500*time.Millisecond)
	server := &Server{handler: hostAdvisorMCPHandler{}}
	server.SetHostAdvisorRegistry(grpcAdvisorRegistry(t, profile, &clock))

	expiresAt := clock.Add(time.Hour)
	identity := auth.ClientWithPrincipalExpiry("read-write", "workstation-keycard", "agent/example", auth.PrincipalKindAgent, &expiresAt)
	ctx := auth.WithIdentity(context.Background(), identity)
	bound, err := server.Bind(ctx, &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	bindingID := bound.GetBinding().GetBindingId()
	emit := grpcAdvisorEmitDecision(t, clock.Add(time.Minute))
	advisor := &recordingInterventionAdvisor{
		advise: func(_ context.Context, input intervention.AdviseInput) (intervention.Decision, error) {
			if input.Occurrence().SessionRef() != "session-one" || input.Occurrence().PhaseAnchorRef() != "turn-one" {
				t.Fatalf("occurrence projection = %#v", input.Occurrence())
			}
			if input.ProjectEvidence().Anchor.ProjectID != grpcV3Identity().GetAnchorProjectId() {
				t.Fatalf("project evidence projection = %#v", input.ProjectEvidence())
			}
			return emit, nil
		},
	}
	server.SetInterventionAdvisor(advisor)

	response, err := server.Advise(ctx, grpcAdvisorAdviseRequest(bindingID))
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	if advisor.adviseCalls != 1 {
		t.Fatalf("advisor calls = %d, want 1", advisor.adviseCalls)
	}
	if response.GetEmit() == nil || response.GetEmit().GetPacket() == nil || response.GetEmit().GetPacket().GetPresentation().GetBoundedText() != "Use the exact receipt-bound reference." {
		t.Fatalf("emit response = %#v", response)
	}
	if response.GetEmit().GetReceipt().GetReceiptId() != response.GetEmit().GetPacket().GetReceipt().GetReceiptId() {
		t.Fatal("emit receipt and packet receipt diverged")
	}

	missing, err := server.Advise(ctx, grpcAdvisorAdviseRequest("missing-binding"))
	if err != nil {
		t.Fatalf("missing binding Advise: %v", err)
	}
	if missing.GetUnavailable() == nil || missing.GetUnavailable().GetCode() != pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_BINDING_UNAVAILABLE {
		t.Fatalf("missing binding response = %#v", missing)
	}
	if advisor.adviseCalls != 1 {
		t.Fatal("missing binding reached the advisor")
	}

	clock = clock.Add(2 * time.Minute)
	expired, err := server.Advise(ctx, grpcAdvisorAdviseRequest(bindingID))
	if err != nil {
		t.Fatalf("expired binding Advise: %v", err)
	}
	if expired.GetUnavailable() == nil || expired.GetUnavailable().GetCode() != pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_BINDING_UNAVAILABLE {
		t.Fatalf("expired binding response = %#v", expired)
	}
	if advisor.adviseCalls != 1 {
		t.Fatal("expired binding reached the advisor")
	}
}

func TestHostAdvisorAdviseRejectsMalformedOrForgedOccurrence(t *testing.T) {
	clock := time.Now().UTC()
	profile := grpcAdvisorProfileWithCallbackDeadline(t, 500*time.Millisecond)
	server := &Server{handler: hostAdvisorMCPHandler{}}
	server.SetHostAdvisorRegistry(grpcAdvisorRegistry(t, profile, &clock))
	expiresAt := clock.Add(time.Hour)
	ctx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipalExpiry("read-write", "workstation-keycard", "agent/example", auth.PrincipalKindAgent, &expiresAt))
	bound, err := server.Bind(ctx, &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	advisor := &recordingInterventionAdvisor{advise: func(context.Context, intervention.AdviseInput) (intervention.Decision, error) {
		return grpcAdvisorEmitDecision(t, clock.Add(time.Minute)), nil
	}}
	server.SetInterventionAdvisor(advisor)

	unknown := grpcAdvisorAdviseRequest(bound.GetBinding().GetBindingId())
	unknown.ProtoReflect().SetUnknown([]byte{0x20, 0x01})
	forged := grpcAdvisorAdviseRequest(bound.GetBinding().GetBindingId())
	forged.Occurrence.Predecessor = grpcAdvisorReceiptIdentity("forged-predecessor", 9)
	badPath := grpcAdvisorAdviseRequest(bound.GetBinding().GetBindingId())
	badPath.Occurrence.BeforeAgentStart.Facts[0].Value = "../outside"

	for name, request := range map[string]*pb.HostAdvisorAdviseRequest{
		"unknown wire field": unknown,
		"forged predecessor": forged,
		"invalid path fact":  badPath,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := server.Advise(ctx, request); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("status = %v, error = %v", status.Code(err), err)
			}
		})
	}
	if advisor.adviseCalls != 0 {
		t.Fatalf("malformed requests reached advisor %d times", advisor.adviseCalls)
	}
}

func TestHostAdvisorAdviseOmitsLatePacketAsDeliveryAmbiguous(t *testing.T) {
	clock := time.Now().UTC()
	profile := grpcAdvisorProfileWithCallbackDeadline(t, 400*time.Millisecond)
	server := &Server{handler: hostAdvisorMCPHandler{}}
	server.SetHostAdvisorRegistry(grpcAdvisorRegistry(t, profile, &clock))
	expiresAt := clock.Add(time.Hour)
	ctx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipalExpiry("read-write", "workstation-keycard", "agent/example", auth.PrincipalKindAgent, &expiresAt))
	bound, err := server.Bind(ctx, &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	emit := grpcAdvisorEmitDecision(t, clock.Add(time.Minute))
	server.SetInterventionAdvisor(&recordingInterventionAdvisor{advise: func(callbackContext context.Context, _ intervention.AdviseInput) (intervention.Decision, error) {
		<-callbackContext.Done()
		return emit, nil
	}})

	response, err := server.Advise(ctx, grpcAdvisorAdviseRequest(bound.GetBinding().GetBindingId()))
	if err != nil {
		t.Fatalf("Advise: %v", err)
	}
	if response.GetDeliveryAmbiguous() == nil || response.GetDeliveryAmbiguous().GetReceipt().GetReceiptId() != "receipt-one" {
		t.Fatalf("late response = %#v", response)
	}
	if response.GetEmit() != nil {
		t.Fatal("late response leaked a packet")
	}
}

func TestHostAdvisorObserveSeparatesReceiptAndChannelTargets(t *testing.T) {
	clock := time.Now().UTC()
	profile := grpcAdvisorProfileWithCallbackDeadline(t, 500*time.Millisecond)
	server := &Server{handler: hostAdvisorMCPHandler{}}
	server.SetHostAdvisorRegistry(grpcAdvisorRegistry(t, profile, &clock))
	expiresAt := clock.Add(time.Hour)
	ctx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipalExpiry("read-write", "workstation-keycard", "agent/example", auth.PrincipalKindAgent, &expiresAt))
	bound, err := server.Bind(ctx, &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	bindingID := bound.GetBinding().GetBindingId()
	advisor := &recordingInterventionAdvisor{}
	advisor.observe = func(_ context.Context, input intervention.ObserveInput) (intervention.ObservationAck, error) {
		switch input.TargetKind() {
		case intervention.ObservationTargetReceiptBound:
			target, ok := input.ReceiptBound()
			if !ok || target.Receipt().ID() != "receipt-one" {
				t.Fatalf("receipt target = %#v", input)
			}
			return intervention.NewAcceptedObservationAck("observation-one", intervention.ObservationReasonAcceptedAttestation)
		case intervention.ObservationTargetChannelGap:
			target, ok := input.ChannelGap()
			if !ok || target.SemanticGap() != intervention.SemanticGapCallbackUnavailable {
				t.Fatalf("channel gap target = %#v", input)
			}
			return intervention.NewAcceptedObservationAck("observation-two", intervention.ObservationReasonAcceptedSemanticGap)
		default:
			return intervention.ObservationAck{}, errors.New("unexpected observation target")
		}
	}
	server.SetInterventionAdvisor(advisor)

	receiptBound := &pb.HostAdvisorObserveRequest{
		BindingId: bindingID,
		Target: &pb.HostAdvisorObserveRequest_ReceiptBound{ReceiptBound: &pb.HostAdvisorReceiptBoundObservation{
			DecisionReceipt:      grpcAdvisorReceiptIdentity("receipt-one", 1),
			ObservationAnchorRef: "observation-anchor-one",
			Evidence: &pb.HostAdvisorReceiptBoundObservation_AdapterAttested{AdapterAttested: &pb.HostAdvisorAdapterAttestation{
				Kind: pb.HostAdvisorAttestationKind_HOST_ADVISOR_ATTESTATION_KIND_DECISION_RECEIVED,
			}},
		}},
	}
	receiptResponse, err := server.Observe(ctx, receiptBound)
	if err != nil {
		t.Fatalf("receipt Observe: %v", err)
	}
	if receiptResponse.GetState() != pb.HostAdvisorObservationState_HOST_ADVISOR_OBSERVATION_STATE_ACCEPTED || receiptResponse.GetObservationId() != "observation-one" || receiptResponse.GetReason() != pb.HostAdvisorObservationReason_HOST_ADVISOR_OBSERVATION_REASON_ACCEPTED_ATTESTATION {
		t.Fatalf("receipt observation response = %#v", receiptResponse)
	}

	channelGap := &pb.HostAdvisorObserveRequest{
		BindingId: bindingID,
		Target: &pb.HostAdvisorObserveRequest_ChannelGap{ChannelGap: &pb.HostAdvisorChannelSemanticGap{
			ObservationAnchorRef: "observation-anchor-two",
			AdapterSemanticGap: &pb.HostAdvisorAdapterSemanticGap{
				Code: pb.HostAdvisorSemanticGapCode_HOST_ADVISOR_SEMANTIC_GAP_CODE_CALLBACK_UNAVAILABLE,
			},
		}},
	}
	channelResponse, err := server.Observe(ctx, channelGap)
	if err != nil {
		t.Fatalf("channel Observe: %v", err)
	}
	if channelResponse.GetState() != pb.HostAdvisorObservationState_HOST_ADVISOR_OBSERVATION_STATE_ACCEPTED || channelResponse.GetObservationId() != "observation-two" || channelResponse.GetReason() != pb.HostAdvisorObservationReason_HOST_ADVISOR_OBSERVATION_REASON_ACCEPTED_SEMANTIC_GAP {
		t.Fatalf("channel observation response = %#v", channelResponse)
	}
	if advisor.observeCalls != 2 {
		t.Fatalf("observe calls = %d, want 2", advisor.observeCalls)
	}

	missing, err := server.Observe(ctx, &pb.HostAdvisorObserveRequest{BindingId: "missing-binding", Target: channelGap.Target})
	if err != nil {
		t.Fatalf("missing binding Observe: %v", err)
	}
	if missing.GetState() != pb.HostAdvisorObservationState_HOST_ADVISOR_OBSERVATION_STATE_UNAVAILABLE || missing.GetReason() != pb.HostAdvisorObservationReason_HOST_ADVISOR_OBSERVATION_REASON_DEPENDENCY_UNAVAILABLE || missing.GetObservationId() != "" {
		t.Fatalf("missing binding observation response = %#v", missing)
	}
	if advisor.observeCalls != 2 {
		t.Fatal("missing observation binding reached the advisor")
	}
}

func TestHostAdvisorObserveRejectsMalformedEnvelope(t *testing.T) {
	clock := time.Now().UTC()
	profile := grpcAdvisorProfileWithCallbackDeadline(t, 500*time.Millisecond)
	server := &Server{handler: hostAdvisorMCPHandler{}}
	server.SetHostAdvisorRegistry(grpcAdvisorRegistry(t, profile, &clock))
	expiresAt := clock.Add(time.Hour)
	ctx := auth.WithIdentity(context.Background(), auth.ClientWithPrincipalExpiry("read-write", "workstation-keycard", "agent/example", auth.PrincipalKindAgent, &expiresAt))
	bound, err := server.Bind(ctx, &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	advisor := &recordingInterventionAdvisor{observe: func(context.Context, intervention.ObserveInput) (intervention.ObservationAck, error) {
		return intervention.NewRejectedObservationAck(), nil
	}}
	server.SetInterventionAdvisor(advisor)

	unknown := &pb.HostAdvisorObserveRequest{BindingId: bound.GetBinding().GetBindingId()}
	unknown.ProtoReflect().SetUnknown([]byte{0x20, 0x01})
	missingEvidence := &pb.HostAdvisorObserveRequest{
		BindingId: bound.GetBinding().GetBindingId(),
		Target: &pb.HostAdvisorObserveRequest_ReceiptBound{ReceiptBound: &pb.HostAdvisorReceiptBoundObservation{
			DecisionReceipt:      grpcAdvisorReceiptIdentity("receipt-one", 1),
			ObservationAnchorRef: "observation-anchor-one",
		}},
	}
	for name, request := range map[string]*pb.HostAdvisorObserveRequest{
		"unknown wire field": unknown,
		"missing evidence":   missingEvidence,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := server.Observe(ctx, request); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("status = %v, error = %v", status.Code(err), err)
			}
		})
	}
	if advisor.observeCalls != 0 {
		t.Fatalf("malformed observations reached advisor %d times", advisor.observeCalls)
	}
}

func TestHostAdvisorAdviseAndObserveAuthenticateBeforeEnvelopeParsing(t *testing.T) {
	server := &Server{handler: hostAdvisorMCPHandler{}}
	calls := map[string]func(context.Context) error{
		"advise": func(ctx context.Context) error {
			_, err := server.Advise(ctx, nil)
			return err
		},
		"observe": func(ctx context.Context) error {
			_, err := server.Observe(ctx, nil)
			return err
		},
	}
	for name, call := range calls {
		t.Run(name+" missing identity", func(t *testing.T) {
			if err := call(context.Background()); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("status = %v, error = %v", status.Code(err), err)
			}
		})
		t.Run(name+" ineligible identity", func(t *testing.T) {
			ctx := auth.WithIdentity(context.Background(), auth.AuthDisabled())
			if err := call(ctx); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("status = %v, error = %v", status.Code(err), err)
			}
		})
	}
}

func grpcAdvisorAdviseRequest(bindingID string) *pb.HostAdvisorAdviseRequest {
	return &pb.HostAdvisorAdviseRequest{
		BindingId:       bindingID,
		ProjectEvidence: grpcV3Identity(),
		Occurrence: &pb.HostAdvisorOccurrence{
			Phase:          pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_BEFORE_AGENT_START,
			SessionRef:     "session-one",
			PhaseAnchorRef: "turn-one",
			BeforeAgentStart: &pb.HostAdvisorBeforeAgentStartFacts{
				TaskQuery: "Review the intervention path",
				Facts: []*pb.HostAdvisorTypedFact{
					{Kind: pb.HostAdvisorFactKind_HOST_ADVISOR_FACT_KIND_PATH, Value: "internal/grpcserver"},
					{Kind: pb.HostAdvisorFactKind_HOST_ADVISOR_FACT_KIND_KEYWORD, Value: "advisor"},
					{Kind: pb.HostAdvisorFactKind_HOST_ADVISOR_FACT_KIND_TOOL, Value: "read"},
				},
			},
		},
	}
}

func grpcAdvisorEmitDecision(t *testing.T, expiresAt time.Time) intervention.Decision {
	t.Helper()
	receipt, err := intervention.NewReceiptIdentity("receipt-one", grpcAdvisorInterventionDigest(1))
	if err != nil {
		t.Fatalf("NewReceiptIdentity: %v", err)
	}
	knowledge, err := intervention.NewKnowledgeReference(42, 3, intervention.CandidateTierExact, grpcAdvisorInterventionDigest(2))
	if err != nil {
		t.Fatalf("NewKnowledgeReference: %v", err)
	}
	presentation, err := intervention.NewUntrustedReferencePresentation("Use the exact receipt-bound reference.")
	if err != nil {
		t.Fatalf("NewUntrustedReferencePresentation: %v", err)
	}
	packet, err := intervention.NewPacket(receipt, expiresAt, knowledge, presentation)
	if err != nil {
		t.Fatalf("NewPacket: %v", err)
	}
	decision, err := intervention.NewEmitDecision(receipt, packet)
	if err != nil {
		t.Fatalf("NewEmitDecision: %v", err)
	}
	return decision
}

func grpcAdvisorReceiptIdentity(id string, seed byte) *pb.HostAdvisorReceiptIdentity {
	digest := grpcAdvisorInterventionDigest(seed)
	return &pb.HostAdvisorReceiptIdentity{ReceiptId: id, IntegritySha256: append([]byte(nil), digest[:]...)}
}

func grpcAdvisorInterventionDigest(seed byte) [32]byte {
	var digest [32]byte
	for index := range digest {
		digest[index] = seed + byte(index)
	}
	return digest
}

func grpcAdvisorProfile(t *testing.T) hostadvisor.AcceptedProfile {
	return grpcAdvisorProfileWithCallbackDeadline(t, 250*time.Millisecond)
}

func grpcAdvisorProfileWithCallbackDeadline(t *testing.T, callbackDeadline time.Duration) hostadvisor.AcceptedProfile {
	t.Helper()
	profile, err := hostadvisor.NewOMPAdvisor1Profile(hostadvisor.OMPAdvisor1ProfileSpec{
		HostVersion:             "omp-1.0.0",
		AdapterID:               "omp-adapter",
		AdapterVersion:          "adapter-1.0.0",
		InstalledArtifactDigest: grpcAdvisorDigest(1),
		RuntimeProbeReceiptID:   "probe-receipt-one",
		SnapshotID:              "snapshot-one",
		SnapshotRevision:        1,
		CallbackDeadline:        callbackDeadline,
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
