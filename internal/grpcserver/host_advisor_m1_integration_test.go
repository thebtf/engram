package grpcserver

import (
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/intervention"
	"github.com/thebtf/engram/internal/taskmemory"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	"google.golang.org/protobuf/proto"
)

const (
	m1ReferenceWorkstation = "hap03-m1-workstation"
	m1ReferencePrincipal   = "agent/hap03-m1"
)

func TestHostAdvisorM1ReferenceDelivery(t *testing.T) {
	const referenceText = "Register the retry callback exactly once before the first action."

	t.Run("ordinary authorized memory emits once then becomes delivery ambiguous", func(t *testing.T) {
		store, receiptStore := openT02HostAdvisorStore(t)
		memory, err := gormdb.NewMemoryStore(store).Create(context.Background(), &models.Memory{
			Project:             grpcV3ProjectKey,
			Content:             referenceText,
			Tags:                []string{"retry", "callback"},
			PrivacyScope:        "project",
			SourceWorkstationID: m1ReferenceWorkstation,
			OwnerPrincipal:      m1ReferencePrincipal,
			OwnerPrincipalKind:  "agent",
			AgentVisibility:     models.AgentVisibilityShared,
		})
		require.NoError(t, err)

		client, ctx, bindingID := newM1ReferenceAdvisorClient(t, store, receiptStore)
		request := grpcAdvisorAdviseRequest(bindingID)
		request.Occurrence.BeforeAgentStart.TaskQuery = referenceText

		first, err := client.Advise(ctx, request)
		require.NoError(t, err)
		require.NotNil(t, first.GetEmit(), "an ordinary authorized memory must use reference delivery instead of learned-policy abstention")

		emit := first.GetEmit()
		packet := emit.GetPacket()
		require.NotNil(t, packet)
		knowledge := packet.GetKnowledge()
		require.NotNil(t, knowledge)
		require.EqualValues(t, memory.ID, knowledge.GetMemoryId())
		require.EqualValues(t, memory.Version, knowledge.GetMemoryVersion())
		require.Equal(t, grpcV3ProjectKey, knowledge.GetSourceProject())
		require.Equal(t, pb.HostAdvisorCandidateTier_HOST_ADVISOR_CANDIDATE_TIER_EXACT, knowledge.GetSourceTier())
		expectedDigest := sha256.Sum256([]byte(referenceText))
		require.Equal(t, expectedDigest[:], knowledge.GetTextSha256())
		require.NotNil(t, packet.GetPresentation())
		require.Equal(t, pb.HostAdvisorContextInjectionMode_HOST_ADVISOR_CONTEXT_INJECTION_MODE_HIDDEN_UNTRUSTED_MESSAGE, packet.GetPresentation().GetInjectionMode())
		require.Contains(t, packet.GetPresentation().GetBoundedText(), referenceText)
		require.LessOrEqual(t, len([]byte(packet.GetPresentation().GetBoundedText())), taskmemory.MaxMaterializedExcerptBytes)
		require.LessOrEqual(t, len([]byte(packet.GetPresentation().GetBoundedText())), intervention.MaxReferencePresentationBytes)
		require.NotNil(t, emit.GetReceipt())
		require.NotEmpty(t, emit.GetReceipt().GetReceiptId())
		require.Len(t, emit.GetReceipt().GetIntegritySha256(), 32)
		require.Equal(t, emit.GetReceipt().GetReceiptId(), packet.GetReceipt().GetReceiptId())
		require.Equal(t, emit.GetReceipt().GetIntegritySha256(), packet.GetReceipt().GetIntegritySha256())
		require.EqualValues(t, 1, t02ReceiptCount(t, store))
		var persisted struct {
			DecisionMode          string `gorm:"column:decision_mode"`
			SelectedMemoryID      int64  `gorm:"column:selected_memory_id"`
			SelectedMemoryVersion int    `gorm:"column:selected_memory_version"`
			SelectedSourceProject string `gorm:"column:selected_source_project"`
			SelectedSourceTier    int    `gorm:"column:selected_source_tier"`
			SelectedTextDigest    []byte `gorm:"column:selected_text_digest"`
		}
		require.NoError(t, store.GetDB().Table("task_memory_intervention_receipts").Select("decision_mode, selected_memory_id, selected_memory_version, selected_source_project, selected_source_tier, selected_text_digest").Where("receipt_id = ?", emit.GetReceipt().GetReceiptId()).Scan(&persisted).Error)
		require.Equal(t, "context_reference", persisted.DecisionMode)
		require.EqualValues(t, memory.ID, persisted.SelectedMemoryID)
		require.EqualValues(t, memory.Version, persisted.SelectedMemoryVersion)
		require.Equal(t, grpcV3ProjectKey, persisted.SelectedSourceProject)
		require.EqualValues(t, taskmemory.CandidateExact, persisted.SelectedSourceTier)
		require.Equal(t, expectedDigest[:], persisted.SelectedTextDigest)
		var legacySelectionFields int
		require.NoError(t, store.GetDB().Raw(`
			SELECT COUNT(*)
			FROM task_memory_intervention_receipts
			WHERE receipt_id = ?
			  AND (selected_policy_id IS NOT NULL OR selected_snapshot_id IS NOT NULL OR selected_snapshot_version IS NOT NULL)
		`, emit.GetReceipt().GetReceiptId()).Scan(&legacySelectionFields).Error)
		require.Zero(t, legacySelectionFields, "context-reference receipt must not invent learned-policy or snapshot facts")

		replayed, err := client.Advise(ctx, proto.Clone(request).(*pb.HostAdvisorAdviseRequest))
		require.NoError(t, err)
		require.NotNil(t, replayed.GetDeliveryAmbiguous())
		require.Equal(t, emit.GetReceipt().GetReceiptId(), replayed.GetDeliveryAmbiguous().GetReceipt().GetReceiptId())
		require.Equal(t, emit.GetReceipt().GetIntegritySha256(), replayed.GetDeliveryAmbiguous().GetReceipt().GetIntegritySha256())
		require.Nil(t, replayed.GetEmit(), "exact emitted occurrence must not receive a second body")
		require.Nil(t, replayed.GetAbstain())
		require.Nil(t, replayed.GetUnavailable())
		require.EqualValues(t, 1, t02ReceiptCount(t, store))
	})

	t.Run("empty corpus abstains without an emit receipt", func(t *testing.T) {
		store, receiptStore := openT02HostAdvisorStore(t)
		client, ctx, bindingID := newM1ReferenceAdvisorClient(t, store, receiptStore)

		response, err := client.Advise(ctx, grpcAdvisorAdviseRequest(bindingID))
		require.NoError(t, err)
		require.NotNil(t, response.GetAbstain())
		require.Equal(t, pb.HostAdvisorAbstentionReason_HOST_ADVISOR_ABSTENTION_REASON_NO_CANDIDATES, response.GetAbstain().GetReason())
		require.NotEmpty(t, response.GetAbstain().GetReceipt().GetReceiptId())
		require.Nil(t, response.GetEmit())
		require.EqualValues(t, 1, t02ReceiptCount(t, store))
	})
}

func newM1ReferenceAdvisorClient(t *testing.T, store *gormdb.Store, receiptStore *gormdb.InterventionReceiptStore) (pb.EngramServiceClient, context.Context, string) {
	t.Helper()

	keyProvider, _ := t02ExistingVaultKeyProvider(t)
	preparer, err := taskmemory.NewPreparer(taskmemory.PreparerConfig{
		Authority:  &t02AuthorityResolver{},
		Candidates: gormdb.NewTaskMemoryCandidateStore(store),
	})
	require.NoError(t, err)
	runtime, err := intervention.NewRuntimeAdvisor(intervention.RuntimeAdvisorConfig{
		Preparer:     preparer,
		Materializer: gormdb.NewTaskMemoryCandidateStore(store),
		ReceiptStore: receiptStore,
		KeyProvider:  keyProvider,
		Clock:        time.Now,
		NewUUID: func() (string, error) {
			return uuid.NewString(), nil
		},
	})
	require.NoError(t, err)

	profile := grpcAdvisorProfileWithCallbackDeadline(t, 900*time.Millisecond)
	clock := time.Now().UTC()
	registry := grpcAdvisorRegistry(t, profile, &clock)
	rawToken := "engram_ffff666600000000000000000000beef"
	keycard := makeKeycardRow(t, m1ReferenceWorkstation, rawToken, "read-write")
	keycard.Principal = m1ReferencePrincipal
	keycard.PrincipalKind = "agent"
	validator := auth.NewValidator("hap03-m1-master", &stubReader{rows: map[string][]gormdb.APIToken{
		"ffff6666": {keycard},
	}})
	client, stop := newT02HostAdvisorClient(t, validator, registry, runtime)
	t.Cleanup(stop)

	ctx := bearerOutgoingContext(rawToken)
	bound, err := client.Bind(ctx, &pb.HostAdvisorBindRequest{Hello: grpcAdvisorHello(profile, "runtime-one")})
	require.NoError(t, err)
	require.NotEmpty(t, bound.GetBinding().GetBindingId())
	capabilities := bound.GetBinding().GetCapabilitySnapshot().GetCapabilities()
	require.Len(t, capabilities, 1)
	require.Equal(t, []pb.HostAdvisorAction{
		pb.HostAdvisorAction_HOST_ADVISOR_ACTION_ADAPTER_ATTESTATION,
		pb.HostAdvisorAction_HOST_ADVISOR_ACTION_EMIT_CONTEXT_REFERENCE,
	}, capabilities[0].GetAllowedActions(), "M1 bindings must advertise only the closed context-reference action")
	return client, ctx, bound.GetBinding().GetBindingId()
}
