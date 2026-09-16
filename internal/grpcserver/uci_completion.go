package grpcserver

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	maxUCICompletionRequestBytes        = 8 << 10
	maxUCICompletionReferenceBytes      = 256
	maxUCICompletionIdempotencyKeyBytes = 4 << 10
)

// UCICompletionRecorder accepts a pre-verified supported-host callback.
// It is separate from MCPHandler so generic gRPC adapters never gain this
// completion capability accidentally.
type UCICompletionRecorder interface {
	RecordUCICompletion(context.Context, uci.VerifiedSupportedHostCallback) error
}

type uciCompletionRequestValues struct {
	canonicalProject projectidentity.ProjectKeyV3
	exposureRef      string
	supportedHostRef string
	callbackRef      string
	outcome          uci.CompletionOutcome
	idempotencyKey   string
}

// RecordUCICompletion accepts an already-supported-host callback through a
// dedicated, project-service-only transport. It never returns parent exposure
// or completion evidence details.
func (s *Server) RecordUCICompletion(ctx context.Context, request *pb.RecordUCICompletionRequest) (*pb.RecordUCICompletionResponse, error) {
	if ctx == nil {
		return nil, uciCompletionInvalidArgument()
	}
	if err := ctx.Err(); err != nil {
		return nil, status.FromContextError(err).Err()
	}
	if err := requireProjectServiceClass(ctx); err != nil {
		return nil, err
	}

	values, err := uciCompletionRequestValuesFromProto(request)
	if err != nil {
		return nil, uciCompletionInvalidArgument()
	}
	if err := requireProjectServiceMatch(ctx, string(values.canonicalProject)); err != nil {
		return nil, err
	}

	if s == nil {
		return nil, uciCompletionUnavailable()
	}
	recorder := s.currentUCICompletionRecorder()
	if recorder == nil {
		return nil, uciCompletionUnavailable()
	}
	callback := uci.VerifiedSupportedHostCallback{
		ExposureRef:      values.exposureRef,
		SupportedHostRef: values.supportedHostRef,
		CallbackRef:      values.callbackRef,
		Outcome:          values.outcome,
		IdempotencyKey:   values.idempotencyKey,
		OccurredAt:       time.Now().UTC(),
	}
	if err := recorder.RecordUCICompletion(ctx, callback); err != nil {
		return nil, uciCompletionRecorderError(ctx, err)
	}
	return &pb.RecordUCICompletionResponse{Accepted: true}, nil
}

func uciCompletionRequestValuesFromProto(request *pb.RecordUCICompletionRequest) (uciCompletionRequestValues, error) {
	if request == nil || len(request.ProtoReflect().GetUnknown()) != 0 || proto.Size(request) > maxUCICompletionRequestBytes {
		return uciCompletionRequestValues{}, errors.New("invalid completion request")
	}
	canonicalProject, err := projectidentity.NewProjectKeyV3(request.GetCanonicalProject())
	if err != nil || !uci.ValidExposureRef(request.GetExposureRef()) ||
		!validUCICompletionReference(request.GetSupportedHostRef()) ||
		!validUCICompletionReference(request.GetCallbackRef()) ||
		!validUCICompletionIdempotencyKey(request.GetIdempotencyKey()) {
		return uciCompletionRequestValues{}, errors.New("invalid completion request")
	}
	outcome, ok := uciCompletionOutcomeFromProto(request.GetOutcome())
	if !ok {
		return uciCompletionRequestValues{}, errors.New("invalid completion request")
	}
	return uciCompletionRequestValues{
		canonicalProject: canonicalProject,
		exposureRef:      request.GetExposureRef(),
		supportedHostRef: request.GetSupportedHostRef(),
		callbackRef:      request.GetCallbackRef(),
		outcome:          outcome,
		idempotencyKey:   request.GetIdempotencyKey(),
	}, nil
}

func uciCompletionOutcomeFromProto(value pb.UCICompletionOutcome) (uci.CompletionOutcome, bool) {
	switch value {
	case pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_SUCCEEDED:
		return uci.CompletionSucceeded, true
	case pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_PARTIAL:
		return uci.CompletionPartial, true
	case pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_FAILED:
		return uci.CompletionFailed, true
	case pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_ABANDONED:
		return uci.CompletionAbandoned, true
	default:
		return "", false
	}
}

func validUCICompletionReference(value string) bool {
	return validUCICompletionText(value, maxUCICompletionReferenceBytes)
}

func validUCICompletionIdempotencyKey(value string) bool {
	return validUCICompletionText(value, maxUCICompletionIdempotencyKeyBytes)
}

func validUCICompletionText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func uciCompletionRecorderError(ctx context.Context, err error) error {
	if errors.Is(err, uci.ErrIdempotencyMismatch) {
		return status.Error(codes.FailedPrecondition, uci.ErrIdempotencyMismatch.Error())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return status.FromContextError(err).Err()
	}
	if ctx != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return status.FromContextError(contextErr).Err()
		}
	}
	return uciCompletionUnavailable()
}

func uciCompletionInvalidArgument() error {
	return status.Error(codes.InvalidArgument, "completion request is invalid")
}

func uciCompletionUnavailable() error {
	return status.Error(codes.Unavailable, uci.ErrCompletionEvidenceUnavailable.Error())
}
