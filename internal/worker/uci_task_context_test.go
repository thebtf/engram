package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/mcp"
	"github.com/thebtf/engram/internal/taskcontext"
	"github.com/thebtf/engram/internal/uci"
)

func TestUCITaskContextPacketIsReleasedFromOneReadOnlyView(t *testing.T) {
	t.Setenv("ENGRAM_CODE_INTEL_ENABLED", "true")
	store := openWorkerUCIContextCompositionStore(t)
	server := mcp.NewServer(mcp.ServerOptions{Version: "native-task-context-fixture"})
	composition, err := composeUCIContext(true, store.GetDB(), server, workerUCISemanticConfig())
	require.NoError(t, err)
	fixture := newWorkerUCIApplicationFixture(t, composition)
	provider := &taskContextProviderSpy{}
	composition.application.semanticService = uci.NewSemanticService(uci.VectorProfile{}, provider, composition.projectionStore, composition.projectionStore, composition.projectionStore)
	project := uuid.NewString()
	require.NoError(t, store.GetDB().Exec("INSERT INTO projects (id, display_name) VALUES (?, ?)", project, "Task packet fixture").Error)
	require.NoError(t, store.GetDB().Model(&gormstore.UCISource{}).Where("source_id = ?", fixture.source.SourceID).Update("project_id", project).Error)
	require.NoError(t, store.GetDB().Model(&gormstore.UCICheckout{}).Where("checkout_id = ?", fixture.checkout.CheckoutID).Update("registration_profile_id", fixture.profile.ProfileID).Error)
	identity := auth.ClientWithPrincipal("read-write", fixture.workstationID, fixture.principal, auth.PrincipalKindAgent)
	caller := auth.WithIdentity(mcp.ContextWithProject(mcp.ContextWithSession(context.Background(), fixture.clientSessionID), project), identity)
	caller = taskcontext.WithNativeLocator(caller, fixture.checkout.LocatorRef)
	before, err := composition.contextStore.GetCheckout(context.Background(), fixture.checkout.CheckoutID)
	require.NoError(t, err)
	exposures := workerUCIApplicationExposureCount(t, store.GetDB())
	invoke := func(ctx context.Context, query string, budget int) taskcontext.Response {
		t.Helper()
		raw, err := json.Marshal(taskcontext.Request{Schema: 1, HostSessionID: "actual-host-session", Query: query, DeadlineUnixMS: time.Now().Add(300 * time.Millisecond).UnixMilli(), MaxBytes: budget})
		require.NoError(t, err)
		var args map[string]any
		require.NoError(t, json.Unmarshal(raw, &args))
		response := workerUCIApplicationToolResponse(t, server, ctx, "codebase_task_context", args)
		text := workerUCIApplicationToolText(t, response)
		require.LessOrEqual(t, len(text)+1, budget)
		var packet taskcontext.Response
		require.NoError(t, json.Unmarshal([]byte(text), &packet))
		return packet
	}
	packet := invoke(caller, "Alpha", 8000)
	require.Equal(t, "ok", packet.Status, "packet=%+v", packet)
	require.NotNil(t, packet.Packet)
	require.NotNil(t, packet.Packet.Source)
	require.NotNil(t, packet.Packet.Calls)
	for _, part := range []uci.QueryResponse{packet.Packet.Search, *packet.Packet.Source, *packet.Packet.Calls} {
		require.NoError(t, part.Validate())
		require.NotNil(t, part.Exposure)
		require.Equal(t, fixture.current.Context.ViewID, (*part.Contexts)[0].ViewID)
	}
	require.Contains(t, (*packet.Packet.Source.Items)[0].Excerpt, "current-published-body")
	require.NotEmpty(t, packet.Packet.Calls.Graph.Edges)
	require.Equal(t, exposures+3, workerUCIApplicationExposureCount(t, store.GetDB()))
	_, selected := composition.resolver.BoundSelector(fixture.clientSessionID)
	require.False(t, selected, "task context installed a default binding")
	after, err := composition.contextStore.GetCheckout(context.Background(), fixture.checkout.CheckoutID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	missing := taskcontext.WithNativeLocator(caller, "file:///unregistered/task-worktree")
	require.Equal(t, "context_required", invoke(missing, "Alpha", 8000).Status)
	oversize := invoke(caller, "Alpha", 1024)
	require.Equal(t, "unavailable", oversize.Status)
	require.Equal(t, "BUDGET_EXCEEDED", oversize.ReasonCode)
	require.Nil(t, oversize.Packet)
	cancelled, cancel := context.WithCancel(caller)
	cancel()
	require.Equal(t, "deadline_exceeded", invoke(cancelled, "Alpha", 8000).Status)
	require.Equal(t, "ok", invoke(caller, "Alpha", 8000).Status, "cancelled request damaged shared application")
	withoutNative := auth.WithIdentity(mcp.ContextWithProject(mcp.ContextWithSession(context.Background(), "unbound-native"), project), identity)
	require.Equal(t, "forbidden", invoke(withoutNative, "Alpha", 8000).Status)
	empty := invoke(caller, "noSuchSymbol4f1c", 8000)
	require.Equal(t, "empty", empty.Status)
	require.NotNil(t, empty.Packet.Search.Exposure)
	for _, state := range []gormstore.UCICheckoutState{gormstore.UCICheckoutOffline, gormstore.UCICheckoutCatchingUp} {
		require.NoError(t, store.GetDB().Model(&gormstore.UCICheckout{}).Where("checkout_id = ?", fixture.checkout.CheckoutID).Update("state", state).Error)
		require.Equal(t, "stale", invoke(caller, "Alpha", 8000).Status)
	}
	require.NoError(t, store.GetDB().Model(&gormstore.UCICheckout{}).Where("checkout_id = ?", fixture.checkout.CheckoutID).Updates(map[string]any{"state": before.State, "current_view_id": nil}).Error)
	require.Equal(t, "not_ready", invoke(caller, "Alpha", 8000).Status)
	require.NoError(t, store.GetDB().Model(&gormstore.UCICheckout{}).Where("checkout_id = ?", fixture.checkout.CheckoutID).Update("current_view_id", before.CurrentViewID).Error)
	foreign := newWorkerUCIApplicationFixture(t, composition)
	require.NoError(t, store.GetDB().Model(&gormstore.UCISource{}).Where("source_id = ?", foreign.source.SourceID).Update("project_id", project).Error)
	require.NoError(t, store.GetDB().Model(&gormstore.UCICheckout{}).Where("checkout_id = ?", foreign.checkout.CheckoutID).Updates(map[string]any{"locator_ref": fixture.checkout.LocatorRef, "owner_principal": fixture.principal, "workstation_id": fixture.workstationID}).Error)
	ambiguous := invoke(caller, "Alpha", 8000)
	require.Equal(t, "forbidden", ambiguous.Status)
	require.Equal(t, "CONTEXT_MISMATCH", ambiguous.ReasonCode)
	require.NoError(t, store.GetDB().Model(&gormstore.UCICheckout{}).Where("checkout_id = ?", foreign.checkout.CheckoutID).Update("locator_ref", foreign.checkout.LocatorRef).Error)
	beforeFailure := workerUCIApplicationExposureCount(t, store.GetDB())
	server.SetUCIExposureRecorder(nil)
	failedRelease := invoke(caller, "Alpha", 8000)
	require.Equal(t, "unavailable", failedRelease.Status)
	require.Nil(t, failedRelease.Packet)
	require.Equal(t, beforeFailure, workerUCIApplicationExposureCount(t, store.GetDB()))
	server.SetUCIExposureRecorder(composition.exposureRecorder)
	require.Equal(t, "ok", invoke(caller, "Alpha", 8000).Status)
	require.Zero(t, provider.calls.Load(), "deterministic task path entered semantic/embedding provider")
	t.Log("TASK_PACKET_BOUNDARIES_PASS same_view source calls exposure_before_release no_default no_catalog_mutation missing no_view stale ambiguous denied byte_cap provider_calls=0")
}

type taskContextProviderSpy struct{ calls atomic.Int32 }

func (*taskContextProviderSpy) Model() string { return "must-not-run" }
func (spy *taskContextProviderSpy) Embed(context.Context, []string) ([][]float32, error) {
	spy.calls.Add(1)
	return nil, errors.New("task context must not invoke a provider")
}
