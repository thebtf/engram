package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/thebtf/engram/internal/auditcontext"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func uci1ProbePublicationFaults(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (map[string]uciInstalledAcceptanceScenarioEvidence, error) {
	if ctx == nil {
		return nil, errors.New("UCI-1 publication faults require a context")
	}

	exchange, err := uci1OpenPublicationFaultExchange(ctx, runtime)
	if err != nil {
		return nil, err
	}
	defer exchange.close()

	partialDigest, err := uci1ObservePublicationPartialEOF(ctx, exchange)
	if err != nil {
		return nil, err
	}
	staleLeaseDigest, err := uci1ObservePublicationStaleLease(ctx, exchange)
	if err != nil {
		return nil, err
	}

	return map[string]uciInstalledAcceptanceScenarioEvidence{
		"U04": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: partialDigest},
		"U06": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: staleLeaseDigest},
	}, nil
}

type uci1PublicationFaultExchange struct {
	runtime       uciInstalledAcceptanceScenarioRuntime
	authority     *uciInstalledAcceptanceAuthority
	client        pb.EngramServiceClient
	connection    *grpc.ClientConn
	callCtx       context.Context
	sessionID     string
	contextHandle string
	scope         *pb.CodeIndexScope
	parent        *pb.ContextRef
	owner         string
}

type uci1PublicationFaultBaseline struct {
	checkoutID     string
	currentViewID  string
	viewCount      int64
	leaseEpoch     int64
	ownerInstance  sql.NullString
	leaseExpiresAt sql.NullTime
}

func uci1OpenPublicationFaultExchange(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (*uci1PublicationFaultExchange, error) {
	if runtime.Authority == nil || runtime.Authority.store == nil || runtime.Authority.rawToken == "" {
		return nil, errors.New("installed publication fault authority is incomplete")
	}
	publication, found := runtime.Publications[uciInstalledAcceptanceClientA]
	checkout := runtime.Authority.checkouts[uciInstalledAcceptanceClientA]
	if !found || checkout == nil || publication.sourceID == "" || publication.checkoutID == "" || publication.viewID == "" || publication.profileID == "" || checkout.IncarnationID == "" {
		return nil, errors.New("installed publication fault baseline is incomplete")
	}

	address, err := uci1PublicationFaultServerAddress(runtime.ServerEnvironment)
	if err != nil {
		return nil, err
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("connect installed publication fault transport: %w", err)
	}

	sessionID := "uci1-publication-" + strings.ReplaceAll(uuid.NewString(), "-", "")
	callCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"authorization", "Bearer "+runtime.Authority.rawToken,
		auditcontext.SourceSessionMetadataKey, sessionID,
	))
	client := pb.NewEngramServiceClient(connection)
	parent := &pb.ContextRef{
		SourceId:          publication.sourceID,
		CheckoutId:        publication.checkoutID,
		ViewId:            publication.viewID,
		Generation:        publication.generation,
		AnalysisProfileId: publication.profileID,
	}
	bound, err := client.BindCodeContext(callCtx, &pb.BindCodeContextRequest{
		ClientSessionId:  sessionID,
		RequestedContext: parent,
	})
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("bind installed publication fault context: %w", err)
	}
	if bound == nil || bound.GetContextHandle() == "" || bound.GetIndexScope() == nil ||
		bound.GetIndexScope().GetSourceId() != publication.sourceID ||
		bound.GetIndexScope().GetCheckoutId() != publication.checkoutID ||
		bound.GetIndexScope().GetIncarnationId() != checkout.IncarnationID ||
		bound.GetIndexScope().GetAnalysisProfileId() != publication.profileID {
		_ = connection.Close()
		return nil, errors.New("installed publication fault context binding is invalid")
	}

	return &uci1PublicationFaultExchange{
		runtime:       runtime,
		authority:     runtime.Authority,
		client:        client,
		connection:    connection,
		callCtx:       callCtx,
		sessionID:     sessionID,
		contextHandle: bound.GetContextHandle(),
		scope:         bound.GetIndexScope(),
		parent:        parent,
		owner:         "uci1-publication-owner-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
	}, nil
}

func (exchange *uci1PublicationFaultExchange) close() {
	if exchange != nil && exchange.connection != nil {
		_ = exchange.connection.Close()
		exchange.connection = nil
	}
}

func (exchange *uci1PublicationFaultExchange) reconnect(ctx context.Context) (*uci1PublicationFaultExchange, error) {
	if exchange == nil || exchange.authority == nil || exchange.sessionID == "" || exchange.contextHandle == "" {
		return nil, errors.New("installed publication fault exchange is incomplete")
	}
	exchange.close()
	address, err := uci1PublicationFaultServerAddress(exchange.runtime.ServerEnvironment)
	if err != nil {
		return nil, err
	}
	connection, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("reconnect installed publication fault transport: %w", err)
	}
	callCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs(
		"authorization", "Bearer "+exchange.authority.rawToken,
		auditcontext.SourceSessionMetadataKey, exchange.sessionID,
	))
	client := pb.NewEngramServiceClient(connection)
	bound, err := client.BindCodeContext(callCtx, &pb.BindCodeContextRequest{ClientSessionId: exchange.sessionID, ContextHandle: exchange.contextHandle})
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("reconnect installed publication fault binding: %w", err)
	}
	if bound == nil || bound.GetContextHandle() != exchange.contextHandle || bound.GetIndexScope() == nil {
		_ = connection.Close()
		return nil, errors.New("reconnected installed publication fault binding is invalid")
	}
	return &uci1PublicationFaultExchange{
		runtime: exchange.runtime, authority: exchange.authority, client: client, connection: connection,
		callCtx: callCtx, sessionID: exchange.sessionID, contextHandle: exchange.contextHandle,
		scope: bound.GetIndexScope(), parent: exchange.parent, owner: exchange.owner,
	}, nil
}

func uci1PublicationFaultServerAddress(environment []string) (string, error) {
	values := make(map[string]string, 2)
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "ENGRAM_WORKER_HOST" || key == "ENGRAM_WORKER_PORT" {
			values[key] = strings.TrimSpace(value)
		}
	}
	host, port := values["ENGRAM_WORKER_HOST"], values["ENGRAM_WORKER_PORT"]
	if host == "" || port == "" {
		return "", errors.New("installed publication fault server endpoint is unavailable")
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return "", errors.New("installed publication fault server port is invalid")
	}
	return net.JoinHostPort(host, strconv.Itoa(parsedPort)), nil
}

func uci1PublicationFaultStageMinimal(exchange *uci1PublicationFaultExchange, build *pb.BeginCodeIndexResponse) (*pb.StageCodeIndexResponse, error) {
	if exchange == nil || exchange.client == nil || exchange.scope == nil || build == nil || build.GetBuildId() == "" || build.GetLeaseEpoch() == 0 {
		return nil, errors.New("installed publication stage is incomplete")
	}
	payload, err := uci.EncodeIndexAdmissionFrame(uci.IndexAdmissionFrame{
		Version: uci.IndexAdmissionFrameVersion,
		Profile: uci.IndexAdmissionProfile{ID: exchange.scope.GetAnalysisProfileId()},
	})
	if err != nil {
		return nil, fmt.Errorf("encode installed publication frame: %w", err)
	}
	stream, err := exchange.client.StageCodeIndex(exchange.callCtx)
	if err != nil {
		return nil, fmt.Errorf("open installed publication stream: %w", err)
	}
	if err := stream.Send(&pb.StageCodeIndexFrame{
		Scope:         exchange.scope,
		BuildId:       build.GetBuildId(),
		LeaseEpoch:    build.GetLeaseEpoch(),
		Sequence:      0,
		PayloadDigest: string(uci.DigestIndexAdmissionPayload(payload)),
		Payload:       payload,
	}); err != nil {
		return nil, fmt.Errorf("send installed publication frame: %w", err)
	}
	staged, err := stream.CloseAndRecv()
	if err != nil {
		return nil, fmt.Errorf("close installed publication stream: %w", err)
	}
	if staged == nil || staged.GetBuildId() != build.GetBuildId() || staged.GetAcceptedPartCount() != 1 || staged.GetAcceptedSequence() != 0 || staged.GetPartDigest() == "" {
		return nil, errors.New("installed publication stream did not acknowledge exactly one frame")
	}
	return staged, nil
}

func uci1ObservePublicationPartialEOF(ctx context.Context, exchange *uci1PublicationFaultExchange) (string, error) {
	var partDigest string
	err := uci1PublicationFaultWithBuild(ctx, exchange, "partial-eof", func(build *pb.BeginCodeIndexResponse, baseline uci1PublicationFaultBaseline) error {
		payload, err := uci.EncodeIndexAdmissionFrame(uci.IndexAdmissionFrame{
			Version: uci.IndexAdmissionFrameVersion,
			Profile: uci.IndexAdmissionProfile{ID: exchange.scope.GetAnalysisProfileId()},
		})
		if err != nil {
			return fmt.Errorf("encode partial installed publication frame: %w", err)
		}
		stream, err := exchange.client.StageCodeIndex(exchange.callCtx)
		if err != nil {
			return fmt.Errorf("open installed partial publication stream: %w", err)
		}
		if err := stream.Send(&pb.StageCodeIndexFrame{
			Scope:         exchange.scope,
			BuildId:       build.GetBuildId(),
			LeaseEpoch:    build.GetLeaseEpoch(),
			Sequence:      0,
			PayloadDigest: string(uci.DigestIndexAdmissionPayload(payload)),
			Payload:       payload,
		}); err != nil {
			return fmt.Errorf("send installed partial publication frame: %w", err)
		}
		staged, err := stream.CloseAndRecv()
		if err != nil {
			return fmt.Errorf("close installed partial publication stream: %w", err)
		}
		if staged == nil || staged.GetBuildId() != build.GetBuildId() || staged.GetAcceptedPartCount() != 1 || staged.GetAcceptedSequence() != 0 || staged.GetPartDigest() == "" {
			return errors.New("installed partial publication stream did not acknowledge exactly one frame")
		}
		if err := uci1PublicationFaultAssertUnpublished(ctx, exchange.authority, build.GetBuildId(), baseline); err != nil {
			return err
		}
		partDigest = staged.GetPartDigest()
		return nil
	})
	if err != nil {
		return "", err
	}
	return uciInstalledAcceptanceStringDigest("U04\x00" + partDigest), nil
}

func uci1ObservePublicationFinalizeReplayAfterDiscardedAcknowledgement(ctx context.Context, exchange *uci1PublicationFaultExchange) (string, error) {
	var evidenceDigest string
	err := uci1PublicationFaultWithBuild(ctx, exchange, "discarded-ack-replay", func(build *pb.BeginCodeIndexResponse, baseline uci1PublicationFaultBaseline) error {
		staged, err := uci1PublicationFaultStageMinimal(exchange, build)
		if err != nil {
			return err
		}
		if err := uci1PublicationFaultAssertUnpublished(ctx, exchange.authority, build.GetBuildId(), baseline); err != nil {
			return err
		}
		request, err := uci1PublicationFaultStagedFinalize(exchange, build, staged)
		if err != nil {
			return err
		}
		wire := proto.MarshalOptions{Deterministic: true}
		requestBytes, err := wire.Marshal(request)
		if err != nil {
			return fmt.Errorf("marshal installed publication finalize replay request: %w", err)
		}
		firstRequest := &pb.FinalizeCodeIndexRequest{}
		if err := proto.Unmarshal(requestBytes, firstRequest); err != nil {
			return fmt.Errorf("restore installed publication finalize request: %w", err)
		}
		firstBytes, err := wire.Marshal(firstRequest)
		if err != nil {
			return fmt.Errorf("marshal restored installed publication finalize request: %w", err)
		}
		if !bytes.Equal(requestBytes, firstBytes) {
			return errors.New("installed publication finalize request did not retain exact bytes")
		}

		// Deliberately discard the successful acknowledgement, close this connection,
		// then replay the retained request through a new authenticated binding.
		if _, err := exchange.client.FinalizeCodeIndex(exchange.callCtx, firstRequest); err != nil {
			return fmt.Errorf("finalize installed publication before deliberately discarded acknowledgement: %w", err)
		}
		published, err := uci1PublicationFaultPublishedContext(ctx, exchange, build, baseline)
		if err != nil {
			return err
		}

		replayExchange, err := exchange.reconnect(ctx)
		if err != nil {
			return err
		}
		defer replayExchange.close()
		replayRequest := &pb.FinalizeCodeIndexRequest{}
		if err := proto.Unmarshal(requestBytes, replayRequest); err != nil {
			return fmt.Errorf("restore installed reconnected finalize request: %w", err)
		}
		replayBytes, err := wire.Marshal(replayRequest)
		if err != nil {
			return fmt.Errorf("marshal installed reconnected finalize request: %w", err)
		}
		if !bytes.Equal(requestBytes, replayBytes) {
			return errors.New("installed reconnected finalize request did not retain exact bytes")
		}
		replayed, err := replayExchange.client.FinalizeCodeIndex(replayExchange.callCtx, replayRequest)
		if err != nil {
			return fmt.Errorf("replay installed finalize after deliberately discarded acknowledgement: %w", err)
		}
		if replayed == nil || replayed.GetBuildId() != build.GetBuildId() || replayed.GetLeaseEpoch() != build.GetLeaseEpoch() || !proto.Equal(replayed.GetPublishedContext(), published) {
			return errors.New("installed reconnected finalize did not replay the durable publication")
		}
		replayedPublished, err := uci1PublicationFaultPublishedContext(ctx, exchange, build, baseline)
		if err != nil {
			return err
		}
		if !proto.Equal(replayedPublished, published) {
			return errors.New("installed reconnected finalize changed the published context")
		}
		evidenceDigest, err = uci1PublicationFaultReplayEvidenceDigest(requestBytes, published)
		return err
	})
	if err != nil {
		return "", err
	}
	return evidenceDigest, nil
}

func uci1ObservePublicationStaleLease(ctx context.Context, exchange *uci1PublicationFaultExchange) (string, error) {
	var leaseEpoch uint64
	err := uci1PublicationFaultWithBuild(ctx, exchange, "stale-lease", func(build *pb.BeginCodeIndexResponse, baseline uci1PublicationFaultBaseline) error {
		if err := uci1PublicationFaultExpireLease(ctx, exchange, build); err != nil {
			return err
		}
		request, err := uci1PublicationFaultEmptyFinalize(exchange, build)
		if err != nil {
			return err
		}
		_, err = exchange.client.FinalizeCodeIndex(exchange.callCtx, request)
		if err == nil {
			return errors.New("stale installed publication lease finalized a View")
		}
		if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "LEASE_STALE" {
			return fmt.Errorf("stale installed publication lease error = %v, want LEASE_STALE", err)
		}
		if err := uci1PublicationFaultAssertUnpublished(ctx, exchange.authority, build.GetBuildId(), baseline); err != nil {
			return err
		}
		leaseEpoch = build.GetLeaseEpoch()
		return nil
	})
	if err != nil {
		return "", err
	}
	return uciInstalledAcceptanceStringDigest("U06\x00" + strconv.FormatUint(leaseEpoch, 10)), nil
}

func uci1PublicationFaultWithBuild(ctx context.Context, exchange *uci1PublicationFaultExchange, purpose string, operation func(*pb.BeginCodeIndexResponse, uci1PublicationFaultBaseline) error) (err error) {
	if exchange == nil || exchange.authority == nil || exchange.client == nil || exchange.scope == nil || exchange.parent == nil || operation == nil {
		return errors.New("installed publication fault exchange is incomplete")
	}
	baseline, err := uci1PublicationFaultBaselineFor(ctx, exchange.authority, exchange.scope.GetCheckoutId(), exchange.parent.GetViewId())
	if err != nil {
		return err
	}
	build, err := exchange.client.BeginCodeIndex(exchange.callCtx, &pb.BeginCodeIndexRequest{
		Scope:          exchange.scope,
		OwnerInstance:  exchange.owner,
		BuildKey:       "uci1-publication-" + purpose + "-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		ExpectedParent: exchange.parent,
		ManifestMode:   "full",
		JobKind:        "reconcile",
	})
	if err != nil {
		return fmt.Errorf("begin installed publication %s fault: %w", purpose, err)
	}
	if build == nil || build.GetBuildId() == "" || build.GetLeaseEpoch() == 0 {
		return errors.New("installed publication fault Begin response is invalid")
	}
	defer func() {
		err = errors.Join(err, uci1PublicationFaultRestoreBuild(ctx, exchange.authority, build.GetBuildId(), baseline))
	}()
	return operation(build, baseline)
}

func uci1PublicationFaultBaselineFor(ctx context.Context, authority *uciInstalledAcceptanceAuthority, checkoutID, expectedViewID string) (uci1PublicationFaultBaseline, error) {
	if authority == nil || authority.store == nil || checkoutID == "" || expectedViewID == "" {
		return uci1PublicationFaultBaseline{}, errors.New("installed publication fault baseline is incomplete")
	}
	var checkout struct {
		CurrentViewID  sql.NullString `gorm:"column:current_view_id"`
		LeaseEpoch     int64          `gorm:"column:lease_epoch"`
		OwnerInstance  sql.NullString `gorm:"column:owner_instance"`
		LeaseExpiresAt sql.NullTime   `gorm:"column:lease_expires_at"`
	}
	if err := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT current_view_id, lease_epoch, owner_instance, lease_expires_at
		FROM ci_checkouts
		WHERE checkout_id = ?
	`, checkoutID).Scan(&checkout).Error; err != nil {
		return uci1PublicationFaultBaseline{}, fmt.Errorf("read installed publication fault checkout: %w", err)
	}
	if !checkout.CurrentViewID.Valid || checkout.CurrentViewID.String != expectedViewID || checkout.LeaseEpoch < 1 {
		return uci1PublicationFaultBaseline{}, errors.New("installed publication fault current pointer is invalid")
	}
	var viewCount int64
	if err := authority.store.GetDB().WithContext(ctx).Raw(`SELECT COUNT(*) FROM ci_views WHERE checkout_id = ?`, checkoutID).Scan(&viewCount).Error; err != nil {
		return uci1PublicationFaultBaseline{}, fmt.Errorf("count installed publication fault Views: %w", err)
	}
	if viewCount < 1 {
		return uci1PublicationFaultBaseline{}, errors.New("installed publication fault baseline has no View")
	}
	return uci1PublicationFaultBaseline{
		checkoutID:     checkoutID,
		currentViewID:  checkout.CurrentViewID.String,
		viewCount:      viewCount,
		leaseEpoch:     checkout.LeaseEpoch,
		ownerInstance:  checkout.OwnerInstance,
		leaseExpiresAt: checkout.LeaseExpiresAt,
	}, nil
}

func uci1PublicationFaultAssertUnpublished(ctx context.Context, authority *uciInstalledAcceptanceAuthority, buildID string, baseline uci1PublicationFaultBaseline) error {
	if authority == nil || authority.store == nil || buildID == "" {
		return errors.New("installed publication fault assertion is incomplete")
	}
	var job struct {
		ResultViewID sql.NullString `gorm:"column:result_view_id"`
	}
	if err := authority.store.GetDB().WithContext(ctx).Raw(`SELECT result_view_id FROM ci_jobs WHERE job_id = ?`, buildID).Scan(&job).Error; err != nil {
		return fmt.Errorf("read installed publication fault build: %w", err)
	}
	if job.ResultViewID.Valid {
		return errors.New("installed publication fault build unexpectedly produced a View")
	}
	return uci1PublicationFaultAssertBaseline(ctx, authority, baseline)
}

func uci1PublicationFaultAssertBaseline(ctx context.Context, authority *uciInstalledAcceptanceAuthority, baseline uci1PublicationFaultBaseline) error {
	actual, err := uci1PublicationFaultBaselineFor(ctx, authority, baseline.checkoutID, baseline.currentViewID)
	if err != nil {
		return err
	}
	if actual.viewCount != baseline.viewCount {
		return fmt.Errorf("installed publication fault View count = %d, want %d", actual.viewCount, baseline.viewCount)
	}
	return nil
}

func uci1PublicationFaultPublishedContext(ctx context.Context, exchange *uci1PublicationFaultExchange, build *pb.BeginCodeIndexResponse, baseline uci1PublicationFaultBaseline) (*pb.ContextRef, error) {
	if exchange == nil || exchange.authority == nil || exchange.authority.store == nil || exchange.scope == nil || exchange.parent == nil || build == nil || build.GetBuildId() == "" {
		return nil, errors.New("installed publication replay assertion is incomplete")
	}
	var published struct {
		ResultViewID sql.NullString `gorm:"column:result_view_id"`
		Generation   int64          `gorm:"column:generation"`
	}
	if err := exchange.authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT job.result_view_id, view_row.generation
		FROM ci_jobs AS job
		JOIN ci_views AS view_row ON view_row.view_id = job.result_view_id
		WHERE job.job_id = ?
	`, build.GetBuildId()).Scan(&published).Error; err != nil {
		return nil, fmt.Errorf("read installed publication replay result: %w", err)
	}
	if !published.ResultViewID.Valid || published.ResultViewID.String == baseline.currentViewID || published.Generation <= exchange.parent.GetGeneration() {
		return nil, errors.New("installed publication replay result is invalid")
	}
	current, err := uci1PublicationFaultBaselineFor(ctx, exchange.authority, baseline.checkoutID, published.ResultViewID.String)
	if err != nil {
		return nil, err
	}
	if current.viewCount != baseline.viewCount+1 {
		return nil, errors.New("installed publication replay did not leave exactly one additional View")
	}
	return &pb.ContextRef{
		SourceId:          exchange.scope.GetSourceId(),
		CheckoutId:        exchange.scope.GetCheckoutId(),
		ViewId:            published.ResultViewID.String,
		Generation:        published.Generation,
		AnalysisProfileId: exchange.scope.GetAnalysisProfileId(),
	}, nil
}

func uci1PublicationFaultReplayEvidenceDigest(requestBytes []byte, published *pb.ContextRef) (string, error) {
	if len(requestBytes) == 0 || published == nil {
		return "", errors.New("installed publication replay evidence is incomplete")
	}
	publishedBytes, err := (proto.MarshalOptions{Deterministic: true}).Marshal(published)
	if err != nil {
		return "", fmt.Errorf("marshal installed publication replay context: %w", err)
	}
	return uciInstalledAcceptanceStringDigest("U05\x00" + uciInstalledAcceptanceStringDigest(string(requestBytes)) + "\x00" + uciInstalledAcceptanceStringDigest(string(publishedBytes))), nil
}

func uci1PublicationFaultExpireLease(ctx context.Context, exchange *uci1PublicationFaultExchange, build *pb.BeginCodeIndexResponse) error {
	if exchange == nil || exchange.authority == nil || exchange.authority.store == nil || exchange.scope == nil || build == nil || build.GetBuildId() == "" || build.GetLeaseEpoch() == 0 {
		return errors.New("installed stale lease fault is incomplete")
	}
	db := exchange.authority.store.GetDB().WithContext(ctx)
	job := db.Exec(`
		UPDATE ci_jobs
		SET lease_expiry = clock_timestamp() - interval '1 second'
		WHERE job_id = ? AND owner_epoch = ? AND lease_owner = ? AND state = 'running'
	`, build.GetBuildId(), build.GetLeaseEpoch(), exchange.owner)
	if job.Error != nil {
		return fmt.Errorf("expire installed publication build lease: %w", job.Error)
	}
	if job.RowsAffected != 1 {
		return errors.New("installed publication build lease fault did not match one job")
	}
	checkout := db.Exec(`
		UPDATE ci_checkouts
		SET lease_expires_at = clock_timestamp() - interval '1 second'
		WHERE checkout_id = ? AND lease_epoch = ? AND owner_instance = ?
	`, exchange.scope.GetCheckoutId(), build.GetLeaseEpoch(), exchange.owner)
	if checkout.Error != nil {
		return fmt.Errorf("expire installed publication checkout lease: %w", checkout.Error)
	}
	if checkout.RowsAffected != 1 {
		return errors.New("installed publication checkout lease fault did not match one checkout")
	}
	return nil
}

func uci1PublicationFaultStagedFinalize(exchange *uci1PublicationFaultExchange, build *pb.BeginCodeIndexResponse, staged *pb.StageCodeIndexResponse) (*pb.FinalizeCodeIndexRequest, error) {
	if staged == nil || staged.GetBuildId() != build.GetBuildId() || staged.GetAcceptedPartCount() != 1 || staged.GetAcceptedSequence() != 0 || staged.GetPartDigest() == "" {
		return nil, errors.New("installed publication staged finalize is incomplete")
	}
	return uci1PublicationFaultFinalizeWithParts(exchange, build, staged.GetAcceptedPartCount(), staged.GetPartDigest())
}

func uci1PublicationFaultEmptyFinalize(exchange *uci1PublicationFaultExchange, build *pb.BeginCodeIndexResponse) (*pb.FinalizeCodeIndexRequest, error) {
	partsDigest, err := uci.DigestIndexParts(nil)
	if err != nil {
		return nil, fmt.Errorf("digest empty installed publication parts: %w", err)
	}
	return uci1PublicationFaultFinalizeWithParts(exchange, build, 0, string(partsDigest))
}

func uci1PublicationFaultFinalizeWithParts(exchange *uci1PublicationFaultExchange, build *pb.BeginCodeIndexResponse, manifestPartCount uint64, partsDigest string) (*pb.FinalizeCodeIndexRequest, error) {
	if exchange == nil || exchange.scope == nil || exchange.parent == nil || build == nil || build.GetBuildId() == "" || build.GetLeaseEpoch() == 0 || partsDigest == "" {
		return nil, errors.New("installed publication finalize request is incomplete")
	}
	manifestDigest, err := uci.DigestIndexManifest(nil)
	if err != nil {
		return nil, fmt.Errorf("digest empty installed publication manifest: %w", err)
	}
	edgesDigest, err := uci.DigestIndexEdges(nil)
	if err != nil {
		return nil, fmt.Errorf("digest empty installed publication edges: %w", err)
	}
	now := time.Now().UTC()
	dirty := false
	return &pb.FinalizeCodeIndexRequest{
		Scope:                      exchange.scope,
		BuildId:                    build.GetBuildId(),
		LeaseEpoch:                 build.GetLeaseEpoch(),
		ExpectedParent:             exchange.parent,
		ManifestPartCount:          manifestPartCount,
		PartsDigest:                partsDigest,
		ManifestEntryCount:         0,
		ManifestDigest:             string(manifestDigest),
		EdgeCount:                  0,
		EdgesDigest:                string(edgesDigest),
		ObservedFilesystemSequence: 1,
		ScanStartedAt:              timestamppb.New(now),
		ScanCompletedAt:            timestamppb.New(now),
		ScanOutcome:                string(uci.IndexScanComplete),
		CompleteCensus:             true,
		CoverageJson:               []byte(`{"structural":"complete","lexical":"complete","vector":"unavailable","excluded_files":0,"unreadable_files":0,"unresolved_references":0}`),
		Dirty:                      &dirty,
	}, nil
}

func uci1PublicationFaultRestoreBuild(ctx context.Context, authority *uciInstalledAcceptanceAuthority, buildID string, baseline uci1PublicationFaultBaseline) error {
	if authority == nil || authority.store == nil || buildID == "" || baseline.checkoutID == "" {
		return errors.New("installed publication fault restore is incomplete")
	}
	db := authority.store.GetDB().WithContext(ctx)
	tx := db.Begin()
	if tx.Error != nil {
		return fmt.Errorf("begin installed publication fault restore: %w", tx.Error)
	}
	failed := true
	defer func() {
		if failed {
			_ = tx.Rollback().Error
		}
	}()
	var published struct {
		ResultViewID sql.NullString `gorm:"column:result_view_id"`
	}
	if err := tx.Raw(`SELECT result_view_id FROM ci_jobs WHERE job_id = ? FOR UPDATE`, buildID).Scan(&published).Error; err != nil {
		return fmt.Errorf("read installed publication fault build for restore: %w", err)
	}
	restored := tx.Exec(`
		UPDATE ci_checkouts
		SET current_view_id = ?, lease_epoch = ?, owner_instance = ?, lease_expires_at = ?, updated_at = clock_timestamp()
		WHERE checkout_id = ?
	`, baseline.currentViewID, baseline.leaseEpoch, baseline.ownerInstance, baseline.leaseExpiresAt, baseline.checkoutID)
	if restored.Error != nil {
		return fmt.Errorf("restore installed publication checkout lease: %w", restored.Error)
	}
	if restored.RowsAffected != 1 {
		return errors.New("restore installed publication checkout lease did not match one checkout")
	}
	if err := tx.Exec(`DELETE FROM ci_index_build_parts WHERE build_id = ?`, buildID).Error; err != nil {
		return fmt.Errorf("clear installed publication fault staged parts: %w", err)
	}
	removed := tx.Exec(`DELETE FROM ci_jobs WHERE job_id = ?`, buildID)
	if removed.Error != nil {
		return fmt.Errorf("clear installed publication fault build: %w", removed.Error)
	}
	if removed.RowsAffected != 1 {
		return errors.New("clear installed publication fault build did not match one job")
	}
	if published.ResultViewID.Valid {
		removedView := tx.Exec(`DELETE FROM ci_views WHERE view_id = ? AND checkout_id = ?`, published.ResultViewID.String, baseline.checkoutID)
		if removedView.Error != nil {
			return fmt.Errorf("clear installed publication fault View: %w", removedView.Error)
		}
		if removedView.RowsAffected != 1 {
			return errors.New("clear installed publication fault View did not match one View")
		}
	}
	if err := tx.Commit().Error; err != nil {
		return fmt.Errorf("commit installed publication fault restore: %w", err)
	}
	failed = false
	return uci1PublicationFaultAssertBaseline(ctx, authority, baseline)
}
