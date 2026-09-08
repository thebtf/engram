package grpcserver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const uciCompletionTransportProject = "11111111-1111-4111-8111-111111111111"

type uciCompletionTransportHandler struct {
	callbacks []uci.VerifiedSupportedHostCallback
	err       error
}

func (*uciCompletionTransportHandler) HandleToolCall(context.Context, string, []byte) ([]byte, bool, error) {
	return nil, false, nil
}

func (*uciCompletionTransportHandler) ToolDefinitions() []ToolDef { return nil }

func (*uciCompletionTransportHandler) ServerInfo() (string, string) { return "engram", "test" }

func (handler *uciCompletionTransportHandler) RecordUCICompletion(_ context.Context, callback uci.VerifiedSupportedHostCallback) error {
	handler.callbacks = append(handler.callbacks, callback)
	return handler.err
}

func TestRecordUCICompletionAcceptsMatchedProjectServiceCallback(t *testing.T) {
	handler := &uciCompletionTransportHandler{}
	server := &Server{handler: handler}
	request := uciCompletionTransportRequest()

	response, err := server.RecordUCICompletion(uciCompletionTransportContext(uciCompletionTransportProject), request)
	if err != nil {
		t.Fatalf("RecordUCICompletion() error = %v", err)
	}
	if response == nil || !response.GetAccepted() {
		t.Fatalf("RecordUCICompletion() response = %#v", response)
	}
	if len(handler.callbacks) != 1 {
		t.Fatalf("completion callbacks = %d, want 1", len(handler.callbacks))
	}
	callback := handler.callbacks[0]
	if callback.ExposureRef != request.GetExposureRef() ||
		callback.SupportedHostRef != request.GetSupportedHostRef() ||
		callback.CallbackRef != request.GetCallbackRef() ||
		callback.Outcome != uci.CompletionPartial ||
		callback.IdempotencyKey != request.GetIdempotencyKey() ||
		callback.OccurredAt.IsZero() {
		t.Fatalf("verified callback = %#v", callback)
	}
}

func TestRecordUCICompletionRejectsEveryIneligibleIdentity(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Hour)
	for name, identity := range map[string]auth.Identity{
		"auth disabled": auth.AuthDisabled(),
		"master":        auth.Admin(),
		"source client": auth.Client("read-write", "ordinary-keycard"),
		"registration":  auth.ClientWithPrincipal("read-write", "registration-keycard", auth.RegistrationServicePrincipal("workstation-a"), auth.PrincipalKindService),
		"legacy direct": auth.ClientWithPrincipalExpiry("read-write", "legacy-keycard", auth.LegacyDirectPrincipal(uciCompletionTransportProject), auth.PrincipalKindAgent, &expiresAt),
		"wrong project": auth.ClientWithPrincipal("read-write", "wrong-project-keycard", auth.ProjectServicePrincipal("22222222-2222-4222-8222-222222222222"), auth.PrincipalKindService),
	} {
		t.Run(name, func(t *testing.T) {
			handler := &uciCompletionTransportHandler{}
			server := &Server{handler: handler}
			_, err := server.RecordUCICompletion(auth.WithIdentity(context.Background(), identity), uciCompletionTransportRequest())
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("RecordUCICompletion() status = %v, error = %v", status.Code(err), err)
			}
			if len(handler.callbacks) != 0 {
				t.Fatalf("ineligible identity reached completion recorder: %#v", handler.callbacks)
			}
		})
	}

	server := &Server{handler: &uciCompletionTransportHandler{}}
	_, err := server.RecordUCICompletion(context.Background(), uciCompletionTransportRequest())
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("missing identity status = %v, error = %v", status.Code(err), err)
	}
}

func TestRecordUCICompletionRejectsMalformedRequestsBeforeRecorder(t *testing.T) {
	for name, request := range map[string]*pb.RecordUCICompletionRequest{
		"nil":                       nil,
		"invalid canonical project": {CanonicalProject: "not-a-project"},
		"control callback ref": func() *pb.RecordUCICompletionRequest {
			request := uciCompletionTransportRequest()
			request.CallbackRef = "callback\nref"
			return request
		}(),
		"padded host ref": func() *pb.RecordUCICompletionRequest {
			request := uciCompletionTransportRequest()
			request.SupportedHostRef = " host-ref"
			return request
		}(),
		"oversize idempotency key": func() *pb.RecordUCICompletionRequest {
			request := uciCompletionTransportRequest()
			request.IdempotencyKey = strings.Repeat("x", maxUCICompletionIdempotencyKeyBytes+1)
			return request
		}(),
		"unspecified outcome": func() *pb.RecordUCICompletionRequest {
			request := uciCompletionTransportRequest()
			request.Outcome = pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_UNSPECIFIED
			return request
		}(),
		"unknown outcome": func() *pb.RecordUCICompletionRequest {
			request := uciCompletionTransportRequest()
			request.Outcome = pb.UCICompletionOutcome(99)
			return request
		}(),
		"unknown field": func() *pb.RecordUCICompletionRequest {
			request := uciCompletionTransportRequest()
			request.ProtoReflect().SetUnknown([]byte{0x38, 0x01})
			return request
		}(),
	} {
		t.Run(name, func(t *testing.T) {
			handler := &uciCompletionTransportHandler{}
			server := &Server{handler: handler}
			_, err := server.RecordUCICompletion(uciCompletionTransportContext(uciCompletionTransportProject), request)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("RecordUCICompletion() status = %v, error = %v", status.Code(err), err)
			}
			if len(handler.callbacks) != 0 {
				t.Fatalf("malformed request reached completion recorder: %#v", handler.callbacks)
			}
		})
	}
}

func TestRecordUCICompletionKeepsRecorderErrorsClosed(t *testing.T) {
	for name, recorderErr := range map[string]error{
		"idempotency mismatch": uci.ErrIdempotencyMismatch,
		"append failure":       errors.New("private exposure callback failed"),
	} {
		t.Run(name, func(t *testing.T) {
			handler := &uciCompletionTransportHandler{err: recorderErr}
			server := &Server{handler: handler}
			response, err := server.RecordUCICompletion(uciCompletionTransportContext(uciCompletionTransportProject), uciCompletionTransportRequest())
			if response != nil {
				t.Fatalf("RecordUCICompletion() response leaked evidence: %#v", response)
			}
			wantCode := codes.Unavailable
			wantMessage := uci.ErrCompletionEvidenceUnavailable.Error()
			if errors.Is(recorderErr, uci.ErrIdempotencyMismatch) {
				wantCode = codes.FailedPrecondition
				wantMessage = uci.ErrIdempotencyMismatch.Error()
			}
			if status.Code(err) != wantCode || status.Convert(err).Message() != wantMessage {
				t.Fatalf("RecordUCICompletion() error = %v, want %s/%q", err, wantCode, wantMessage)
			}
			if strings.Contains(status.Convert(err).Message(), "private exposure") {
				t.Fatalf("RecordUCICompletion() leaked recorder detail: %v", err)
			}
		})
	}
}

func TestRecordUCICompletionProtoContract(t *testing.T) {
	request := (&pb.RecordUCICompletionRequest{}).ProtoReflect().Descriptor()
	if request.Fields().Len() != 6 {
		t.Fatalf("completion request field count = %d, want 6", request.Fields().Len())
	}
	for name, number := range map[string]protoreflect.FieldNumber{
		"canonical_project":  1,
		"exposure_ref":       2,
		"supported_host_ref": 3,
		"callback_ref":       4,
		"outcome":            5,
		"idempotency_key":    6,
	} {
		field := request.Fields().ByName(protoreflect.Name(name))
		if field == nil || field.Number() != number {
			t.Fatalf("completion request field %q = %#v, want number %d", name, field, number)
		}
	}
	response := (&pb.RecordUCICompletionResponse{}).ProtoReflect().Descriptor()
	if response.Fields().Len() != 1 || response.Fields().ByName("accepted") == nil {
		t.Fatalf("completion response must expose only accepted: %#v", response)
	}
	service := request.ParentFile().Services().ByName("EngramService")
	method := service.Methods().ByName("RecordUCICompletion")
	if method == nil || method.IsStreamingClient() || method.IsStreamingServer() ||
		method.Input().FullName() != "engram.v1.RecordUCICompletionRequest" ||
		method.Output().FullName() != "engram.v1.RecordUCICompletionResponse" {
		t.Fatalf("completion RPC descriptor = %#v", method)
	}
}

func uciCompletionTransportContext(project string) context.Context {
	identity := auth.ClientWithPrincipal("read-write", "completion-project-keycard", auth.ProjectServicePrincipal(project), auth.PrincipalKindService)
	return auth.WithIdentity(context.Background(), identity)
}

func uciCompletionTransportRequest() *pb.RecordUCICompletionRequest {
	return &pb.RecordUCICompletionRequest{
		CanonicalProject: uciCompletionTransportProject,
		ExposureRef:      uci.NewExposureRef(),
		SupportedHostRef: "supported-host-ref",
		CallbackRef:      "callback-ref",
		Outcome:          pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_PARTIAL,
		IdempotencyKey:   "completion-idempotency-key",
	}
}
