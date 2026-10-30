package worker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestUCICompletionGRPCLockedForeignContextIsIndistinguishableFromAbsent(t *testing.T) {
	fixture := newWorkerUCICompletionAuthorityFixture(t)
	fixture.requireUnauthorized(t, fixture.identityB, fixture.refA)
	fixture.requireAuthorized(t, fixture.identityA, fixture.refA)
	fixture.accept(t, fixture.callerB, fixture.request(fixture.projectB, fixture.parentB))
	fixture.accept(t, fixture.callerB, fixture.request(fixture.projectB, fixture.parentB))
	controlChildren := fixture.children(t, fixture.parentB)
	require.Len(t, controlChildren, 1, "the real service/interceptor/persistence control must succeed before testing foreign lock states")

	successAudits := func() []gormstore.AuditLogEntry {
		var rows []gormstore.AuditLogEntry
		require.NoError(t, fixture.store.GetDB().Where("action = ?", "uci_completion_accepted").Order("id").Find(&rows).Error)
		return rows
	}
	auditCount := func() int64 {
		var count int64
		require.NoError(t, fixture.store.GetDB().Model(&gormstore.AuditLogEntry{}).Count(&count).Error)
		return count
	}
	baselineAudits := successAudits()
	require.Len(t, baselineAudits, 1)
	require.Equal(t, fixture.identityB.Principal, baselineAudits[0].Actor)
	baselineAuditCount := auditCount()
	absent := uci.NewExposureRef()
	require.True(t, uci.ValidExposureRef(absent))
	var absentRows int64
	require.NoError(t, fixture.store.GetDB().Model(&gormstore.UCIExposure{}).Where("exposure_ref = ?", absent).Count(&absentRows).Error)
	require.Zero(t, absentRows, "the absent control must be a valid but genuinely nonexistent exposure")

	for _, locked := range []struct {
		name, query, key string
	}{
		{"parent", "SELECT exposure_id FROM uci_exposures WHERE exposure_ref = ? FOR UPDATE", fixture.parentA},
		{"checkout", "SELECT checkout_id FROM ci_checkouts WHERE checkout_id = ? FOR UPDATE", fixture.refA.CheckoutID},
		{"source", "SELECT source_id FROM sources WHERE source_id = ? FOR UPDATE", fixture.refA.SourceID},
		{"View", "SELECT view_id FROM ci_views WHERE view_id = ? FOR UPDATE", fixture.refA.ViewID},
	} {
		t.Run(locked.name, func(t *testing.T) {
			lockTx := fixture.store.GetDB().Begin()
			require.NoError(t, lockTx.Error)
			released := false
			t.Cleanup(func() {
				if !released {
					require.NoError(t, lockTx.Rollback().Error)
				}
			})
			var lockedID string
			lockResult := lockTx.Raw(locked.query, locked.key).Scan(&lockedID)
			require.NoError(t, lockResult.Error)
			require.EqualValues(t, 1, lockResult.RowsAffected)
			require.NotEmpty(t, lockedID)

			// The caller owns A, but the row is temporarily locked. This is
			// transient unavailability, not evidence that permission was lost.
			ownerResponse, ownerErr := fixture.client.RecordUCICompletion(fixture.callerA, fixture.request(fixture.projectA, fixture.parentA))
			require.Nil(t, ownerResponse)
			require.Equal(t, codes.Unavailable, status.Code(ownerErr))
			require.Equal(t, uci.ErrCompletionEvidenceUnavailable.Error(), status.Convert(ownerErr).Message())

			foreignResponse, foreignErr := fixture.client.RecordUCICompletion(fixture.callerB, fixture.request(fixture.projectB, fixture.parentA))
			absentResponse, absentErr := fixture.client.RecordUCICompletion(fixture.callerB, fixture.request(fixture.projectB, absent))

			// Check conservation before the expected RED status assertion.
			require.Empty(t, fixture.children(t, fixture.parentA))
			require.Equal(t, controlChildren, fixture.children(t, fixture.parentB))
			require.Equal(t, baselineAudits, successAudits())
			require.Equal(t, baselineAuditCount, auditCount(), "neither denied nor transiently unavailable callbacks may persist an audit")
			require.NoError(t, lockTx.Rollback().Error)
			released = true

			require.Nil(t, foreignResponse)
			require.Nil(t, absentResponse)
			require.Error(t, foreignErr)
			require.Error(t, absentErr)
			assert.Equal(t, codes.PermissionDenied, status.Code(foreignErr), "foreign lock contention must not reveal parent or context existence")
			assert.Equal(t, codes.PermissionDenied, status.Code(absentErr))
			assert.Equal(t, uci.ErrCompletionPermissionDenied.Error(), status.Convert(foreignErr).Message())
			assert.True(t, proto.Equal(status.Convert(absentErr).Proto(), status.Convert(foreignErr).Proto()), "absent and locked foreign contexts must return identical public code/message/details")
			for _, callErr := range []error{foreignErr, absentErr, ownerErr} {
				wire := status.Convert(callErr).Proto().String()
				for _, forbidden := range []string{fixture.parentA, fixture.parentB, absent, fixture.refA.SourceID, fixture.refA.CheckoutID, fixture.refA.ViewID, fixture.identityA.Principal, controlChildren[0].CompletionEvidenceID} {
					assert.NotContains(t, wire, forbidden)
				}
			}
		})
	}

	fixture.requireAuthorized(t, fixture.identityA, fixture.refA)
	fixture.accept(t, fixture.callerA, fixture.request(fixture.projectA, fixture.parentA))
	require.Len(t, fixture.children(t, fixture.parentA), 1, "removing contention must restore the authorized write without changing permissions")
	require.Len(t, successAudits(), 2)
	require.Equal(t, baselineAuditCount+1, auditCount())
	state, err := fixture.exposures.CompletionState(context.Background(), fixture.parentA)
	require.NoError(t, err)
	require.Equal(t, uci.QueryCompletionPartial, state)
}
