package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auditcontext"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/uci"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	gormlib "gorm.io/gorm"
)

func TestUCICompletionGRPCConcurrentExactRetryHasOneChildAndActorAudit(t *testing.T) {
	for _, schedule := range []struct {
		name           string
		failFirstAudit bool
	}{
		{"both callbacks commit", false},
		{"first audit failure then second callback recovery", true},
	} {
		t.Run(schedule.name, func(t *testing.T) {
			fixture := newWorkerUCICompletionAuthorityFixture(t)
			fixture.requireAuthorized(t, fixture.identityB, fixture.refB)
			request := fixture.request(fixture.projectB, fixture.parentB)
			request.IdempotencyKey = "concurrent-audit-key"
			var initialAuditCount, initialSuccessCount int64
			require.NoError(t, fixture.store.GetDB().Model(&gormstore.AuditLogEntry{}).Count(&initialAuditCount).Error)
			require.NoError(t, fixture.store.GetDB().Model(&gormstore.AuditLogEntry{}).Where("action = ?", "uci_completion_accepted").Count(&initialSuccessCount).Error)
			require.Zero(t, initialSuccessCount)
			firstAuditEntered, releaseFirstAudit := make(chan struct{}), make(chan struct{})
			secondInsertEntered, releaseSecondInsert := make(chan struct{}), make(chan struct{})
			var firstGate, secondGate sync.Once
			var firstCandidateID, secondCandidateID string
			callbackName := "completion-concurrent-retry-" + uuid.NewString()
			firstSession, secondSession := "completion-first-"+uuid.NewString(), "completion-second-"+uuid.NewString()
			callerFor := func(session string) context.Context {
				outgoing, found := metadata.FromOutgoingContext(fixture.callerB)
				require.True(t, found)
				copied := outgoing.Copy()
				copied.Set(auditcontext.SourceSessionMetadataKey, session)
				return metadata.NewOutgoingContext(context.Background(), copied)
			}
			firstCaller, secondCaller := callerFor(firstSession), callerFor(secondSession)
			require.NoError(t, fixture.store.GetDB().Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gormlib.DB) {
				incoming, found := metadata.FromIncomingContext(tx.Statement.Context)
				if !found {
					return
				}
				sessions := incoming.Get(auditcontext.SourceSessionMetadataKey)
				if len(sessions) != 1 {
					return
				}
				if tx.Statement.Table == "uci_completion_evidence" {
					if row, ok := tx.Statement.Dest.(*gormstore.UCICompletionEvidence); ok {
						if sessions[0] == firstSession {
							firstCandidateID = row.CompletionEvidenceID
						}
						if sessions[0] == secondSession {
							secondCandidateID = row.CompletionEvidenceID
						}
					}
				}
				if sessions[0] == firstSession && tx.Statement.Table == "audit_log" {
					firstGate.Do(func() {
						// The first child INSERT has succeeded, but its audit and
						// transaction have not committed. Hold that exact boundary.
						close(firstAuditEntered)
						<-releaseFirstAudit
						if schedule.failFirstAudit {
							_ = tx.AddError(errors.New("synthetic first concurrent completion audit failure"))
						}
					})
				}
				if sessions[0] == secondSession && tx.Statement.Table == "uci_completion_evidence" {
					secondGate.Do(func() {
						// Reaching CREATE proves the real second callback already
						// missed the uncommitted first child in its dedup lookup.
						close(secondInsertEntered)
						<-releaseSecondInsert
					})
				}
			}))

			type callbackResult struct {
				response *pb.RecordUCICompletionResponse
				err      error
			}
			var first, second callbackResult
			firstDone, secondDone := make(chan struct{}), make(chan struct{})
			secondStarted := false
			t.Cleanup(func() {
				for _, release := range []chan struct{}{releaseFirstAudit, releaseSecondInsert} {
					select {
					case <-release:
					default:
						close(release)
					}
				}
				<-firstDone
				if secondStarted {
					<-secondDone
				}
				if err := fixture.store.GetDB().Callback().Create().Remove(callbackName); err != nil {
					t.Errorf("remove concurrent completion gate: %v", err)
				}
			})
			go func() {
				defer close(firstDone)
				first.response, first.err = fixture.client.RecordUCICompletion(firstCaller, proto.Clone(request).(*pb.RecordUCICompletionRequest))
			}()
			select {
			case <-firstAuditEntered:
			case <-firstDone:
				t.Fatalf("first authorized callback did not reach the real audit insert: %v", first.err)
			}
			secondStarted = true
			go func() {
				defer close(secondDone)
				second.response, second.err = fixture.client.RecordUCICompletion(secondCaller, proto.Clone(request).(*pb.RecordUCICompletionRequest))
			}()
			select {
			case <-secondInsertEntered:
			case <-secondDone:
				t.Fatalf("second exact callback did not reach its insert while the first audit was uncommitted: %v", second.err)
			}

			// Both RPCs are now in real transactions. Keep the second paused
			// after its miss so the first commit/rollback deterministically
			// precedes the second INSERT, without sleeps or lock-state polling.
			close(releaseFirstAudit)
			<-firstDone
			if schedule.failFirstAudit {
				require.Nil(t, first.response)
				require.Equal(t, codes.Unavailable, status.Code(first.err))
				require.Equal(t, uci.ErrCompletionEvidenceUnavailable.Error(), status.Convert(first.err).Message())
			} else {
				require.NoError(t, first.err)
				require.NotNil(t, first.response)
				require.True(t, first.response.GetAccepted())
			}
			// Do not request another pooled connection while the second RPC is
			// gated. Candidate IDs below prove rollback after both RPCs finish.
			audits := func() []gormstore.AuditLogEntry {
				var rows []gormstore.AuditLogEntry
				require.NoError(t, fixture.store.GetDB().Where("action = ?", "uci_completion_accepted").Order("id").Find(&rows).Error)
				return rows
			}
			close(releaseSecondInsert)
			<-secondDone
			require.NoError(t, second.err, "the in-flight exact callback must reuse the committed child or recover after its rollback")
			require.NotNil(t, second.response)
			require.True(t, second.response.GetAccepted())

			children := fixture.children(t, fixture.parentB)
			require.Len(t, children, 1)
			require.NotEmpty(t, firstCandidateID)
			require.NotEmpty(t, secondCandidateID)
			require.NotEqual(t, firstCandidateID, secondCandidateID)
			winner, absentCandidate := firstCandidateID, secondCandidateID
			if schedule.failFirstAudit {
				winner, absentCandidate = secondCandidateID, firstCandidateID
			}
			require.Equal(t, winner, children[0].CompletionEvidenceID, "the second callback must recover its own child after the first audit rolls back, or reuse the first committed child")
			var absentCandidateRows int64
			require.NoError(t, fixture.store.GetDB().Model(&gormstore.UCICompletionEvidence{}).Where("completion_evidence_id = ?", absentCandidate).Count(&absentCandidateRows).Error)
			require.Zero(t, absentCandidateRows, "the rolled-back first candidate or conflicting second candidate must not persist")
			accepted := audits()
			require.Len(t, accepted, 1)
			require.Equal(t, fixture.identityB.Principal, accepted[0].Actor)
			require.NotNil(t, accepted[0].AfterState)
			var auditBinding struct {
				ExposureRef          string `json:"exposure_ref"`
				CompletionEvidenceID string `json:"completion_evidence_id"`
				Outcome              string `json:"outcome"`
			}
			require.NoError(t, json.Unmarshal(*accepted[0].AfterState, &auditBinding))
			require.Equal(t, fixture.parentB, auditBinding.ExposureRef)
			require.Equal(t, children[0].CompletionEvidenceID, auditBinding.CompletionEvidenceID)
			require.Equal(t, string(children[0].Outcome), auditBinding.Outcome)
			canonical, err := uci.CanonicalCompletionBindingDigest(uci.CompletionEvidence{
				ExposureRef: fixture.parentB, SupportedHostRef: request.GetSupportedHostRef(), CallbackRef: request.GetCallbackRef(),
				Outcome: uci.CompletionPartial, IdempotencyKey: request.GetIdempotencyKey(),
			})
			require.NoError(t, err)
			require.Equal(t, canonical, children[0].IdempotencyBindingDigest, "concurrent retries must not change semantic hash material")
			var totalChildren, totalAudits int64
			require.NoError(t, fixture.store.GetDB().Model(&gormstore.UCICompletionEvidence{}).Count(&totalChildren).Error)
			require.NoError(t, fixture.store.GetDB().Model(&gormstore.AuditLogEntry{}).Count(&totalAudits).Error)
			require.EqualValues(t, 1, totalChildren)
			require.Equal(t, initialAuditCount+1, totalAudits)

			fixture.accept(t, fixture.callerB, request)
			require.Equal(t, children, fixture.children(t, fixture.parentB))
			require.Equal(t, accepted, audits(), "a later exact retry must preserve the winning child and actor audit")
		})
	}
}
