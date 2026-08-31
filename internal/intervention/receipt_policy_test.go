package intervention

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPolicyAbstentionReceiptClosedShape(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), interventionTestTime.Add(time.Second))
	defer cancel()
	epoch := fixtureKeyEpoch(t)
	axis := receiptTestAxis(t, epoch)
	for _, reason := range []AbstentionReason{AbstentionPolicyObserving, AbstentionEvidenceInsufficient, AbstentionEvidenceState} {
		t.Run(fmt.Sprintf("reason-%d", reason), func(t *testing.T) {
			receipt, err := NewPolicyAbstentionReceipt(ctx, epoch, axis, "70000000-0000-4000-8000-000000000001", "70000000-0000-4000-8000-000000000002", interventionTestTime, reason, 3)
			if err != nil {
				t.Fatal(err)
			}
			if !receipt.CanCommit() || !receipt.Verify(epoch) {
				t.Fatal("policy receipt was not signed and authorable")
			}
			record := receipt.PersistenceRecord()
			if record.ClosedReason == nil || *record.ClosedReason != reason || record.EvaluatedCount != 3 ||
				record.EligibleCount != 0 || string(record.SnapshotRefsJSON) != "[]" || record.Selection != nil {
				t.Fatalf("record = %#v", record)
			}
		})
	}
}

func TestPolicyAbstentionReceiptRejectsNonT03Shapes(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), interventionTestTime.Add(time.Second))
	defer cancel()
	epoch := fixtureKeyEpoch(t)
	axis := receiptTestAxis(t, epoch)
	for _, testCase := range []struct {
		name      string
		reason    AbstentionReason
		evaluated int
	}{
		{name: "no candidates belongs to T02", reason: AbstentionNoCandidates, evaluated: 1},
		{name: "unsupported reason", reason: AbstentionTaskFit, evaluated: 1},
		{name: "zero evaluation", reason: AbstentionPolicyObserving, evaluated: 0},
		{name: "too many candidates", reason: AbstentionPolicyObserving, evaluated: maxReceiptSnapshotRefs + 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := NewPolicyAbstentionReceipt(ctx, epoch, axis, "70000000-0000-4000-8000-000000000003", "70000000-0000-4000-8000-000000000004", interventionTestTime, testCase.reason, testCase.evaluated); err == nil {
				t.Fatal("invalid policy abstention shape was signed")
			}
		})
	}
}
