package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"golang.org/x/crypto/bcrypt"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type uci1CompletionProbeState struct {
	firstExposure  string
	secondExposure string
	firstBefore    uci1CompletionParent
	secondBefore   uci1CompletionParent
}

// uci1ProbeCompletionInstalled exercises the supported-host callback strictly
// through the installed gRPC transport. It leaves only redacted U42/U45 proof.
func uci1ProbeCompletionInstalled(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (_ map[string]uciInstalledAcceptanceScenarioEvidence, retErr error) {
	if ctx == nil {
		return nil, errors.New("installed completion scenario context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runtime.Recorder == nil || !runtime.Recorder.Transcript().UsedStdio || runtime.Authority == nil || runtime.Authority.store == nil || runtime.Authority.projectKey == "" {
		return nil, errors.New("installed completion scenario runtime is incomplete")
	}
	selection, found := runtime.Selections[uciInstalledAcceptanceClientRecorder]
	if !found || selection.contextHandle == "" {
		return nil, errors.New("installed completion recorder selection is unavailable")
	}

	rawKeycard, err := uciInstalledAcceptanceKeycard()
	if err != nil {
		return nil, err
	}
	tokenHash, err := bcrypt.GenerateFromPassword([]byte(rawKeycard), bcrypt.DefaultCost)
	if err != nil {
		return nil, errors.New("hash installed completion project-service keycard")
	}
	keycards := gormdb.NewTokenStore(runtime.Authority.store)
	expiresAt := uciInstalledAcceptanceTokenExpiry(ctx, time.Now())
	keycard, err := keycards.CreateWithPrincipal(ctx, gormdb.TokenCreatePrincipalInput{
		Name:          "uci1-completion-" + uuid.NewString(),
		TokenHash:     string(tokenHash),
		TokenPrefix:   rawKeycard[len("engram_") : len("engram_")+8],
		Scope:         "read-write",
		Principal:     auth.ProjectServicePrincipal(runtime.Authority.projectKey),
		PrincipalKind: string(auth.PrincipalKindService),
		ExpiresAt:     &expiresAt,
	})
	if err != nil {
		return nil, errors.New("create installed completion project-service keycard")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanupErr := keycards.Revoke(cleanupCtx, keycard.ID)
		if err := runtime.Authority.store.GetDB().WithContext(cleanupCtx).Exec(`DELETE FROM api_tokens WHERE id = ?`, keycard.ID).Error; err != nil {
			cleanupErr = errors.Join(cleanupErr, errors.New("delete installed completion project-service keycard"))
		}
		rawKeycard = ""
		retErr = errors.Join(retErr, cleanupErr)
	}()

	state, err := uci1CompletionPrepareProbeState(ctx, runtime, selection)
	if err != nil {
		return nil, err
	}

	u42Digest, firstAfterMismatch, err := uci1CompletionObserveU42(ctx, runtime, state, rawKeycard)
	if err != nil {
		return nil, err
	}

	u45Digest, err := uci1CompletionObserveU45(ctx, runtime, state, rawKeycard, firstAfterMismatch)
	if err != nil {
		return nil, err
	}

	return map[string]uciInstalledAcceptanceScenarioEvidence{
		"U42": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u42Digest},
		"U45": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u45Digest},
	}, nil
}

func uci1CompletionPrepareProbeState(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, selection uciInstalledAcceptanceSelection) (uci1CompletionProbeState, error) {
	firstExposure, err := uci1CompletionCreateExposure(ctx, runtime, selection)
	if err != nil {
		return uci1CompletionProbeState{}, err
	}
	secondExposure, err := uci1CompletionCreateExposure(ctx, runtime, selection)
	if err != nil {
		return uci1CompletionProbeState{}, err
	}
	if firstExposure == secondExposure {
		return uci1CompletionProbeState{}, errors.New("installed completion probe did not create fresh exposure parents")
	}
	firstBefore, err := uci1CompletionReadParent(ctx, runtime.Authority, firstExposure)
	if err != nil {
		return uci1CompletionProbeState{}, err
	}
	secondBefore, err := uci1CompletionReadParent(ctx, runtime.Authority, secondExposure)
	if err != nil {
		return uci1CompletionProbeState{}, err
	}
	if firstBefore.CompletionCount != 0 || firstBefore.CompletionOutcome != "" || secondBefore.CompletionCount != 0 || secondBefore.CompletionOutcome != "" {
		return uci1CompletionProbeState{}, errors.New("fresh installed completion parent already has child evidence")
	}
	return uci1CompletionProbeState{firstExposure: firstExposure, secondExposure: secondExposure, firstBefore: firstBefore, secondBefore: secondBefore}, nil
}

func uci1CompletionObserveU42(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, state uci1CompletionProbeState, rawKeycard string) (string, uci1CompletionParent, error) {
	callback := uci1InstalledCompletionCallback{
		CanonicalProject: runtime.Authority.projectKey,
		ExposureRef:      state.firstExposure,
		SupportedHostRef: "uci1-supported-host-" + uuid.NewString(),
		CallbackRef:      "uci1-completion-" + uuid.NewString(),
		Outcome:          pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_PARTIAL,
		IdempotencyKey:   "uci1-completion-" + uuid.NewString(),
	}
	accepted, err := uci1RecordInstalledCompletion(ctx, runtime, rawKeycard, callback)
	if err != nil || !accepted {
		return "", uci1CompletionParent{}, errors.New("installed partial completion callback was not accepted")
	}
	accepted, err = uci1RecordInstalledCompletion(ctx, runtime, rawKeycard, callback)
	if err != nil || !accepted {
		return "", uci1CompletionParent{}, errors.New("installed completion exact retry was not accepted")
	}
	firstAfterRetry, err := uci1CompletionReadParent(ctx, runtime.Authority, state.firstExposure)
	if err != nil {
		return "", uci1CompletionParent{}, err
	}
	secondAfterU42, err := uci1CompletionReadParent(ctx, runtime.Authority, state.secondExposure)
	if err != nil {
		return "", uci1CompletionParent{}, err
	}
	if firstAfterRetry.CompletionCount != 1 || firstAfterRetry.CompletionOutcome != "partial" || secondAfterU42.CompletionCount != 0 || secondAfterU42.CompletionOutcome != "" ||
		!uci1CompletionRetrievalUnchanged(state.firstBefore, firstAfterRetry) || !uci1CompletionRetrievalUnchanged(state.secondBefore, secondAfterU42) {
		return "", uci1CompletionParent{}, errors.New("installed completion U42 parent state is not durable and unchanged")
	}
	digest := uci1CompletionEvidenceDigest("U42", state.firstExposure, state.secondExposure, firstAfterRetry, secondAfterU42, "partial_exact_retry")

	mismatchCallback := callback
	mismatchCallback.CallbackRef = "uci1-completion-mismatch-" + uuid.NewString()
	mismatchCallback.Outcome = pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_FAILED
	accepted, err = uci1RecordInstalledCompletion(ctx, runtime, rawKeycard, mismatchCallback)
	mismatchCode, mismatchMessage := uci1CompletionGRPCStatus(err)
	if accepted || err == nil || mismatchCode != codes.FailedPrecondition || mismatchMessage != "IDEMPOTENCY_MISMATCH" {
		return "", uci1CompletionParent{}, errors.New("installed completion mismatch did not return safe FailedPrecondition IDEMPOTENCY_MISMATCH")
	}
	firstAfterMismatch, err := uci1CompletionReadParent(ctx, runtime.Authority, state.firstExposure)
	if err != nil {
		return "", uci1CompletionParent{}, err
	}
	if !uci1CompletionParentEqual(firstAfterRetry, firstAfterMismatch) {
		return "", uci1CompletionParent{}, errors.New("installed completion mismatch changed retained parent state")
	}
	return digest, firstAfterMismatch, nil
}

func uci1CompletionObserveU45(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, state uci1CompletionProbeState, rawKeycard string, firstAfterMismatch uci1CompletionParent) (digest string, retErr error) {
	fault, err := uci1InstallCompletionInsertFault(ctx, runtime.Authority)
	if err != nil {
		return "", err
	}
	defer func() {
		retErr = errors.Join(retErr, fault.Close())
	}()
	callback := uci1InstalledCompletionCallback{
		CanonicalProject: runtime.Authority.projectKey,
		ExposureRef:      state.secondExposure,
		SupportedHostRef: "uci1-supported-host-" + uuid.NewString(),
		CallbackRef:      "uci1-completion-fault-" + uuid.NewString(),
		Outcome:          pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_FAILED,
		IdempotencyKey:   "uci1-completion-fault-" + uuid.NewString(),
	}
	accepted, err := uci1RecordInstalledCompletion(ctx, runtime, rawKeycard, callback)
	failureCode, failureMessage := uci1CompletionGRPCStatus(err)
	if accepted || err == nil || failureCode != codes.Unavailable || failureMessage != "COMPLETION_EVIDENCE_UNAVAILABLE" {
		return "", errors.New("installed completion insert fault did not return safe Unavailable")
	}
	if err := fault.Close(); err != nil {
		return "", err
	}
	secondAfterFault, err := uci1CompletionReadParent(ctx, runtime.Authority, state.secondExposure)
	if err != nil {
		return "", err
	}
	if secondAfterFault.CompletionCount != 0 || secondAfterFault.CompletionOutcome != "" || !uci1CompletionRetrievalUnchanged(state.secondBefore, secondAfterFault) {
		return "", errors.New("installed completion fault did not preserve unknown parent without child evidence")
	}
	return uci1CompletionEvidenceDigest("U45", state.firstExposure, state.secondExposure, firstAfterMismatch, secondAfterFault, "idempotency_mismatch", "completion_insert_unavailable"), nil
}

type uci1CompletionParent struct {
	ExposureID        string `gorm:"column:exposure_id"`
	ResultState       string `gorm:"column:result_state"`
	RetrievalMode     string `gorm:"column:retrieval_mode"`
	CoverageState     string `gorm:"column:coverage_state"`
	EvidenceSource    string `gorm:"column:evidence_source"`
	Certainty         string `gorm:"column:certainty"`
	CompletionCount   int64  `gorm:"column:completion_count"`
	CompletionOutcome string `gorm:"column:completion_outcome"`
}

func uci1CompletionCreateExposure(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, selection uciInstalledAcceptanceSelection) (string, error) {
	result, err := runtime.Recorder.ToolWithCall(ctx, "codebase_search", uciInstalledAcceptanceSearchArguments(selection.contextHandle, runtime.Request.Fixture.SharedSymbol))
	if err != nil {
		return "", errors.New("create installed completion exposure")
	}
	if result.isError {
		return "", errors.New("installed completion exposure search returned protocol error")
	}
	exposureRef, err := uciInstalledAcceptanceExposureReference(result.payload)
	if err != nil {
		return "", errors.New("decode installed completion exposure")
	}
	return exposureRef, nil
}

func uci1CompletionReadParent(ctx context.Context, authority *uciInstalledAcceptanceAuthority, exposureRef string) (uci1CompletionParent, error) {
	if authority == nil || authority.store == nil || exposureRef == "" {
		return uci1CompletionParent{}, errors.New("installed completion parent authority is incomplete")
	}
	var parent uci1CompletionParent
	result := authority.store.GetDB().WithContext(ctx).Raw(`
		SELECT exposure.exposure_id,
		       exposure.result_state,
		       exposure.retrieval_mode,
		       exposure.coverage_state,
		       exposure.evidence_source,
		       exposure.certainty,
		       COUNT(completion.completion_evidence_id) AS completion_count,
		       COALESCE(MAX(completion.outcome), '') AS completion_outcome
		FROM uci_exposures AS exposure
		LEFT JOIN uci_completion_evidence AS completion ON completion.exposure_id = exposure.exposure_id
		WHERE exposure.exposure_ref = ?
		GROUP BY exposure.exposure_id, exposure.result_state, exposure.retrieval_mode, exposure.coverage_state, exposure.evidence_source, exposure.certainty
	`, exposureRef).Scan(&parent)
	if result.Error != nil {
		return uci1CompletionParent{}, fmt.Errorf("read installed completion parent: %w", result.Error)
	}
	if result.RowsAffected != 1 || parent.ExposureID == "" {
		return uci1CompletionParent{}, errors.New("installed completion parent is missing")
	}
	return parent, nil
}

func uci1CompletionGRPCStatus(err error) (codes.Code, string) {
	var resultCode codes.Code
	var resultMessage string
	for current := err; current != nil; current = errors.Unwrap(current) {
		if grpcStatus, ok := status.FromError(current); ok {
			resultCode = grpcStatus.Code()
			resultMessage = grpcStatus.Message()
		}
	}
	return resultCode, resultMessage
}

func uci1CompletionRetrievalUnchanged(before, after uci1CompletionParent) bool {
	return before.ExposureID == after.ExposureID &&
		before.ResultState == after.ResultState &&
		before.RetrievalMode == after.RetrievalMode &&
		before.CoverageState == after.CoverageState &&
		before.EvidenceSource == after.EvidenceSource &&
		before.Certainty == after.Certainty
}

func uci1CompletionParentEqual(left, right uci1CompletionParent) bool {
	return uci1CompletionRetrievalUnchanged(left, right) &&
		left.CompletionCount == right.CompletionCount &&
		left.CompletionOutcome == right.CompletionOutcome
}

func uci1CompletionEvidenceDigest(scenarioID, firstExposure, secondExposure string, first, second uci1CompletionParent, facts ...string) string {
	parts := append([]string{
		"engram.uci1-probe-completion/v1",
		scenarioID,
		uciInstalledAcceptanceStringDigest(firstExposure),
		uciInstalledAcceptanceStringDigest(secondExposure),
		first.ResultState,
		first.RetrievalMode,
		first.CoverageState,
		first.EvidenceSource,
		first.Certainty,
		strconv.FormatInt(first.CompletionCount, 10),
		first.CompletionOutcome,
		second.ResultState,
		second.RetrievalMode,
		second.CoverageState,
		second.EvidenceSource,
		second.Certainty,
		strconv.FormatInt(second.CompletionCount, 10),
		second.CompletionOutcome,
	}, facts...)
	return uciInstalledAcceptanceStringDigest(strings.Join(parts, "\x00"))
}

type uci1CompletionInsertFault struct {
	authority    *uciInstalledAcceptanceAuthority
	functionName string
	triggerName  string
	closed       bool
}

func uci1InstallCompletionInsertFault(ctx context.Context, authority *uciInstalledAcceptanceAuthority) (*uci1CompletionInsertFault, error) {
	if ctx == nil || authority == nil || authority.store == nil || !uciInstalledAcceptanceRunSchema(authority.schema) {
		return nil, errors.New("installed completion fault authority is incomplete")
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	fault := &uci1CompletionInsertFault{
		authority:    authority,
		functionName: "uci_comp_fail_" + suffix,
		triggerName:  "uci_comp_fail_tr_" + suffix,
	}
	schema := pq.QuoteIdentifier(authority.schema)
	qualifiedTable := schema + "." + pq.QuoteIdentifier("uci_completion_evidence")
	qualifiedFunction := schema + "." + pq.QuoteIdentifier(fault.functionName)
	db := authority.store.GetDB().WithContext(ctx)
	if err := db.Exec(`CREATE FUNCTION ` + qualifiedFunction + `() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'installed UCI completion append fault'; END; $$`).Error; err != nil {
		return nil, errors.New("install completion insert fault function")
	}
	if err := db.Exec(`CREATE TRIGGER ` + pq.QuoteIdentifier(fault.triggerName) + ` BEFORE INSERT ON ` + qualifiedTable + ` FOR EACH ROW EXECUTE FUNCTION ` + qualifiedFunction + `()`).Error; err != nil {
		return nil, errors.Join(errors.New("install completion insert fault trigger"), fault.Close())
	}
	return fault, nil
}

func (fault *uci1CompletionInsertFault) Close() error {
	if fault == nil || fault.closed {
		return nil
	}
	if fault.authority == nil || fault.authority.store == nil || !uciInstalledAcceptanceRunSchema(fault.authority.schema) {
		return errors.New("installed completion fault cleanup authority is incomplete")
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	schema := pq.QuoteIdentifier(fault.authority.schema)
	qualifiedTable := schema + "." + pq.QuoteIdentifier("uci_completion_evidence")
	qualifiedFunction := schema + "." + pq.QuoteIdentifier(fault.functionName)
	db := fault.authority.store.GetDB().WithContext(cleanupCtx)
	triggerErr := db.Exec(`DROP TRIGGER IF EXISTS ` + pq.QuoteIdentifier(fault.triggerName) + ` ON ` + qualifiedTable).Error
	functionErr := db.Exec(`DROP FUNCTION IF EXISTS ` + qualifiedFunction + `()`).Error
	if triggerErr == nil && functionErr == nil {
		fault.closed = true
	}
	if triggerErr != nil || functionErr != nil {
		return errors.New("remove installed completion insert fault")
	}
	return nil
}
