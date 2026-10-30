package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
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

// uci1ProbeCompletionInstalled denies a distinct service callback in the real
// MCP parent's portable-marker project. Positive loopback-gRPC callbacks use a
// separate synthetic service-owned parent bound to the fixture catalog project;
// they do not prove public MCP service registration or retrieval.
func uci1ProbeCompletionInstalled(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime) (_ map[string]uciInstalledAcceptanceScenarioEvidence, retErr error) {
	if ctx == nil {
		return nil, errors.New("installed completion scenario context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runtime.Recorder == nil || !runtime.Recorder.Transcript().UsedStdio || runtime.Authority == nil || runtime.Authority.store == nil || runtime.Authority.projectKey == "" || runtime.Authority.anchorProjectID == "" {
		return nil, errors.New("installed completion scenario runtime is incomplete")
	}
	selection, found := runtime.Selections[uciInstalledAcceptanceClientRecorder]
	if !found || selection.contextHandle == "" {
		return nil, errors.New("installed completion recorder selection is unavailable")
	}

	// This credential belongs only to the synthetic store-owned positive
	// fixture. Its Source is registered under this same catalog project below.
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

	unauthorizedParent, err := uci1CompletionCreateExposure(ctx, runtime, selection)
	if err != nil {
		return nil, err
	}
	// Native public registration uses the already-pinned portable marker UUID,
	// not the separate catalog-project seed used by the positive storage fixture.
	publicRaw, err := uciInstalledAcceptanceKeycard()
	if err != nil {
		return nil, err
	}
	publicHash, err := bcrypt.GenerateFromPassword([]byte(publicRaw), bcrypt.DefaultCost)
	if err != nil {
		return nil, errors.New("hash public-parent same-project service keycard")
	}
	publicService, err := keycards.CreateWithPrincipal(ctx, gormdb.TokenCreatePrincipalInput{
		Name: "uci1-public-parent-service-" + uuid.NewString(), TokenHash: string(publicHash),
		TokenPrefix: publicRaw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen],
		Scope:       "read-write", Principal: auth.ProjectServicePrincipal(runtime.Authority.anchorProjectID),
		PrincipalKind: string(auth.PrincipalKindService), ExpiresAt: &expiresAt,
	})
	if err != nil {
		return nil, errors.New("create public-parent same-project service keycard")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cleanupErr := keycards.Revoke(cleanupCtx, publicService.ID)
		if err := runtime.Authority.store.GetDB().WithContext(cleanupCtx).Delete(&gormdb.APIToken{}, "id = ?", publicService.ID).Error; err != nil {
			cleanupErr = errors.Join(cleanupErr, errors.New("delete public-parent same-project service keycard"))
		}
		publicRaw = ""
		retErr = errors.Join(retErr, cleanupErr)
	}()
	denied := uci1InstalledCompletionCallback{
		CanonicalProject: runtime.Authority.anchorProjectID, ExposureRef: unauthorizedParent,
		SupportedHostRef: "uci1-denied-host-" + uuid.NewString(), CallbackRef: "uci1-denied-callback-" + uuid.NewString(),
		Outcome: pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_PARTIAL, IdempotencyKey: "uci1-denied-key-" + uuid.NewString(),
	}
	if err := uci1CompletionRequireDenied(ctx, runtime, publicRaw, denied); err != nil {
		return nil, err
	}
	state, ownerCheckoutID, err := uci1CompletionPrepareServiceOwnerState(ctx, runtime, keycard, rawKeycard)
	if err != nil {
		return nil, err
	}
	u42Digest, firstAfterMismatch, err := uci1CompletionObserveU42(ctx, runtime, state, rawKeycard)
	if err != nil {
		return nil, err
	}
	if err := uci1CompletionRequireForeignDenied(ctx, runtime, state.firstExposure); err != nil {
		return nil, err
	}
	u45Digest, err := uci1CompletionObserveU45(ctx, runtime, state, rawKeycard, firstAfterMismatch)
	if err != nil {
		return nil, err
	}
	ownerAfter, err := gormdb.NewUCIContextStore(runtime.Authority.store.GetDB()).GetCheckout(ctx, ownerCheckoutID)
	if err != nil || ownerAfter.OwnerPrincipal != keycard.Principal {
		return nil, errors.New("installed completion changed the genuinely stored service owner")
	}

	return map[string]uciInstalledAcceptanceScenarioEvidence{
		"U42": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u42Digest},
		"U45": {Code: uciInstalledAcceptanceScenarioCodeObserved, Digest: u45Digest},
	}, nil
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
	auditBefore, err := uci1CompletionAcceptedAuditCount(ctx, runtime.Authority, "")
	if err != nil {
		return "", uci1CompletionParent{}, err
	}
	actorBefore, err := uci1CompletionAcceptedAuditCount(ctx, runtime.Authority, auth.ProjectServicePrincipal(runtime.Authority.projectKey))
	if err != nil {
		return "", uci1CompletionParent{}, err
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
	auditAfter, err := uci1CompletionAcceptedAuditCount(ctx, runtime.Authority, "")
	if err != nil {
		return "", uci1CompletionParent{}, err
	}
	actorAfter, err := uci1CompletionAcceptedAuditCount(ctx, runtime.Authority, auth.ProjectServicePrincipal(runtime.Authority.projectKey))
	if err != nil {
		return "", uci1CompletionParent{}, err
	}
	if auditAfter != auditBefore+1 || actorAfter != actorBefore+1 {
		return "", uci1CompletionParent{}, errors.New("installed completion callback and exact retry did not retain exactly one audit attributed to the actual service actor")
	}
	digest := uci1CompletionEvidenceDigest("U42", state.firstExposure, state.secondExposure, firstAfterRetry, secondAfterU42, "synthetic_service_owned_parent", "not_public_mcp_retrieval", "partial_exact_retry_single_actor_audit")

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
	auditAfterMismatch, err := uci1CompletionAcceptedAuditCount(ctx, runtime.Authority, "")
	if err != nil || auditAfterMismatch != auditAfter {
		return "", uci1CompletionParent{}, errors.New("installed completion mismatch added an accepted audit")
	}
	return digest, firstAfterMismatch, nil
}

func uci1CompletionObserveU45(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, state uci1CompletionProbeState, rawKeycard string, firstAfterMismatch uci1CompletionParent) (digest string, retErr error) {
	auditBefore, err := uci1CompletionAcceptedAuditCount(ctx, runtime.Authority, "")
	if err != nil {
		return "", err
	}
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
	auditAfter, err := uci1CompletionAcceptedAuditCount(ctx, runtime.Authority, "")
	if err != nil || auditAfter != auditBefore {
		return "", errors.New("installed completion append failure added an accepted audit")
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

func uci1CompletionAcceptedAuditCount(ctx context.Context, authority *uciInstalledAcceptanceAuthority, actor string) (int64, error) {
	query := authority.store.GetDB().WithContext(ctx).Model(&gormdb.AuditLogEntry{}).Where("action = ?", "uci_completion_accepted")
	if actor != "" {
		query = query.Where("actor = ?", actor)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, errors.New("read installed completion accepted audit count")
	}
	return count, nil
}

func uci1CompletionRequireDenied(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, rawKeycard string, callback uci1InstalledCompletionCallback) error {
	before, err := uci1CompletionReadParent(ctx, runtime.Authority, callback.ExposureRef)
	if err != nil {
		return err
	}
	auditBefore, err := uci1CompletionAcceptedAuditCount(ctx, runtime.Authority, "")
	if err != nil {
		return err
	}
	malformed := callback
	malformed.CallbackRef = ""
	accepted, callErr := uci1RecordInstalledCompletion(ctx, runtime, rawKeycard, malformed)
	code, _ := uci1CompletionGRPCStatus(callErr)
	if accepted || code != codes.InvalidArgument {
		return errors.New("completion denial control did not first authenticate its actual project-service keycard")
	}
	for attempt := range 3 {
		if attempt == 2 {
			callback.CallbackRef += "-changed-binding"
			callback.Outcome = pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_FAILED
		}
		accepted, callErr := uci1RecordInstalledCompletion(ctx, runtime, rawKeycard, callback)
		code, message := uci1CompletionGRPCStatus(callErr)
		if accepted || callErr == nil || code != codes.PermissionDenied ||
			strings.Contains(message, callback.ExposureRef) || strings.Contains(message, before.ExposureID) || strings.Contains(message, "IDEMPOTENCY_MISMATCH") {
			return errors.New("unauthorized completion append, replay, or rebound retry disclosed or bypassed parent authority")
		}
		after, err := uci1CompletionReadParent(ctx, runtime.Authority, callback.ExposureRef)
		if err != nil {
			return err
		}
		auditAfter, err := uci1CompletionAcceptedAuditCount(ctx, runtime.Authority, "")
		if err != nil || auditAfter != auditBefore || !uci1CompletionParentEqual(before, after) {
			return errors.New("unauthorized completion changed parent, child, or accepted audit evidence")
		}
	}
	return nil
}

func uci1CompletionRequireForeignDenied(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, parent string) (retErr error) {
	raw, err := uciInstalledAcceptanceKeycard()
	if err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(raw), bcrypt.DefaultCost)
	if err != nil {
		return errors.New("hash foreign completion fixture keycard")
	}
	foreignProject := uuid.NewString()
	expiresAt := uciInstalledAcceptanceTokenExpiry(ctx, time.Now())
	tokens := gormdb.NewTokenStore(runtime.Authority.store)
	token, err := tokens.CreateWithPrincipal(ctx, gormdb.TokenCreatePrincipalInput{
		Name: "uci1-completion-foreign-" + uuid.NewString(), TokenHash: string(hash),
		TokenPrefix: raw[len(auth.TokenRawPrefix) : len(auth.TokenRawPrefix)+auth.TokenPrefixLen],
		Scope:       "read-write", Principal: auth.ProjectServicePrincipal(foreignProject),
		PrincipalKind: string(auth.PrincipalKindService), ExpiresAt: &expiresAt,
	})
	if err != nil {
		return errors.New("create foreign completion fixture keycard")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		retErr = errors.Join(retErr, tokens.Revoke(cleanupCtx, token.ID))
		if err := runtime.Authority.store.GetDB().WithContext(cleanupCtx).Delete(&gormdb.APIToken{}, "id = ?", token.ID).Error; err != nil {
			retErr = errors.Join(retErr, errors.New("delete foreign completion fixture keycard"))
		}
	}()
	parentState, err := uci1CompletionReadParent(ctx, runtime.Authority, parent)
	if err != nil {
		return err
	}
	var acceptedChild gormdb.UCICompletionEvidence
	if err := runtime.Authority.store.GetDB().WithContext(ctx).Where("exposure_id = ?", parentState.ExposureID).First(&acceptedChild).Error; err != nil {
		return errors.New("read accepted completion binding for unauthorized exact-replay control")
	}
	return uci1CompletionRequireDenied(ctx, runtime, raw, uci1InstalledCompletionCallback{
		CanonicalProject: foreignProject, ExposureRef: parent,
		SupportedHostRef: acceptedChild.SupportedHostRef, CallbackRef: acceptedChild.CallbackRef,
		Outcome: pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_PARTIAL, IdempotencyKey: acceptedChild.IdempotencyKey,
	})
}

// HAP project-service keycards cannot Initialize/CallTool on the normal MCP
// route. These parents are therefore labelled storage fixtures, not fabricated
// public searches. Existing admission/publisher/recorder owners create every
// row; no Source.ProjectID, checkout owner, or View state is rewritten by SQL.
func uci1CompletionPrepareServiceOwnerState(ctx context.Context, runtime uciInstalledAcceptanceScenarioRuntime, keycard *gormdb.APIToken, raw string) (uci1CompletionProbeState, string, error) {
	identity, err := auth.NewValidator("", gormdb.NewTokenStore(runtime.Authority.store)).Validate(ctx, raw)
	if err != nil || !identity.IsHAPProjectServiceFor(runtime.Authority.projectKey) || identity.Role != auth.RoleReadWrite || identity.Principal != keycard.Principal {
		return uci1CompletionProbeState{}, "", errors.New("authenticate actual synthetic service-owner fixture credential")
	}
	root := filepath.Join(runtime.Request.FixtureRoot, "completion-empty-service-owner-"+uuid.NewString())
	if err := os.Mkdir(root, 0o700); err != nil {
		return uci1CompletionProbeState{}, "", errors.New("create disposable empty service-owner Git fixture")
	}
	if err := uciRunInstalledAcceptanceGit(ctx, root, "init"); err != nil {
		return uci1CompletionProbeState{}, "", err
	}
	locator, err := uciInstalledAcceptanceFileURI(root)
	if err != nil {
		return uci1CompletionProbeState{}, "", err
	}
	contexts := gormdb.NewUCIContextStore(runtime.Authority.store.GetDB())
	parserBundle := false
	registered, err := contexts.RegisterLocalGit(ctx, gormdb.RegisterLocalGitInput{
		AuthRealm: string(identity.Source), Principal: identity.Principal, WorkstationID: identity.WorkstationID(),
		ProjectID: runtime.Authority.projectKey, SourceLabel: "synthetic-service-completion-" + uuid.NewString(),
		Locator: locator, ParserBundle: &parserBundle,
	})
	if err != nil {
		return uci1CompletionProbeState{}, "", errors.New("admit synthetic service-owned checkout through existing store owner")
	}
	publisher, err := gormdb.NewUCIProjectionStore(runtime.Authority.store.GetDB()).Publisher(gormdb.NewUCIContextAuthorizer(contexts), uci.IndexPublicationConfig{Limits: uci.DefaultIndexPublicationLimits()})
	if err != nil {
		return uci1CompletionProbeState{}, "", err
	}
	caller := uci.IndexCaller{AuthRealm: string(identity.Source), Principal: identity.Principal, OwnerInstance: "synthetic-completion-" + uuid.NewString()}
	build, err := publisher.Begin(ctx, caller, uci.IndexBeginInput{
		BuildKey:  "synthetic-empty-" + uuid.NewString(),
		Scope:     uci.IndexScope{SourceID: registered.SourceID, CheckoutID: registered.CheckoutID, IncarnationID: registered.IncarnationID},
		ProfileID: registered.ProfileID, Mode: uci.IndexManifestFull, JobKind: uci.IndexJobInitial,
	})
	if err != nil {
		return uci1CompletionProbeState{}, "", err
	}
	partsDigest, err := uci.DigestIndexParts(nil)
	if err != nil {
		return uci1CompletionProbeState{}, "", err
	}
	manifestDigest, err := uci.DigestIndexManifest(nil)
	if err != nil {
		return uci1CompletionProbeState{}, "", err
	}
	edgesDigest, err := uci.DigestIndexEdges(nil)
	if err != nil {
		return uci1CompletionProbeState{}, "", err
	}
	now := time.Now().UTC()
	published, err := publisher.Finalize(ctx, caller, uci.IndexFinalizeInput{Build: build.Build, Manifest: uci.IndexManifestCompletion{
		PartsDigest: partsDigest, ManifestDigest: manifestDigest, EdgesDigest: edgesDigest,
		ScanOutcome: uci.IndexScanComplete, CensusComplete: true,
		Observation: uci.IndexObservation{ObservedFSSeq: 1, ScanStart: now, ScanEnd: now},
		Coverage:    uci.IndexCoverage{Structural: uci.IndexCoverageComplete, Lexical: uci.IndexCoverageComplete, Vector: uci.IndexCoverageUnavailable},
	}})
	if err != nil {
		return uci1CompletionProbeState{}, "", err
	}
	store := gormdb.NewUCIExposureStore(runtime.Authority.store.GetDB())
	parents := make([]string, 0, 2)
	for range 2 {
		suffix := uuid.NewString()
		record := uci.ExposureRecord{
			AuthRealm: string(identity.Source), SourceID: registered.SourceID, CheckoutID: registered.CheckoutID, ViewID: published.Context.ViewID,
			ClientRef:        uciInstalledAcceptanceSHA256Prefix + uciInstalledAcceptanceStringDigest(identity.WorkstationID()),
			ClientSessionRef: uciInstalledAcceptanceSHA256Prefix + uciInstalledAcceptanceStringDigest("synthetic-session-"+suffix),
			RequestRef:       uciInstalledAcceptanceSHA256Prefix + uciInstalledAcceptanceStringDigest("synthetic-empty-request-"+suffix),
			IdempotencyKey:   uciInstalledAcceptanceSHA256Prefix + uciInstalledAcceptanceStringDigest("synthetic-empty-key-"+suffix),
			Operation:        uci.ExposureOperationCodeSearch, Result: uci.ExposureResultEmpty, Retrieval: uci.ExposureRetrievalExact,
			Coverage: uci.ExposureCoverageComplete, Evidence: uci.ExposureEvidenceExact, Certainty: uci.ExposureCertaintyEstablished, RecordedAt: now,
		}
		record.BindingDigest, err = uci.CanonicalExposureBindingDigest(record)
		if err != nil {
			return uci1CompletionProbeState{}, "", err
		}
		parent, err := store.AppendExposure(ctx, record)
		if err != nil {
			return uci1CompletionProbeState{}, "", err
		}
		parents = append(parents, parent.ExposureRef)
	}
	first, err := uci1CompletionReadParent(ctx, runtime.Authority, parents[0])
	if err != nil {
		return uci1CompletionProbeState{}, "", err
	}
	second, err := uci1CompletionReadParent(ctx, runtime.Authority, parents[1])
	if err != nil {
		return uci1CompletionProbeState{}, "", err
	}
	if first.CompletionCount != 0 || second.CompletionCount != 0 || parents[0] == parents[1] {
		return uci1CompletionProbeState{}, "", errors.New("synthetic service-owned parents are not fresh and distinct")
	}
	return uci1CompletionProbeState{firstExposure: parents[0], secondExposure: parents[1], firstBefore: first, secondBefore: second}, registered.CheckoutID, nil
}
