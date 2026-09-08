package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// uci1InstalledCompletionCallback is the package-local external gRPC input for
// the installed UCI-1 scenario probe. The caller supplies the HAP project
// service keycard separately; this helper never logs or returns that secret.
type uci1InstalledCompletionCallback struct {
	CanonicalProject string
	ExposureRef      string
	SupportedHostRef string
	CallbackRef      string
	Outcome          pb.UCICompletionOutcome
	IdempotencyKey   string
}

// uci1RecordInstalledCompletion reaches the installed server over its real
// gRPC listener. It deliberately does not call an in-process recorder.
func uci1RecordInstalledCompletion(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, bearerToken string, callback uci1InstalledCompletionCallback) (bool, error) {
	if ctx == nil {
		return false, errors.New("installed completion context is nil")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if bearerToken == "" || strings.TrimSpace(bearerToken) != bearerToken {
		return false, errors.New("installed completion keycard is unavailable")
	}
	address, err := uci1PublicationFaultServerAddress(runtime.ServerEnvironment)
	if err != nil {
		return false, fmt.Errorf("resolve installed completion server: %w", err)
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return false, fmt.Errorf("connect installed completion transport: %w", err)
	}
	defer connection.Close()

	callCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs("authorization", "Bearer "+bearerToken))
	response, err := pb.NewEngramServiceClient(connection).RecordUCICompletion(callCtx, &pb.RecordUCICompletionRequest{
		CanonicalProject: callback.CanonicalProject,
		ExposureRef:      callback.ExposureRef,
		SupportedHostRef: callback.SupportedHostRef,
		CallbackRef:      callback.CallbackRef,
		Outcome:          callback.Outcome,
		IdempotencyKey:   callback.IdempotencyKey,
	})
	if err != nil {
		return false, fmt.Errorf("record installed completion: %w", err)
	}
	if response == nil {
		return false, errors.New("installed completion response is empty")
	}
	return response.GetAccepted(), nil
}
