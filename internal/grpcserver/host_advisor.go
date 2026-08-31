package grpcserver

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/hostadvisor"
	"github.com/thebtf/engram/internal/intervention"
	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskmemory"
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
// host-advisor registry.
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

// Advise validates one bound BEFORE_AGENT_START occurrence before delegating
// the immutable input to the injected intervention runtime.
func (s *Server) Advise(ctx context.Context, request *pb.HostAdvisorAdviseRequest) (*pb.HostAdvisorAdviseResponse, error) {
	if ctx == nil {
		return nil, hostAdvisorInvalidArgument()
	}
	enteredAt := time.Now().UTC()
	subject, err := bindAuthenticatedSubject(ctx)
	if err != nil {
		return nil, err
	}
	project, occurrence, err := hostAdvisorAdviseValuesFromProto(request)
	if err != nil {
		return nil, hostAdvisorInvalidArgument()
	}

	binding, active := s.activeHostAdvisorBinding(subject, request.GetBindingId())
	if !active {
		return s.hostAdvisorAdviseUnavailable(ctx, enteredAt, time.Time{}, intervention.UnavailableBinding)
	}
	bindingFacts, err := intervention.NewBindingFacts(binding)
	if err != nil || !bindingFacts.LiveAt(enteredAt) {
		return s.hostAdvisorAdviseUnavailable(ctx, enteredAt, binding.ExpiresAt(), intervention.UnavailableBinding)
	}
	if !bindingFacts.AllowsAdvise() {
		return s.hostAdvisorAdviseUnavailable(ctx, enteredAt, bindingFacts.ExpiresAt(), intervention.UnavailableCapability)
	}

	deadline, err := intervention.NewEffectiveDeadline(ctx, enteredAt, bindingFacts)
	if err != nil || !deadline.AllowsEntry(enteredAt) {
		return s.hostAdvisorAdviseUnavailable(ctx, enteredAt, bindingFacts.ExpiresAt(), intervention.UnavailableDeadline)
	}
	input, err := intervention.NewAdviseInput(bindingFacts, project, occurrence)
	if err != nil {
		return nil, hostAdvisorInvalidArgument()
	}
	advisor := s.currentInterventionAdvisor()
	if advisor == nil {
		return s.hostAdvisorAdviseUnavailable(ctx, enteredAt, deadline.Deadline(), intervention.UnavailableDependency)
	}
	if !deadline.AllowsWork(time.Now().UTC()) {
		return s.hostAdvisorAdviseUnavailable(ctx, enteredAt, deadline.Deadline(), intervention.UnavailableDeadline)
	}
	callbackContext, cancel := context.WithDeadline(ctx, deadline.Deadline())
	defer cancel()
	decision, err := advisor.Advise(callbackContext, input)
	if err != nil {
		code := intervention.UnavailableDependency
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(callbackContext.Err(), context.DeadlineExceeded) {
			code = intervention.UnavailableDeadline
		}
		return s.hostAdvisorAdviseUnavailable(ctx, enteredAt, deadline.Deadline(), code)
	}
	if !decision.Valid() {
		return s.hostAdvisorAdviseUnavailable(ctx, enteredAt, deadline.Deadline(), intervention.UnavailableDependency)
	}
	if !deadline.AllowsResponse(time.Now().UTC()) {
		if receipt, _, emitted := decision.Emit(); emitted {
			ambiguous, err := intervention.NewDeliveryAmbiguousDecision(receipt)
			if err == nil {
				return hostAdvisorDecisionProto(ambiguous)
			}
		}
		return s.hostAdvisorAdviseUnavailable(ctx, enteredAt, deadline.Deadline(), intervention.UnavailableDeadline)
	}
	response, err := hostAdvisorDecisionProto(decision)
	if err != nil {
		return s.hostAdvisorAdviseUnavailable(ctx, enteredAt, deadline.Deadline(), intervention.UnavailableDependency)
	}
	return response, nil
}

// Observe validates an adapter attestation or semantic gap on the independently
// admitted BEFORE_AGENT_START channel. It never resolves project evidence.
func (s *Server) Observe(ctx context.Context, request *pb.HostAdvisorObserveRequest) (*pb.HostAdvisorObserveResponse, error) {
	if ctx == nil {
		return nil, hostAdvisorInvalidArgument()
	}
	subject, err := bindAuthenticatedSubject(ctx)
	if err != nil {
		return nil, err
	}
	if hostAdvisorMessageMalformed(request) || proto.Size(request) > intervention.MaxWireBytes {
		return nil, hostAdvisorInvalidArgument()
	}
	binding, active := s.activeHostAdvisorBinding(subject, request.GetBindingId())
	if !active {
		return hostAdvisorObserveUnavailable()
	}
	bindingFacts, err := intervention.NewBindingFacts(binding)
	if err != nil || !bindingFacts.LiveAt(time.Now().UTC()) || !bindingFacts.AllowsObserve() {
		return hostAdvisorObserveUnavailable()
	}
	input, err := hostAdvisorObserveInputFromProto(request, bindingFacts)
	if err != nil {
		return nil, hostAdvisorInvalidArgument()
	}
	advisor := s.currentInterventionAdvisor()
	if advisor == nil {
		return hostAdvisorObserveUnavailable()
	}
	acknowledgement, err := advisor.Observe(ctx, input)
	if err != nil || !acknowledgement.Valid() {
		return hostAdvisorObserveUnavailable()
	}
	response, err := hostAdvisorObservationAckProto(acknowledgement)
	if err != nil {
		return hostAdvisorObserveUnavailable()
	}
	return response, nil
}

func (s *Server) activeHostAdvisorBinding(subject hostadvisor.AuthenticatedSubject, bindingID string) (hostadvisor.HostBinding, bool) {
	registry := s.currentHostAdvisorRegistry()
	if registry == nil {
		return hostadvisor.HostBinding{}, false
	}
	binding, err := registry.RequireActive(subject, hostadvisor.BindingID(bindingID))
	return binding, err == nil
}

func (s *Server) hostAdvisorAdviseUnavailable(ctx context.Context, enteredAt, expiry time.Time, code intervention.UnavailableCode) (*pb.HostAdvisorAdviseResponse, error) {
	if expiry.IsZero() {
		expiry = enteredAt.Add(intervention.HardDeadlineCap)
	}
	if contextDeadline, hasDeadline := ctx.Deadline(); hasDeadline && contextDeadline.Before(expiry) {
		expiry = contextDeadline
	}
	decision, err := intervention.NewUnavailableDecision(uuid.NewString(), expiry, code)
	if err != nil {
		return nil, status.Error(codes.Internal, "host advisor unavailable response failed")
	}
	return hostAdvisorDecisionProto(decision)
}

func hostAdvisorObserveUnavailable() (*pb.HostAdvisorObserveResponse, error) {
	acknowledgement, err := intervention.NewUnavailableObservationAck(intervention.ObservationReasonDependencyUnavailable)
	if err != nil {
		return nil, status.Error(codes.Internal, "host advisor unavailable acknowledgement failed")
	}
	return hostAdvisorObservationAckProto(acknowledgement)
}

func hostAdvisorAdviseValuesFromProto(request *pb.HostAdvisorAdviseRequest) (taskmemory.ProjectEvidenceV3, intervention.BeforeAgentStartOccurrence, error) {
	if hostAdvisorMessageMalformed(request) || proto.Size(request) > intervention.MaxWireBytes {
		return taskmemory.ProjectEvidenceV3{}, intervention.BeforeAgentStartOccurrence{}, intervention.ErrInvalidInput
	}
	project, err := hostAdvisorProjectEvidenceFromProto(request.GetProjectEvidence())
	if err != nil {
		return taskmemory.ProjectEvidenceV3{}, intervention.BeforeAgentStartOccurrence{}, err
	}
	occurrence, err := hostAdvisorOccurrenceFromProto(request.GetOccurrence())
	if err != nil {
		return taskmemory.ProjectEvidenceV3{}, intervention.BeforeAgentStartOccurrence{}, err
	}
	return project, occurrence, nil
}

func hostAdvisorProjectEvidenceFromProto(value *pb.ProjectIdentityV3) (taskmemory.ProjectEvidenceV3, error) {
	if hostAdvisorMessageMalformed(value) {
		return taskmemory.ProjectEvidenceV3{}, intervention.ErrInvalidInput
	}
	for _, identifier := range value.GetLegacyIdentifiers() {
		if hostAdvisorMessageMalformed(identifier) {
			return taskmemory.ProjectEvidenceV3{}, intervention.ErrInvalidInput
		}
	}
	anchor, descriptor := grpcProjectIdentityV3Evidence(value)
	validated, err := projectidentity.BuildDescriptorV3(anchor, descriptor.NormalizedGitRemotes, descriptor.LegacyIdentifiers, descriptor.ClientInstanceID)
	if err != nil {
		return taskmemory.ProjectEvidenceV3{}, intervention.ErrInvalidInput
	}
	return taskmemory.ProjectEvidenceV3{Anchor: anchor, Descriptor: validated}, nil
}

func hostAdvisorOccurrenceFromProto(value *pb.HostAdvisorOccurrence) (intervention.BeforeAgentStartOccurrence, error) {
	if hostAdvisorMessageMalformed(value) || value.GetPredecessor() != nil || value.GetPhase() != pb.HostAdvisorSemantic_HOST_ADVISOR_SEMANTIC_BEFORE_AGENT_START {
		return intervention.BeforeAgentStartOccurrence{}, intervention.ErrInvalidInput
	}
	beforeAgentStart := value.GetBeforeAgentStart()
	if hostAdvisorMessageMalformed(beforeAgentStart) {
		return intervention.BeforeAgentStartOccurrence{}, intervention.ErrInvalidInput
	}
	typedFacts := make([]intervention.TypedFact, len(beforeAgentStart.GetFacts()))
	for index, fact := range beforeAgentStart.GetFacts() {
		if hostAdvisorMessageMalformed(fact) {
			return intervention.BeforeAgentStartOccurrence{}, intervention.ErrInvalidInput
		}
		kind, ok := hostAdvisorFactKindFromProto(fact.GetKind())
		if !ok {
			return intervention.BeforeAgentStartOccurrence{}, intervention.ErrInvalidInput
		}
		parsed, err := intervention.NewTypedFact(kind, fact.GetValue())
		if err != nil {
			return intervention.BeforeAgentStartOccurrence{}, intervention.ErrInvalidInput
		}
		typedFacts[index] = parsed
	}
	facts, err := intervention.NewBeforeAgentStartFacts(beforeAgentStart.GetTaskQuery(), typedFacts)
	if err != nil {
		return intervention.BeforeAgentStartOccurrence{}, intervention.ErrInvalidInput
	}
	return intervention.NewBeforeAgentStartOccurrence(value.GetSessionRef(), value.GetPhaseAnchorRef(), facts)
}

func hostAdvisorFactKindFromProto(kind pb.HostAdvisorFactKind) (intervention.FactKind, bool) {
	switch kind {
	case pb.HostAdvisorFactKind_HOST_ADVISOR_FACT_KIND_KEYWORD:
		return intervention.FactKindKeyword, true
	case pb.HostAdvisorFactKind_HOST_ADVISOR_FACT_KIND_PATH:
		return intervention.FactKindPath, true
	case pb.HostAdvisorFactKind_HOST_ADVISOR_FACT_KIND_TOOL:
		return intervention.FactKindTool, true
	default:
		return 0, false
	}
}

func hostAdvisorObserveInputFromProto(request *pb.HostAdvisorObserveRequest, binding intervention.BindingFacts) (intervention.ObserveInput, error) {
	if hostAdvisorMessageMalformed(request) || proto.Size(request) > intervention.MaxWireBytes {
		return intervention.ObserveInput{}, intervention.ErrInvalidInput
	}
	switch target := request.GetTarget().(type) {
	case *pb.HostAdvisorObserveRequest_ReceiptBound:
		if target.ReceiptBound == nil || hostAdvisorMessageMalformed(target.ReceiptBound) {
			return intervention.ObserveInput{}, intervention.ErrInvalidInput
		}
		receipt, err := hostAdvisorReceiptIdentityFromProto(target.ReceiptBound.GetDecisionReceipt())
		if err != nil {
			return intervention.ObserveInput{}, err
		}
		switch evidence := target.ReceiptBound.GetEvidence().(type) {
		case *pb.HostAdvisorReceiptBoundObservation_AdapterAttested:
			if evidence.AdapterAttested == nil || hostAdvisorMessageMalformed(evidence.AdapterAttested) {
				return intervention.ObserveInput{}, intervention.ErrInvalidInput
			}
			attestation, ok := hostAdvisorAttestationKindFromProto(evidence.AdapterAttested.GetKind())
			if !ok {
				return intervention.ObserveInput{}, intervention.ErrInvalidInput
			}
			return intervention.NewObserveReceiptAttestation(binding, receipt, target.ReceiptBound.GetObservationAnchorRef(), attestation)
		case *pb.HostAdvisorReceiptBoundObservation_AdapterSemanticGap:
			if evidence.AdapterSemanticGap == nil || hostAdvisorMessageMalformed(evidence.AdapterSemanticGap) {
				return intervention.ObserveInput{}, intervention.ErrInvalidInput
			}
			gap, ok := hostAdvisorSemanticGapCodeFromProto(evidence.AdapterSemanticGap.GetCode())
			if !ok {
				return intervention.ObserveInput{}, intervention.ErrInvalidInput
			}
			return intervention.NewObserveReceiptSemanticGap(binding, receipt, target.ReceiptBound.GetObservationAnchorRef(), gap)
		default:
			return intervention.ObserveInput{}, intervention.ErrInvalidInput
		}
	case *pb.HostAdvisorObserveRequest_ChannelGap:
		if target.ChannelGap == nil || hostAdvisorMessageMalformed(target.ChannelGap) {
			return intervention.ObserveInput{}, intervention.ErrInvalidInput
		}
		gapWire := target.ChannelGap.GetAdapterSemanticGap()
		if hostAdvisorMessageMalformed(gapWire) {
			return intervention.ObserveInput{}, intervention.ErrInvalidInput
		}
		gap, ok := hostAdvisorSemanticGapCodeFromProto(gapWire.GetCode())
		if !ok {
			return intervention.ObserveInput{}, intervention.ErrInvalidInput
		}
		return intervention.NewObserveChannelSemanticGap(binding, target.ChannelGap.GetObservationAnchorRef(), gap)
	default:
		return intervention.ObserveInput{}, intervention.ErrInvalidInput
	}
}

func hostAdvisorReceiptIdentityFromProto(value *pb.HostAdvisorReceiptIdentity) (intervention.ReceiptIdentity, error) {
	if hostAdvisorMessageMalformed(value) || len(value.GetIntegritySha256()) != sha256.Size {
		return intervention.ReceiptIdentity{}, intervention.ErrInvalidInput
	}
	var integrity [sha256.Size]byte
	copy(integrity[:], value.GetIntegritySha256())
	return intervention.NewReceiptIdentity(value.GetReceiptId(), integrity)
}

func hostAdvisorAttestationKindFromProto(kind pb.HostAdvisorAttestationKind) (intervention.AttestationKind, bool) {
	switch kind {
	case pb.HostAdvisorAttestationKind_HOST_ADVISOR_ATTESTATION_KIND_DECISION_RECEIVED:
		return intervention.AttestationDecisionReceived, true
	case pb.HostAdvisorAttestationKind_HOST_ADVISOR_ATTESTATION_KIND_UNTRUSTED_REFERENCE_PRESENTED:
		return intervention.AttestationUntrustedReferencePresented, true
	default:
		return 0, false
	}
}

func hostAdvisorSemanticGapCodeFromProto(code pb.HostAdvisorSemanticGapCode) (intervention.SemanticGapCode, bool) {
	switch code {
	case pb.HostAdvisorSemanticGapCode_HOST_ADVISOR_SEMANTIC_GAP_CODE_CALLBACK_UNAVAILABLE:
		return intervention.SemanticGapCallbackUnavailable, true
	case pb.HostAdvisorSemanticGapCode_HOST_ADVISOR_SEMANTIC_GAP_CODE_CONTEXT_INJECTION_UNAVAILABLE:
		return intervention.SemanticGapContextInjectionUnavailable, true
	case pb.HostAdvisorSemanticGapCode_HOST_ADVISOR_SEMANTIC_GAP_CODE_RECEIPT_CORRELATION_UNAVAILABLE:
		return intervention.SemanticGapReceiptCorrelationUnavailable, true
	default:
		return 0, false
	}
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

func hostAdvisorDecisionProto(decision intervention.Decision) (*pb.HostAdvisorAdviseResponse, error) {
	if !decision.Valid() {
		return nil, intervention.ErrInvalidInput
	}
	switch decision.Kind() {
	case intervention.DecisionEmit:
		receipt, packet, ok := decision.Emit()
		if !ok {
			return nil, intervention.ErrInvalidInput
		}
		packetProto, err := hostAdvisorPacketProto(packet)
		if err != nil {
			return nil, err
		}
		return &pb.HostAdvisorAdviseResponse{Decision: &pb.HostAdvisorAdviseResponse_Emit{Emit: &pb.HostAdvisorEmit{
			Receipt: hostAdvisorReceiptIdentityProto(receipt),
			Packet:  packetProto,
		}}}, nil
	case intervention.DecisionAbstain:
		receipt, reason, ok := decision.Abstain()
		if !ok {
			return nil, intervention.ErrInvalidInput
		}
		reasonProto, ok := hostAdvisorAbstentionReasonProto(reason)
		if !ok {
			return nil, intervention.ErrInvalidInput
		}
		return &pb.HostAdvisorAdviseResponse{Decision: &pb.HostAdvisorAdviseResponse_Abstain{Abstain: &pb.HostAdvisorAbstain{
			Receipt: hostAdvisorReceiptIdentityProto(receipt),
			Reason:  reasonProto,
		}}}, nil
	case intervention.DecisionDeliveryAmbiguous:
		receipt, ok := decision.DeliveryAmbiguous()
		if !ok {
			return nil, intervention.ErrInvalidInput
		}
		return &pb.HostAdvisorAdviseResponse{Decision: &pb.HostAdvisorAdviseResponse_DeliveryAmbiguous{DeliveryAmbiguous: &pb.HostAdvisorDeliveryAmbiguous{
			Receipt: hostAdvisorReceiptIdentityProto(receipt),
		}}}, nil
	case intervention.DecisionUnavailable:
		unavailable, ok := decision.Unavailable()
		if !ok {
			return nil, intervention.ErrInvalidInput
		}
		code, ok := hostAdvisorUnavailableCodeProto(unavailable.Code())
		if !ok {
			return nil, intervention.ErrInvalidInput
		}
		return &pb.HostAdvisorAdviseResponse{Decision: &pb.HostAdvisorAdviseResponse_Unavailable{Unavailable: &pb.HostAdvisorUnavailable{
			CorrelationId: unavailable.CorrelationID(),
			ExpiresAt:     timestamppb.New(unavailable.ExpiresAt()),
			Code:          code,
		}}}, nil
	default:
		return nil, intervention.ErrInvalidInput
	}
}

func hostAdvisorPacketProto(packet intervention.Packet) (*pb.HostAdvisorPacket, error) {
	knowledge := packet.Knowledge()
	tier, ok := hostAdvisorCandidateTierProto(knowledge.Tier())
	if !ok {
		return nil, intervention.ErrInvalidInput
	}
	presentation := packet.Presentation()
	injectionMode, ok := hostAdvisorPresentationModeProto(presentation.InjectionMode())
	if !ok {
		return nil, intervention.ErrInvalidInput
	}
	return &pb.HostAdvisorPacket{
		Receipt:   hostAdvisorReceiptIdentityProto(packet.Receipt()),
		ExpiresAt: timestamppb.New(packet.ExpiresAt()),
		Knowledge: &pb.HostAdvisorKnowledgeReference{
			MemoryId:      knowledge.MemoryID(),
			MemoryVersion: knowledge.MemoryVersion(),
			SourceTier:    tier,
			TextSha256:    knowledge.TextDigest().Bytes(),
		},
		Presentation: &pb.HostAdvisorPresentation{
			InjectionMode: injectionMode,
			BoundedText:   presentation.Text(),
		},
	}, nil
}

func hostAdvisorReceiptIdentityProto(receipt intervention.ReceiptIdentity) *pb.HostAdvisorReceiptIdentity {
	return &pb.HostAdvisorReceiptIdentity{
		ReceiptId:       receipt.ID(),
		IntegritySha256: receipt.IntegrityDigest().Bytes(),
	}
}

func hostAdvisorAbstentionReasonProto(reason intervention.AbstentionReason) (pb.HostAdvisorAbstentionReason, bool) {
	switch reason {
	case intervention.AbstentionNoCandidates:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_NO_CANDIDATES, true
	case intervention.AbstentionPolicyObserving:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_POLICY_OBSERVING, true
	case intervention.AbstentionPolicyShadow:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_POLICY_SHADOW, true
	case intervention.AbstentionPolicyCanaryBudget:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_POLICY_CANARY_BUDGET, true
	case intervention.AbstentionEvidenceInsufficient:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_EVIDENCE_INSUFFICIENT, true
	case intervention.AbstentionHarmBound:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_HARM_BOUND, true
	case intervention.AbstentionPolicySuppressed:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_POLICY_SUPPRESSED, true
	case intervention.AbstentionTaskFit:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_TASK_FIT, true
	case intervention.AbstentionActionability:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_ACTIONABILITY, true
	case intervention.AbstentionEvidenceState:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_EVIDENCE_STATE, true
	case intervention.AbstentionAlreadyVisible:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_ALREADY_VISIBLE, true
	case intervention.AbstentionContextBudget:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_CONTEXT_BUDGET, true
	case intervention.AbstentionAmbiguousConflict:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_AMBIGUOUS_CONFLICT, true
	default:
		return pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_UNSPECIFIED, false
	}
}

func hostAdvisorUnavailableCodeProto(code intervention.UnavailableCode) (pb.HostAdvisorUnavailableCode, bool) {
	switch code {
	case intervention.UnavailableBinding:
		return pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_BINDING_UNAVAILABLE, true
	case intervention.UnavailableCapability:
		return pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_CAPABILITY_UNAVAILABLE, true
	case intervention.UnavailableFacts:
		return pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_FACTS_INVALID, true
	case intervention.UnavailablePredecessor:
		return pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_PREDECESSOR_INVALID, true
	case intervention.UnavailableDeadline:
		return pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_DEADLINE_EXPIRED, true
	case intervention.UnavailableReplayConflict:
		return pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_REPLAY_CONFLICT, true
	case intervention.UnavailableDependency:
		return pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_DEPENDENCY_UNAVAILABLE, true
	case intervention.UnavailableReceipt:
		return pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_RECEIPT_UNAVAILABLE, true
	default:
		return pb.HostAdvisorUnavailableCode_HOST_ADVISOR_UNAVAILABLE_CODE_UNSPECIFIED, false
	}
}

func hostAdvisorCandidateTierProto(tier intervention.CandidateTier) (pb.HostAdvisorCandidateTier, bool) {
	switch tier {
	case intervention.CandidateTierExact:
		return pb.HostAdvisorCandidateTier_HOST_ADVISOR_CANDIDATE_TIER_EXACT, true
	case intervention.CandidateTierFTS:
		return pb.HostAdvisorCandidateTier_HOST_ADVISOR_CANDIDATE_TIER_FTS, true
	case intervention.CandidateTierVector:
		return pb.HostAdvisorCandidateTier_HOST_ADVISOR_CANDIDATE_TIER_VECTOR, true
	default:
		return pb.HostAdvisorCandidateTier_HOST_ADVISOR_CANDIDATE_TIER_UNSPECIFIED, false
	}
}

func hostAdvisorPresentationModeProto(mode intervention.ContextInjectionMode) (pb.HostAdvisorContextInjectionMode, bool) {
	switch mode {
	case intervention.ContextInjectionHiddenUntrustedMessage:
		return pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_HIDDEN_UNTRUSTED_MESSAGE, true
	default:
		return pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_UNSPECIFIED, false
	}
}

func hostAdvisorObservationAckProto(acknowledgement intervention.ObservationAck) (*pb.HostAdvisorObserveResponse, error) {
	if !acknowledgement.Valid() {
		return nil, intervention.ErrInvalidInput
	}
	state, ok := hostAdvisorObservationStateProto(acknowledgement.State())
	if !ok {
		return nil, intervention.ErrInvalidInput
	}
	reason, ok := hostAdvisorObservationReasonProto(acknowledgement.Reason())
	if !ok {
		return nil, intervention.ErrInvalidInput
	}
	observationID, hasObservationID := acknowledgement.ObservationID()
	if (acknowledgement.State() == intervention.ObservationAccepted || acknowledgement.State() == intervention.ObservationDuplicate) != hasObservationID {
		return nil, intervention.ErrInvalidInput
	}
	return &pb.HostAdvisorObserveResponse{
		State:         state,
		ObservationId: observationID,
		Reason:        reason,
	}, nil
}

func hostAdvisorObservationStateProto(state intervention.ObservationState) (pb.HostAdvisorObservationState, bool) {
	switch state {
	case intervention.ObservationAccepted:
		return pb.HostAdvisorObservationState_HOST_ADVISOR_OBSERVATION_STATE_ACCEPTED, true
	case intervention.ObservationDuplicate:
		return pb.HostAdvisorObservationState_HOST_ADVISOR_OBSERVATION_STATE_DUPLICATE, true
	case intervention.ObservationRejected:
		return pb.HostAdvisorObservationState_HOST_ADVISOR_OBSERVATION_STATE_REJECTED, true
	case intervention.ObservationUnavailable:
		return pb.HostAdvisorObservationState_HOST_ADVISOR_OBSERVATION_STATE_UNAVAILABLE, true
	default:
		return pb.HostAdvisorObservationState_HOST_ADVISOR_OBSERVATION_STATE_UNSPECIFIED, false
	}
}

func hostAdvisorObservationReasonProto(reason intervention.ObservationReason) (pb.HostAdvisorObservationReason, bool) {
	switch reason {
	case intervention.ObservationReasonAcceptedAttestation:
		return pb.HostAdvisorObservationReason_HOST_ADVISOR_OBSERVATION_REASON_ACCEPTED_ATTESTATION, true
	case intervention.ObservationReasonAcceptedSemanticGap:
		return pb.HostAdvisorObservationReason_HOST_ADVISOR_OBSERVATION_REASON_ACCEPTED_SEMANTIC_GAP, true
	case intervention.ObservationReasonDuplicate:
		return pb.HostAdvisorObservationReason_HOST_ADVISOR_OBSERVATION_REASON_DUPLICATE, true
	case intervention.ObservationReasonInvalidTarget:
		return pb.HostAdvisorObservationReason_HOST_ADVISOR_OBSERVATION_REASON_INVALID_TARGET, true
	case intervention.ObservationReasonReceiptUnavailable:
		return pb.HostAdvisorObservationReason_HOST_ADVISOR_OBSERVATION_REASON_RECEIPT_UNAVAILABLE, true
	case intervention.ObservationReasonDependencyUnavailable:
		return pb.HostAdvisorObservationReason_HOST_ADVISOR_OBSERVATION_REASON_DEPENDENCY_UNAVAILABLE, true
	default:
		return pb.HostAdvisorObservationReason_HOST_ADVISOR_OBSERVATION_REASON_UNSPECIFIED, false
	}
}
