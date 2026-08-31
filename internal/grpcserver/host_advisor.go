package grpcserver

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/hostadvisor"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	authenticatedSubjectProofDomain = "engram.host-advisor.authenticated-subject/v1"
	maxHostAdvisorCapabilities      = 4
	maxHostAdvisorActions           = 5
	maxHostAdvisorInjectionModes    = 3
)

// Bind admits only a validated ordinary workstation keycard into the installed
// host-advisor registry. Advise and Observe intentionally remain provided by
// the embedded generated UnimplementedEngramServiceServer until HAP-03.
func (s *Server) Bind(ctx context.Context, request *pb.HostAdvisorBindRequest) (*pb.HostAdvisorBindResponse, error) {
	subject, err := bindAuthenticatedSubject(ctx)
	if err != nil {
		return nil, err
	}
	registry := s.currentHostAdvisorRegistry()
	if registry == nil {
		return nil, hostAdvisorFailedPrecondition()
	}
	hello, err := hostHelloFromProto(request)
	if err != nil {
		return nil, hostAdvisorInvalidArgument()
	}
	binding, err := registry.Bind(subject, hello)
	if err != nil {
		return nil, hostAdvisorRegistryError(err)
	}
	return &pb.HostAdvisorBindResponse{Binding: hostBindingProto(binding)}, nil
}

func initializeAuthenticatedSubjectProof(ctx context.Context) []byte {
	identity, found := auth.IdentityFrom(ctx)
	if !found {
		return nil
	}
	subject, eligible := authenticatedSubjectFromIdentity(identity)
	if !eligible {
		return nil
	}
	return subject.ProofBytes()
}

func bindAuthenticatedSubject(ctx context.Context) (hostadvisor.AuthenticatedSubject, error) {
	identity, found := auth.IdentityFrom(ctx)
	if !found {
		return hostadvisor.AuthenticatedSubject{}, status.Error(codes.Unauthenticated, "host advisor authentication required")
	}
	subject, eligible := authenticatedSubjectFromIdentity(identity)
	if !eligible {
		return hostadvisor.AuthenticatedSubject{}, status.Error(codes.PermissionDenied, "host advisor binding is not permitted")
	}
	return subject, nil
}

func authenticatedSubjectFromIdentity(identity auth.Identity) (hostadvisor.AuthenticatedSubject, bool) {
	if identity.Source != auth.SourceClient || identity.Role != auth.RoleReadWrite || identity.KeycardID == "" || identity.WorkstationID() == "" {
		return hostadvisor.AuthenticatedSubject{}, false
	}
	if _, reserved := auth.ParseHAPPrincipal(identity.Principal); reserved {
		return hostadvisor.AuthenticatedSubject{}, false
	}
	proof := authenticatedSubjectDigest(identity)
	subject, err := hostadvisor.NewAuthenticatedSubject(proof)
	if err != nil {
		return hostadvisor.AuthenticatedSubject{}, false
	}
	return subject, true
}

func authenticatedSubjectDigest(identity auth.Identity) hostadvisor.Digest {
	hash := sha256.New()
	writeSubjectText(hash, authenticatedSubjectProofDomain)
	writeSubjectText(hash, string(identity.Source))
	writeSubjectText(hash, string(identity.Role))
	writeSubjectText(hash, identity.KeycardID)
	writeSubjectText(hash, identity.Principal)
	writeSubjectText(hash, string(identity.PrincipalKind))
	if identity.ExpiresAt == nil {
		writeSubjectBoolean(hash, false)
	} else {
		writeSubjectBoolean(hash, true)
		var encoded [8]byte
		binary.BigEndian.PutUint64(encoded[:], uint64(identity.ExpiresAt.UTC().UnixNano()))
		_, _ = hash.Write(encoded[:])
	}
	var proof hostadvisor.Digest
	copy(proof[:], hash.Sum(nil))
	return proof
}

func writeSubjectText(hash interface{ Write([]byte) (int, error) }, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = hash.Write(length[:])
	_, _ = hash.Write([]byte(value))
}

func writeSubjectBoolean(hash interface{ Write([]byte) (int, error) }, value bool) {
	if value {
		writeSubjectText(hash, "1")
		return
	}
	writeSubjectText(hash, "0")
}

func hostHelloFromProto(request *pb.HostAdvisorBindRequest) (hostadvisor.HostHello, error) {
	if hostAdvisorMessageMalformed(request) {
		return hostadvisor.HostHello{}, hostadvisor.ErrInvalidInput
	}
	hello := request.GetHello()
	if hostAdvisorMessageMalformed(hello) {
		return hostadvisor.HostHello{}, hostadvisor.ErrInvalidInput
	}
	protocol := hello.GetProtocolRange()
	host := hello.GetHost()
	evidence := hello.GetEvidenceRef()
	if hostAdvisorMessageMalformed(protocol) || hostAdvisorMessageMalformed(host) || hostAdvisorMessageMalformed(evidence) {
		return hostadvisor.HostHello{}, hostadvisor.ErrInvalidInput
	}
	artifact := evidence.GetArtifact()
	if hostAdvisorMessageMalformed(artifact) {
		return hostadvisor.HostHello{}, hostadvisor.ErrInvalidInput
	}

	family, ok := hostFamilyFromProto(host.GetFamily())
	if !ok {
		return hostadvisor.HostHello{}, hostadvisor.ErrInvalidInput
	}
	artifactKind, ok := artifactKindFromProto(artifact.GetKind())
	if !ok {
		return hostadvisor.HostHello{}, hostadvisor.ErrInvalidInput
	}
	digest, err := digestFromProto(artifact.GetDigestSha256())
	if err != nil {
		return hostadvisor.HostHello{}, err
	}

	requestedWire := hello.GetRequestedCapabilities()
	if len(requestedWire) == 0 || len(requestedWire) > maxHostAdvisorCapabilities {
		return hostadvisor.HostHello{}, hostadvisor.ErrInvalidInput
	}
	requested := make([]hostadvisor.Capability, len(requestedWire))
	for index, capability := range requestedWire {
		converted, err := capabilityFromProto(capability)
		if err != nil {
			return hostadvisor.HostHello{}, err
		}
		requested[index] = converted
	}

	return hostadvisor.HostHello{
		Protocol: hostadvisor.ProtocolRange{
			Min: protocol.GetMinVersion(),
			Max: protocol.GetMaxVersion(),
		},
		Host: hostadvisor.HostIdentity{
			Family:             family,
			HostVersion:        host.GetHostVersion(),
			AdapterID:          host.GetAdapterId(),
			AdapterVersion:     host.GetAdapterVersion(),
			RuntimeInstanceRef: host.GetRuntimeInstanceRef(),
		},
		Requested: requested,
		Evidence: hostadvisor.EvidenceRef{
			ArtifactKind:          artifactKind,
			ArtifactSHA256:        digest,
			RuntimeProbeReceiptID: evidence.GetRuntimeProbeReceiptId(),
		},
	}, nil
}

func capabilityFromProto(message *pb.HostCapability) (hostadvisor.Capability, error) {
	if hostAdvisorMessageMalformed(message) {
		return hostadvisor.Capability{}, hostadvisor.ErrInvalidInput
	}
	correlation := message.GetCorrelation()
	callback := message.GetCallback()
	if hostAdvisorMessageMalformed(correlation) || hostAdvisorMessageMalformed(callback) {
		return hostadvisor.Capability{}, hostadvisor.ErrInvalidInput
	}
	semantic, ok := semanticFromProto(message.GetSemantic())
	if !ok {
		return hostadvisor.Capability{}, hostadvisor.ErrInvalidInput
	}
	acknowledgement, ok := acknowledgementFromProto(message.GetAcknowledgement())
	if !ok {
		return hostadvisor.Capability{}, hostadvisor.ErrInvalidInput
	}
	ordering, ok := callbackOrderingFromProto(callback.GetOrdering())
	if !ok {
		return hostadvisor.Capability{}, hostadvisor.ErrInvalidInput
	}

	actionsWire := message.GetAllowedActions()
	if len(actionsWire) == 0 || len(actionsWire) > maxHostAdvisorActions {
		return hostadvisor.Capability{}, hostadvisor.ErrInvalidInput
	}
	actions := make([]hostadvisor.Action, len(actionsWire))
	for index, action := range actionsWire {
		converted, ok := actionFromProto(action)
		if !ok {
			return hostadvisor.Capability{}, hostadvisor.ErrInvalidInput
		}
		actions[index] = converted
	}
	modesWire := message.GetContextInjectionModes()
	if len(modesWire) == 0 || len(modesWire) > maxHostAdvisorInjectionModes {
		return hostadvisor.Capability{}, hostadvisor.ErrInvalidInput
	}
	modes := make([]hostadvisor.InjectionMode, len(modesWire))
	for index, mode := range modesWire {
		converted, ok := injectionModeFromProto(mode)
		if !ok {
			return hostadvisor.Capability{}, hostadvisor.ErrInvalidInput
		}
		modes[index] = converted
	}

	return hostadvisor.Capability{
		Semantic:       semantic,
		Actions:        actions,
		InjectionModes: modes,
		Correlation: hostadvisor.Correlation{
			Session:           correlation.GetSession(),
			Turn:              correlation.GetTurn(),
			ToolAction:        correlation.GetToolAction(),
			StablePhaseAnchor: correlation.GetStablePhaseAnchor(),
		},
		Callback: hostadvisor.CallbackContract{
			Awaited:  callback.GetAwaited(),
			Deadline: time.Duration(callback.GetDeadlineMs()) * time.Millisecond,
			Ordering: ordering,
		},
		Acknowledgement: acknowledgement,
	}, nil
}

func digestFromProto(value []byte) (hostadvisor.Digest, error) {
	if len(value) != sha256.Size {
		return hostadvisor.Digest{}, hostadvisor.ErrInvalidInput
	}
	var digest hostadvisor.Digest
	copy(digest[:], value)
	return digest, nil
}

func hostAdvisorMessageMalformed(message proto.Message) bool {
	return message == nil || !message.ProtoReflect().IsValid() || len(message.ProtoReflect().GetUnknown()) != 0
}

func hostAdvisorRegistryError(err error) error {
	switch {
	case errors.Is(err, hostadvisor.ErrInvalidInput):
		return hostAdvisorInvalidArgument()
	case errors.Is(err, hostadvisor.ErrUnsupported), errors.Is(err, hostadvisor.ErrBindingUnavailable):
		return hostAdvisorFailedPrecondition()
	case errors.Is(err, hostadvisor.ErrCapacity):
		return status.Error(codes.ResourceExhausted, "host advisor binding unavailable")
	default:
		return status.Error(codes.Internal, "host advisor binding failed")
	}
}

func hostAdvisorInvalidArgument() error {
	return status.Error(codes.InvalidArgument, "host advisor request is invalid")
}

func hostAdvisorFailedPrecondition() error {
	return status.Error(codes.FailedPrecondition, "host advisor binding unavailable")
}

func hostBindingProto(binding hostadvisor.HostBinding) *pb.HostBinding {
	snapshot := binding.Snapshot()
	contractDigest := snapshot.ContractDigest()
	return &pb.HostBinding{
		BindingId: binding.ID().String(),
		CapabilitySnapshot: &pb.AcceptedCapabilitySnapshot{
			SnapshotId:         snapshot.SnapshotID(),
			ContractSha256:     append([]byte(nil), contractDigest[:]...),
			Revision:           snapshot.Revision(),
			CapabilityRevision: snapshot.CapabilityRevision(),
			Capabilities:       capabilitiesProto(snapshot.Capabilities()),
		},
		ExpiresAt:                       timestamppb.New(binding.ExpiresAt()),
		CallbackDeadlineMs:              uint32(binding.CallbackDeadline() / time.Millisecond),
		AuthenticatedSubjectProofSha256: binding.Subject().ProofBytes(),
	}
}

func capabilitiesProto(capabilities []hostadvisor.Capability) []*pb.HostCapability {
	result := make([]*pb.HostCapability, len(capabilities))
	for index, capability := range capabilities {
		result[index] = capabilityProto(capability)
	}
	return result
}

func capabilityProto(capability hostadvisor.Capability) *pb.HostCapability {
	actions := make([]pb.HostAdvisorAction, len(capability.Actions))
	for index, action := range capability.Actions {
		actions[index] = actionProto(action)
	}
	modes := make([]pb.HostAdvisorContextInjectionMode, len(capability.InjectionModes))
	for index, mode := range capability.InjectionModes {
		modes[index] = injectionModeProto(mode)
	}
	return &pb.HostCapability{
		Semantic:              semanticProto(capability.Semantic),
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
			Ordering:   callbackOrderingProto(capability.Callback.Ordering),
		},
		Acknowledgement: acknowledgementProto(capability.Acknowledgement),
	}
}

func hostFamilyFromProto(family pb.HostAdvisorHostFamily) (hostadvisor.HostFamily, bool) {
	switch family {
	case pb.HostAdvisorHostFamily_HOST_ADVISOR_HOST_FAMILY_OMP:
		return hostadvisor.HostFamilyOMP, true
	case pb.HostAdvisorHostFamily_HOST_ADVISOR_HOST_FAMILY_CLAUDE_CODE:
		return hostadvisor.HostFamilyClaudeCode, true
	case pb.HostAdvisorHostFamily_HOST_ADVISOR_HOST_FAMILY_CODEX:
		return hostadvisor.HostFamilyCodex, true
	default:
		return hostadvisor.HostFamilyUnspecified, false
	}
}

func artifactKindFromProto(kind pb.HostAdvisorArtifactKind) (hostadvisor.ArtifactKind, bool) {
	switch kind {
	case pb.HostAdvisorArtifactKind_HOST_ADVISOR_ARTIFACT_KIND_INSTALLED:
		return hostadvisor.ArtifactKindInstalled, true
	case pb.HostAdvisorArtifactKind_HOST_ADVISOR_ARTIFACT_KIND_SOURCE:
		return hostadvisor.ArtifactKindSource, true
	default:
		return hostadvisor.ArtifactKindUnspecified, false
	}
}

func semanticFromProto(semantic pb.HostAdvisorSemantic) (hostadvisor.Semantic, bool) {
	switch semantic {
	case pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_BEFORE_AGENT_START:
		return hostadvisor.SemanticBeforeAgentStart, true
	case pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_TOOL_RESULT:
		return hostadvisor.SemanticToolResult, true
	case pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_TOOL_ACTION:
		return hostadvisor.SemanticToolAction, true
	case pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_SESSION_FINALIZATION:
		return hostadvisor.SemanticSessionFinalization, true
	default:
		return hostadvisor.SemanticUnspecified, false
	}
}

func actionFromProto(action pb.HostAdvisorAction) (hostadvisor.Action, bool) {
	switch action {
	case pb.HostAdvisorAction_HOST_ADVISOR_ACTION_EMIT_ADVICE:
		return hostadvisor.ActionEmitAdvice, true
	case pb.HostAdvisorAction_HOST_ADVISOR_ACTION_ADAPTER_ATTESTATION:
		return hostadvisor.ActionAdapterAttestation, true
	case pb.HostAdvisorAction_HOST_ADVISOR_ACTION_ALLOW:
		return hostadvisor.ActionAllow, true
	case pb.HostAdvisorAction_HOST_ADVISOR_ACTION_BLOCK:
		return hostadvisor.ActionBlock, true
	case pb.HostAdvisorAction_HOST_ADVISOR_ACTION_REWRITE:
		return hostadvisor.ActionRewrite, true
	default:
		return hostadvisor.ActionUnspecified, false
	}
}

func injectionModeFromProto(mode pb.HostAdvisorContextInjectionMode) (hostadvisor.InjectionMode, bool) {
	switch mode {
	case pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_HIDDEN_UNTRUSTED_MESSAGE:
		return hostadvisor.InjectionModeHiddenUntrustedMessage, true
	case pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_DEVELOPER_CONTEXT:
		return hostadvisor.InjectionModeDeveloperContext, true
	case pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_ADDITIONAL_CONTEXT:
		return hostadvisor.InjectionModeAdditionalContext, true
	default:
		return hostadvisor.InjectionModeUnspecified, false
	}
}

func callbackOrderingFromProto(ordering pb.HostAdvisorCallbackOrdering) (hostadvisor.CallbackOrdering, bool) {
	switch ordering {
	case pb.HostAdvisorCallbackOrdering_HOST_ADVISOR_CALLBACK_ORDERING_BEFORE_FIRST_ACTION:
		return hostadvisor.CallbackOrderingBeforeFirstAction, true
	case pb.HostAdvisorCallbackOrdering_HOST_ADVISOR_CALLBACK_ORDERING_AFTER_TOOL_RESULT:
		return hostadvisor.CallbackOrderingAfterToolResult, true
	case pb.HostAdvisorCallbackOrdering_HOST_ADVISOR_CALLBACK_ORDERING_OTHER_PROVEN_ORDER:
		return hostadvisor.CallbackOrderingOtherProvenOrder, true
	default:
		return hostadvisor.CallbackOrderingUnspecified, false
	}
}

func acknowledgementFromProto(acknowledgement pb.HostAdvisorAcknowledgement) (hostadvisor.Acknowledgement, bool) {
	switch acknowledgement {
	case pb.HostAdvisorAcknowledgement_HOST_ADVISOR_ACKNOWLEDGEMENT_NONE:
		return hostadvisor.AcknowledgementNone, true
	case pb.HostAdvisorAcknowledgement_HOST_ADVISOR_ACKNOWLEDGEMENT_ADAPTER_ATTESTED:
		return hostadvisor.AcknowledgementAdapterAttested, true
	case pb.HostAdvisorAcknowledgement_HOST_ADVISOR_ACKNOWLEDGEMENT_HOST_ACKNOWLEDGED:
		return hostadvisor.AcknowledgementHostAcknowledged, true
	case pb.HostAdvisorAcknowledgement_HOST_ADVISOR_ACKNOWLEDGEMENT_CONSUMPTION_OBSERVED:
		return hostadvisor.AcknowledgementConsumptionObserved, true
	default:
		return hostadvisor.AcknowledgementUnspecified, false
	}
}

func semanticProto(semantic hostadvisor.Semantic) pb.HostAdvisorSemantic {
	switch semantic {
	case hostadvisor.SemanticBeforeAgentStart:
		return pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_BEFORE_AGENT_START
	case hostadvisor.SemanticToolResult:
		return pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_TOOL_RESULT
	case hostadvisor.SemanticToolAction:
		return pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_TOOL_ACTION
	case hostadvisor.SemanticSessionFinalization:
		return pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_SESSION_FINALIZATION
	default:
		return pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_UNSPECIFIED
	}
}

func actionProto(action hostadvisor.Action) pb.HostAdvisorAction {
	switch action {
	case hostadvisor.ActionEmitAdvice:
		return pb.HostAdvisorAction_HOST_ADVISOR_ACTION_EMIT_ADVICE
	case hostadvisor.ActionAdapterAttestation:
		return pb.HostAdvisorAction_HOST_ADVISOR_ACTION_ADAPTER_ATTESTATION
	case hostadvisor.ActionAllow:
		return pb.HostAdvisorAction_HOST_ADVISOR_ACTION_ALLOW
	case hostadvisor.ActionBlock:
		return pb.HostAdvisorAction_HOST_ADVISOR_ACTION_BLOCK
	case hostadvisor.ActionRewrite:
		return pb.HostAdvisorAction_HOST_ADVISOR_ACTION_REWRITE
	default:
		return pb.HostAdvisorAction_HOST_ADVISOR_ACTION_UNSPECIFIED
	}
}

func injectionModeProto(mode hostadvisor.InjectionMode) pb.HostAdvisorContextInjectionMode {
	switch mode {
	case hostadvisor.InjectionModeHiddenUntrustedMessage:
		return pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_HIDDEN_UNTRUSTED_MESSAGE
	case hostadvisor.InjectionModeDeveloperContext:
		return pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_DEVELOPER_CONTEXT
	case hostadvisor.InjectionModeAdditionalContext:
		return pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_ADDITIONAL_CONTEXT
	default:
		return pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_UNSPECIFIED
	}
}

func callbackOrderingProto(ordering hostadvisor.CallbackOrdering) pb.HostAdvisorCallbackOrdering {
	switch ordering {
	case hostadvisor.CallbackOrderingBeforeFirstAction:
		return pb.HostAdvisorCallbackOrdering_HOST_ADVISOR_CALLBACK_ORDERING_BEFORE_FIRST_ACTION
	case hostadvisor.CallbackOrderingAfterToolResult:
		return pb.HostAdvisorCallbackOrdering_HOST_ADVISOR_CALLBACK_ORDERING_AFTER_TOOL_RESULT
	case hostadvisor.CallbackOrderingOtherProvenOrder:
		return pb.HostAdvisorCallbackOrdering_HOST_ADVISOR_CALLBACK_ORDERING_OTHER_PROVEN_ORDER
	default:
		return pb.HostAdvisorCallbackOrdering_HOST_ADVISOR_CALLBACK_ORDERING_UNSPECIFIED
	}
}

func acknowledgementProto(acknowledgement hostadvisor.Acknowledgement) pb.HostAdvisorAcknowledgement {
	switch acknowledgement {
	case hostadvisor.AcknowledgementNone:
		return pb.HostAdvisorAcknowledgement_HOST_ADVISOR_ACKNOWLEDGEMENT_NONE
	case hostadvisor.AcknowledgementAdapterAttested:
		return pb.HostAdvisorAcknowledgement_HOST_ADVISOR_ACKNOWLEDGEMENT_ADAPTER_ATTESTED
	case hostadvisor.AcknowledgementHostAcknowledged:
		return pb.HostAdvisorAcknowledgement_HOST_ADVISOR_ACKNOWLEDGEMENT_HOST_ACKNOWLEDGED
	case hostadvisor.AcknowledgementConsumptionObserved:
		return pb.HostAdvisorAcknowledgement_HOST_ADVISOR_ACKNOWLEDGEMENT_CONSUMPTION_OBSERVED
	default:
		return pb.HostAdvisorAcknowledgement_HOST_ADVISOR_ACKNOWLEDGEMENT_UNSPECIFIED
	}
}
