package worker

import (
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	gormlib "gorm.io/gorm"
)

func TestUCICompletionGRPCServiceOwnerAuditIsTransactionalAndIdempotent(t *testing.T) {
	fixture := newWorkerUCICompletionAuthorityFixture(t)
	var checkout gormstore.UCICheckout
	require.NoError(t, fixture.store.GetDB().Where("checkout_id = ?", fixture.refB.CheckoutID).First(&checkout).Error)
	require.Equal(t, fixture.identityB.Principal, checkout.OwnerPrincipal, "the positive control must be the genuinely registered service owner")
	fixture.requireAuthorized(t, fixture.identityB, fixture.refB)
	audits := func() []gormstore.AuditLogEntry {
		var rows []gormstore.AuditLogEntry
		require.NoError(t, fixture.store.GetDB().Where("action = ?", "uci_completion_accepted").Order("id").Find(&rows).Error)
		return rows
	}
	auditCount := func() int64 {
		var count int64
		require.NoError(t, fixture.store.GetDB().Model(&gormstore.AuditLogEntry{}).Count(&count).Error)
		return count
	}
	require.Empty(t, audits())
	initialAuditCount := auditCount()
	request := fixture.request(fixture.projectB, fixture.parentB)
	fixture.accept(t, fixture.callerB, request)
	children := fixture.children(t, fixture.parentB)
	require.Len(t, children, 1)
	accepted := audits()
	require.Len(t, accepted, 1)
	require.Equal(t, initialAuditCount+1, auditCount())
	require.Equal(t, fixture.identityB.Principal, accepted[0].Actor, "the audit must record the authenticated service, not an impersonated agent owner")
	require.Nil(t, accepted[0].MemoryID)
	require.NotNil(t, accepted[0].AfterState)
	var binding struct {
		ExposureRef          string `json:"exposure_ref"`
		CompletionEvidenceID string `json:"completion_evidence_id"`
		Outcome              string `json:"outcome"`
	}
	require.NoError(t, json.Unmarshal(*accepted[0].AfterState, &binding))
	require.Equal(t, fixture.parentB, binding.ExposureRef)
	require.Equal(t, children[0].CompletionEvidenceID, binding.CompletionEvidenceID)
	require.Equal(t, string(children[0].Outcome), binding.Outcome)

	fixture.accept(t, fixture.callerB, request)
	require.Equal(t, children, fixture.children(t, fixture.parentB), "exact retry must preserve the original child and its canonical binding")
	require.Equal(t, accepted, audits(), "exact retry must not create a second success audit")
	require.Equal(t, initialAuditCount+1, auditCount())

	for _, negative := range []struct {
		name     string
		foreign  bool
		mismatch bool
		wantCode codes.Code
	}{
		{"foreign exact replay", true, false, codes.PermissionDenied},
		{"foreign mismatch disclosure", true, true, codes.PermissionDenied},
		{"authorized mismatch", false, true, codes.FailedPrecondition},
	} {
		t.Run(negative.name, func(t *testing.T) {
			changed := proto.Clone(request).(*pb.RecordUCICompletionRequest)
			caller := fixture.callerB
			if negative.foreign {
				caller = fixture.callerA
				changed.CanonicalProject = fixture.projectA
			}
			if negative.mismatch {
				changed.CallbackRef = "changed-retained-callback"
				changed.Outcome = pb.UCICompletionOutcome_UCI_COMPLETION_OUTCOME_FAILED
			}
			response, callErr := fixture.client.RecordUCICompletion(caller, changed)
			require.Nil(t, response)
			require.Equal(t, negative.wantCode, status.Code(callErr))
			for _, forbidden := range []string{fixture.parentB, fixture.refB.SourceID, fixture.refB.CheckoutID, fixture.refB.ViewID, children[0].CompletionEvidenceID} {
				require.NotContains(t, status.Convert(callErr).Proto().String(), forbidden)
			}
			require.Equal(t, children, fixture.children(t, fixture.parentB))
			require.Equal(t, accepted, audits())
			require.Equal(t, initialAuditCount+1, auditCount(), "denial/mismatch must create neither child nor audit")
		})
	}

	var failAudit, faultSeen atomic.Bool
	failAudit.Store(true)
	callbackName := "completion-audit-fault-" + uuid.NewString()
	require.NoError(t, fixture.store.GetDB().Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gormlib.DB) {
		if tx.Statement.Table == "audit_log" && failAudit.Load() {
			faultSeen.Store(true)
			_ = tx.AddError(errors.New("synthetic completion audit failure"))
		}
	}))
	t.Cleanup(func() {
		if err := fixture.store.GetDB().Callback().Create().Remove(callbackName); err != nil {
			t.Errorf("remove completion audit fault: %v", err)
		}
	})
	newCallback := proto.Clone(request).(*pb.RecordUCICompletionRequest)
	newCallback.CallbackRef = "audit-rollback-callback"
	newCallback.IdempotencyKey = "audit-rollback-key"
	response, callErr := fixture.client.RecordUCICompletion(fixture.callerB, newCallback)
	require.True(t, faultSeen.Load(), "the real audit owner must be reached after the child insert")
	require.Nil(t, response)
	require.Equal(t, codes.Unavailable, status.Code(callErr))
	require.Equal(t, uci.ErrCompletionEvidenceUnavailable.Error(), status.Convert(callErr).Message())
	require.Equal(t, children, fixture.children(t, fixture.parentB), "an audit failure must roll back the new child in the same transaction")
	require.Equal(t, accepted, audits())
	require.Equal(t, initialAuditCount+1, auditCount())

	failAudit.Store(false)
	fixture.accept(t, fixture.callerB, newCallback)
	require.Len(t, fixture.children(t, fixture.parentB), 2)
	recovered := audits()
	require.Len(t, recovered, 2)
	require.Equal(t, fixture.identityB.Principal, recovered[1].Actor)
	require.Equal(t, initialAuditCount+2, auditCount())
	fixture.accept(t, fixture.callerB, newCallback)
	require.Len(t, fixture.children(t, fixture.parentB), 2)
	require.Equal(t, recovered, audits(), "retry after recovery must not create a duplicate audit")
	require.Equal(t, initialAuditCount+2, auditCount())
}
