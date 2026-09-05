package intervention

import (
	"context"
	"testing"
	"time"

	"github.com/thebtf/engram/internal/projectidentity"
	"github.com/thebtf/engram/internal/taskmemory"
)

func TestReceiptTrustSeparatesPersistenceFromAuthoring(t *testing.T) {
	ctx, cancel := receiptTestContext(t)
	defer cancel()
	epoch := fixtureKeyEpoch(t)
	axis := receiptTestAxis(t, epoch)
	receipt := mustNoCandidatesReceipt(t, ctx, epoch, axis, "10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002")

	if !receipt.valid() || !receipt.CanCommit() || !receipt.Verify(epoch) {
		t.Fatal("signed constructor receipt was not trusted and authorable")
	}
	signedIdentity := receipt.Identity()
	if !signedIdentity.valid() {
		t.Fatal("signed constructor receipt did not expose identity")
	}

	restored, err := RestoreUnverifiedReceipt(receipt.PersistenceRecord())
	if err != nil || restored.Axis() != axis {
		t.Fatalf("RestoreUnverifiedReceipt() = (%#v, %v)", restored, err)
	}
	if restored.valid() || restored.CanCommit() || restored.Identity().valid() {
		t.Fatal("restored receipt was trusted or authorable before verification")
	}
	if !restored.Verify(epoch) || restored.valid() || restored.CanCommit() {
		t.Fatal("Verify() promoted an unverified receipt")
	}
	verified, ok := restored.VerifyTrusted(epoch)
	if !ok || !verified.valid() || verified.CanCommit() || verified.Axis() != axis {
		t.Fatalf("VerifyTrusted() = (%#v, %t)", verified, ok)
	}
	if verified.Identity() != signedIdentity {
		t.Fatal("verified replay receipt did not retain the signed identity")
	}

	copyRecord := receipt.PersistenceRecord()
	copyRecord.SnapshotRefsJSON[0] = '{'
	*copyRecord.ClosedReason = AbstentionPolicyShadow
	if got := receipt.PersistenceRecord(); string(got.SnapshotRefsJSON) != "[]" || got.ClosedReason == nil || *got.ClosedReason != AbstentionNoCandidates {
		t.Fatalf("PersistenceRecord leaked mutation: %#v", got)
	}
	axisCopy := receipt.Axis()
	axisCopy.channelKey[0] ^= 0x80
	if receipt.Axis().ChannelKey() != axis.ChannelKey() {
		t.Fatal("Axis leaked mutation")
	}

	tamperedRecord := receipt.PersistenceRecord()
	tamperedRecord.ContentCommitment[0] ^= 0x80
	tampered, err := RestoreUnverifiedReceipt(tamperedRecord)
	if err != nil {
		t.Fatalf("RestoreUnverifiedReceipt(tampered) error = %v", err)
	}
	if tampered.valid() || tampered.CanCommit() || tampered.Identity().valid() || tampered.Verify(epoch) {
		t.Fatal("tampered receipt gained trust before verification")
	}
	if verifiedTampered, ok := tampered.VerifyTrusted(epoch); ok || verifiedTampered.valid() {
		t.Fatalf("VerifyTrusted(tampered) = (%#v, %t)", verifiedTampered, ok)
	}

	otherEpoch, err := NewKeyEpoch(testBytes(6), testBytes(7), testBytes(8), testBytes(9), testBytes(10))
	if err != nil {
		t.Fatal(err)
	}
	if verified.Verify(otherEpoch) {
		t.Fatal("receipt verified under a non-current epoch")
	}
}

func TestRestoreUnverifiedReceiptRejectsInvalidPersistenceShapes(t *testing.T) {
	ctx, cancel := receiptTestContext(t)
	defer cancel()
	epoch := fixtureKeyEpoch(t)
	axis := receiptTestAxis(t, epoch)
	receipt := mustNoCandidatesReceipt(t, ctx, epoch, axis, "10000000-0000-4000-8000-000000000003", "10000000-0000-4000-8000-000000000004")

	cases := []struct {
		name   string
		mutate func(*ReceiptPersistenceRecord)
	}{
		{name: "noncanonical project UUID", mutate: func(record *ReceiptPersistenceRecord) { record.CanonicalProject = "project" }},
		{name: "noncanonical snapshot refs", mutate: func(record *ReceiptPersistenceRecord) { record.SnapshotRefsJSON = []byte("[ ]") }},
		{name: "missing abstention reason", mutate: func(record *ReceiptPersistenceRecord) { record.ClosedReason = nil }},
		{name: "selection on abstention", mutate: func(record *ReceiptPersistenceRecord) { record.Selection = &ReceiptSelectionRecord{} }},
		{name: "unbounded count", mutate: func(record *ReceiptPersistenceRecord) { record.EvaluatedCount = maxReceiptSnapshotRefs + 1 }},
		{name: "submicrosecond timestamp", mutate: func(record *ReceiptPersistenceRecord) { record.CreatedAt = record.CreatedAt.Add(time.Nanosecond) }},
		{name: "reversed expiry", mutate: func(record *ReceiptPersistenceRecord) { record.ExpiresAt = record.CreatedAt }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			record := receipt.PersistenceRecord()
			testCase.mutate(&record)
			if _, err := RestoreUnverifiedReceipt(record); err == nil {
				t.Fatalf("RestoreUnverifiedReceipt(%#v) succeeded", record)
			}
		})
	}
}

func TestReceiptIntegrityCoversSelectionAndNullableMarkers(t *testing.T) {
	ctx, cancel := receiptTestContext(t)
	defer cancel()
	epoch := fixtureKeyEpoch(t)
	axis := receiptTestAxis(t, epoch)
	receipt := mustEmittedReceipt(t, ctx, epoch, axis, "10000000-0000-4000-8000-000000000005", "10000000-0000-4000-8000-000000000006")
	if !receipt.Verify(epoch) {
		t.Fatal("emitted receipt did not verify")
	}

	tamperedRecord := receipt.PersistenceRecord()
	tamperedSelection, err := NewLearnedInterventionReceiptSelection(
		1,
		1,
		Digest(testBytes(51)),
		"20000000-0000-4000-8000-000000000001",
		1,
		Digest(testBytes(53)),
	)
	if err != nil {
		t.Fatal(err)
	}
	tamperedRecord.Selection = &tamperedSelection
	tampered, err := RestoreUnverifiedReceipt(tamperedRecord)
	if err != nil {
		t.Fatalf("RestoreUnverifiedReceipt(tampered selection) error = %v", err)
	}
	if tampered.Verify(epoch) {
		t.Fatal("selection-tampered receipt verified")
	}

	abstainRecord := receipt.PersistenceRecord()
	reason := AbstentionNoCandidates
	abstainRecord.Outcome = ReceiptOutcomeAbstain
	abstainRecord.ClosedReason = &reason
	abstainRecord.DecisionMode = ReceiptDecisionModeNone
	abstainRecord.EvaluatedCount = 0
	abstainRecord.EligibleCount = 0
	abstainRecord.SnapshotRefsJSON = []byte("[]")
	abstainRecord.Selection = nil
	abstainRecord.IntegrityDigest = [32]byte{}
	abstain, err := signReceipt(epoch, abstainRecord)
	if err != nil || !abstain.Verify(epoch) {
		t.Fatalf("signReceipt(abstain) = (%#v, %v)", abstain, err)
	}
	if receipt.Verify(epoch) && abstain.Identity() == receipt.Identity() {
		t.Fatal("outcome and nullable-field changes retained the original integrity")
	}
}

func TestReceiptSelectionVariantsAreSealedAndModeMatched(t *testing.T) {
	axis := receiptTestAxis(t, fixtureKeyEpoch(t))
	contextSelection, err := NewContextReferenceReceiptSelection(
		1,
		1,
		axis.CanonicalProject(),
		CandidateTierExact,
		Digest(testBytes(71)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, ok := contextSelection.ContextReference(); !ok {
		t.Fatal("context-reference selection lost its exact source projection")
	}
	if _, _, _, _, _, _, ok := contextSelection.LearnedIntervention(); ok {
		t.Fatal("context-reference selection exposed learned-policy fields")
	}

	learnedSelection, err := NewLearnedInterventionReceiptSelection(
		1,
		1,
		Digest(testBytes(72)),
		"20000000-0000-4000-8000-000000000001",
		1,
		Digest(testBytes(73)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, _, _, ok := learnedSelection.LearnedIntervention(); !ok {
		t.Fatal("learned selection lost its policy projection")
	}
	if _, _, _, _, _, ok := learnedSelection.ContextReference(); ok {
		t.Fatal("learned selection exposed context-reference fields")
	}

	ctx, cancel := receiptTestContext(t)
	defer cancel()
	record := mustEmittedReceipt(t, ctx, fixtureKeyEpoch(t), axis, "10000000-0000-4000-8000-000000000007", "10000000-0000-4000-8000-000000000008").PersistenceRecord()
	record.Selection = &contextSelection
	if _, err := RestoreUnverifiedReceipt(record); err == nil {
		t.Fatal("learned decision mode accepted a context-reference selection")
	}
}

func receiptTestContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithDeadline(context.Background(), interventionTestTime.Add(time.Second))
}

func receiptTestAxis(t *testing.T, epoch KeyEpoch) ReceiptAxis {
	t.Helper()
	axis, err := NewReceiptAxis(epoch, fixtureBindingFacts(), receiptTestAuthority(t), fixtureOccurrence(t, "receipt query", nil))
	if err != nil {
		t.Fatalf("NewReceiptAxis() error = %v", err)
	}
	return axis
}

func receiptTestAuthority(t *testing.T) taskmemory.AuthorizedTaskContext {
	t.Helper()
	caller, err := taskmemory.NewAuthenticatedCaller("client", "read-write", "workstation-1", "agent-1", "agent", nil)
	if err != nil {
		t.Fatal(err)
	}
	project, err := projectidentity.NewProjectKeyV3("00000000-0000-4000-8000-000000000001")
	if err != nil {
		t.Fatal(err)
	}
	correlation, err := projectidentity.NewCorrelationV3("receipt-test-correlation")
	if err != nil {
		t.Fatal(err)
	}
	resolution, err := projectidentity.NewSuccessResultV3(
		projectidentity.ReadFilterIntentV3,
		projectidentity.ProjectResolvedOutcomeV3,
		project,
		projectidentity.RepositoryResolvedScopeV3,
		"",
		correlation,
	)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := taskmemory.NewAuthorizedTaskContext(caller, resolution, "authority-session")
	if err != nil {
		t.Fatal(err)
	}
	return authority
}

func mustNoCandidatesReceipt(t *testing.T, ctx context.Context, epoch KeyEpoch, axis ReceiptAxis, receiptID, operationID string) Receipt {
	t.Helper()
	receipt, err := NewNoCandidatesReceipt(ctx, epoch, axis, receiptID, operationID, interventionTestTime)
	if err != nil {
		t.Fatalf("NewNoCandidatesReceipt() error = %v", err)
	}
	return receipt
}

func mustEmittedReceipt(t *testing.T, ctx context.Context, epoch KeyEpoch, axis ReceiptAxis, receiptID, operationID string) Receipt {
	t.Helper()
	record := mustNoCandidatesReceipt(t, ctx, epoch, axis, receiptID, operationID).PersistenceRecord()
	record.IntegrityDigest = [32]byte{}
	record.Outcome = ReceiptOutcomeEmit
	record.ClosedReason = nil
	record.DecisionMode = ReceiptDecisionModeEligible
	record.EvaluatedCount = 1
	record.EligibleCount = 1
	record.SnapshotRefsJSON = []byte(`[{"snapshot_id":"20000000-0000-4000-8000-000000000001","snapshot_version":1}]`)
	selection, err := NewLearnedInterventionReceiptSelection(
		1,
		1,
		Digest(testBytes(51)),
		"20000000-0000-4000-8000-000000000001",
		1,
		Digest(testBytes(52)),
	)
	if err != nil {
		t.Fatalf("NewLearnedInterventionReceiptSelection() error = %v", err)
	}
	record.Selection = &selection
	receipt, err := signReceipt(epoch, record)
	if err != nil {
		t.Fatalf("signReceipt() error = %v", err)
	}
	return receipt
}
