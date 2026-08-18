package grpcserver

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thebtf/engram/internal/auth"
	localgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
	pb "github.com/thebtf/engram/proto/engram/v1"
	gormlib "gorm.io/gorm"
)

type sessionStartContinuityFixture struct {
	db          *gormlib.DB
	memoryStore *localgorm.MemoryStore
	slotStore   *localgorm.ContinuitySlotStore
	project     string
}

func newSessionStartContinuityFixture(t *testing.T) sessionStartContinuityFixture {
	t.Helper()
	db, closeDB := openSessionStartTestDB(t)
	t.Cleanup(closeDB)

	fixture := sessionStartContinuityFixture{
		db:          db,
		memoryStore: localgorm.NewMemoryStore(&localgorm.Store{DB: db}),
		slotStore:   localgorm.NewContinuitySlotStore(db),
		project:     fmt.Sprintf("grpc-session-start-continuity-%d", time.Now().UnixNano()),
	}
	fixture.cleanupProject(t, fixture.project)
	return fixture
}

func (f sessionStartContinuityFixture) cleanupProject(t *testing.T, project string) {
	t.Helper()
	t.Cleanup(func() {
		require.NoError(t, f.db.Exec(`DELETE FROM project_continuity_slots WHERE project = ? OR memory_id IN (SELECT id FROM memories WHERE project = ?)`, project, project).Error)
		require.NoError(t, f.db.Exec(`DELETE FROM memories WHERE project = ?`, project).Error)
	})
}

func (f sessionStartContinuityFixture) createMemory(t *testing.T, project, content string, mutate func(*models.Memory)) *models.Memory {
	t.Helper()
	memory := &models.Memory{
		Project:            project,
		Content:            content,
		Status:             "active",
		AgentVisibility:    models.AgentVisibilityShared,
		OwnerPrincipal:     "agent/continuity-test",
		OwnerPrincipalKind: "agent",
		SourceAgent:        "continuity-test",
		EditedBy:           project,
	}
	if mutate != nil {
		mutate(memory)
	}
	created, err := f.memoryStore.Create(context.Background(), memory)
	require.NoError(t, err)
	return created
}

func (f sessionStartContinuityFixture) reserve(t *testing.T, target *models.Memory, expiresAt time.Time) {
	t.Helper()
	require.NoError(t, f.slotStore.Upsert(context.Background(), localgorm.ContinuitySlot{
		Project:                     f.project,
		MemoryID:                    target.ID,
		ExpiresAt:                   expiresAt,
		AuthorityDomain:             target.Domain,
		AuthorityOwnerPrincipal:     target.OwnerPrincipal,
		AuthorityOwnerPrincipalKind: target.OwnerPrincipalKind,
	}))
}

func (f sessionStartContinuityFixture) sessionStart(t *testing.T, ctx context.Context, limit int32) *pb.GetSessionStartContextResponse {
	t.Helper()
	response, err := (&Server{db: f.db}).GetSessionStartContext(ctx, &pb.GetSessionStartContextRequest{
		Project:       f.project,
		MemoriesLimit: limit,
	})
	require.NoError(t, err)
	return response
}

func enableSessionStartContinuity(t *testing.T) {
	t.Helper()
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
}

func TestGetSessionStartContext_ContinuitySlotReservesFirstWithoutDuplicate(t *testing.T) {
	enableSessionStartContinuity(t)
	fixture := newSessionStartContinuityFixture(t)
	target := fixture.createMemory(t, fixture.project, "reserved continuity target", nil)
	firstOrdinary := fixture.createMemory(t, fixture.project, "first ordinary candidate", nil)
	secondOrdinary := fixture.createMemory(t, fixture.project, "second ordinary candidate", nil)
	fixture.reserve(t, target, time.Now().UTC().Add(time.Hour))

	response := fixture.sessionStart(t, context.Background(), 3)
	require.Len(t, response.GetMemories(), 3)
	assert.Equal(t, target.ID, response.GetMemories()[0].GetId())

	ids := []int64{response.GetMemories()[0].GetId(), response.GetMemories()[1].GetId(), response.GetMemories()[2].GetId()}
	assert.ElementsMatch(t, []int64{target.ID, firstOrdinary.ID, secondOrdinary.ID}, ids)
	assert.Equal(t, 1, countSessionStartMemoryID(ids, target.ID))
}

func TestGetSessionStartContext_ContinuitySlotUsesTotalLimitOne(t *testing.T) {
	enableSessionStartContinuity(t)
	fixture := newSessionStartContinuityFixture(t)
	target := fixture.createMemory(t, fixture.project, "reserved continuity target", nil)
	fixture.createMemory(t, fixture.project, "ordinary candidate", nil)
	fixture.reserve(t, target, time.Now().UTC().Add(time.Hour))

	response := fixture.sessionStart(t, context.Background(), 1)
	require.Len(t, response.GetMemories(), 1)
	assert.Equal(t, target.ID, response.GetMemories()[0].GetId())
}

func TestGetSessionStartContext_ContinuitySlotUsesDefaultTotalLimit(t *testing.T) {
	enableSessionStartContinuity(t)
	fixture := newSessionStartContinuityFixture(t)
	target := fixture.createMemory(t, fixture.project, "reserved continuity target", nil)
	for i := range defaultSessionStartMemoriesLimit {
		fixture.createMemory(t, fixture.project, fmt.Sprintf("ordinary candidate %d", i), nil)
	}
	fixture.reserve(t, target, time.Now().UTC().Add(time.Hour))

	response := fixture.sessionStart(t, context.Background(), 0)
	require.Len(t, response.GetMemories(), defaultSessionStartMemoriesLimit)
	assert.Equal(t, target.ID, response.GetMemories()[0].GetId())
	ids := make([]int64, 0, len(response.GetMemories()))
	for _, memory := range response.GetMemories() {
		ids = append(ids, memory.GetId())
	}
	assert.Equal(t, 1, countSessionStartMemoryID(ids, target.ID))
}

func TestGetSessionStartContext_ContinuitySlotFetchesTargetBeyondCandidateWindow(t *testing.T) {
	enableSessionStartContinuity(t)
	fixture := newSessionStartContinuityFixture(t)
	target := fixture.createMemory(t, fixture.project, "reserved target outside ordinary window", nil)
	for i := range maxSessionStartMemoriesLimit {
		fixture.createMemory(t, fixture.project, fmt.Sprintf("newer ordinary candidate %d", i), nil)
	}
	fixture.reserve(t, target, time.Now().UTC().Add(time.Hour))

	response := fixture.sessionStart(t, context.Background(), 1)
	require.Len(t, response.GetMemories(), 1)
	assert.Equal(t, target.ID, response.GetMemories()[0].GetId())
}

func TestGetSessionStartContext_ContinuitySlotInvalidTargetsFailOpen(t *testing.T) {
	t.Run("expired slot", func(t *testing.T) {
		enableSessionStartContinuity(t)
		fixture := newSessionStartContinuityFixture(t)
		target := fixture.createMemory(t, fixture.project, "expired slot target", nil)
		for i := range maxSessionStartMemoriesLimit {
			fixture.createMemory(t, fixture.project, fmt.Sprintf("newer ordinary candidate %d", i), nil)
		}
		fixture.reserve(t, target, time.Now().UTC().Add(-time.Hour))

		response := fixture.sessionStart(t, context.Background(), maxSessionStartMemoriesLimit)
		require.Len(t, response.GetMemories(), maxSessionStartMemoriesLimit)
		for _, memory := range response.GetMemories() {
			assert.NotEqual(t, target.ID, memory.GetId())
		}
	})

	t.Run("inactive target", func(t *testing.T) {
		enableSessionStartContinuity(t)
		fixture := newSessionStartContinuityFixture(t)
		target := fixture.createMemory(t, fixture.project, "inactive slot target", nil)
		ordinary := fixture.createMemory(t, fixture.project, "ordinary candidate", nil)
		fixture.reserve(t, target, time.Now().UTC().Add(time.Hour))
		require.NoError(t, fixture.db.Exec(`UPDATE memories SET status = 'archived' WHERE id = ?`, target.ID).Error)

		response := fixture.sessionStart(t, context.Background(), 1)
		require.Len(t, response.GetMemories(), 1)
		assert.Equal(t, ordinary.ID, response.GetMemories()[0].GetId())
	})

	t.Run("not current target", func(t *testing.T) {
		enableSessionStartContinuity(t)
		fixture := newSessionStartContinuityFixture(t)
		target := fixture.createMemory(t, fixture.project, "expired validity target", nil)
		ordinary := fixture.createMemory(t, fixture.project, "ordinary candidate", nil)
		fixture.reserve(t, target, time.Now().UTC().Add(time.Hour))
		require.NoError(t, fixture.db.Exec(`UPDATE memories SET valid_until = ? WHERE id = ?`, time.Now().UTC().Add(-time.Hour), target.ID).Error)

		response := fixture.sessionStart(t, context.Background(), 1)
		require.Len(t, response.GetMemories(), 1)
		assert.Equal(t, ordinary.ID, response.GetMemories()[0].GetId())
	})

	t.Run("cross project target", func(t *testing.T) {
		enableSessionStartContinuity(t)
		fixture := newSessionStartContinuityFixture(t)
		otherProject := fixture.project + "-other"
		fixture.cleanupProject(t, otherProject)
		target := fixture.createMemory(t, otherProject, "cross project slot target", nil)
		ordinary := fixture.createMemory(t, fixture.project, "ordinary candidate", nil)
		fixture.reserve(t, target, time.Now().UTC().Add(time.Hour))

		response := fixture.sessionStart(t, context.Background(), 1)
		require.Len(t, response.GetMemories(), 1)
		assert.Equal(t, ordinary.ID, response.GetMemories()[0].GetId())
	})

	t.Run("invisible target", func(t *testing.T) {
		enableSessionStartContinuity(t)
		fixture := newSessionStartContinuityFixture(t)
		target := fixture.createMemory(t, fixture.project, "private slot target", func(memory *models.Memory) {
			memory.OwnerPrincipal = "agent/alice"
			memory.OwnerPrincipalKind = "agent"
			memory.AgentVisibility = models.AgentVisibilityPrivate
		})
		ordinary := fixture.createMemory(t, fixture.project, "ordinary candidate", nil)
		fixture.reserve(t, target, time.Now().UTC().Add(time.Hour))
		caller := auth.ClientWithPrincipal("read-write", "keycard-bob", "agent/bob", auth.PrincipalKindAgent)

		response := fixture.sessionStart(t, auth.WithIdentity(context.Background(), caller), 1)
		require.Len(t, response.GetMemories(), 1)
		assert.Equal(t, ordinary.ID, response.GetMemories()[0].GetId())
	})
}

func TestGetSessionStartContext_ContinuitySlotFlagOffPreservesOrdinarySelection(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "true")
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	fixture := newSessionStartContinuityFixture(t)
	target := fixture.createMemory(t, fixture.project, "reserved target while flag off", nil)
	for i := range maxSessionStartMemoriesLimit {
		fixture.createMemory(t, fixture.project, fmt.Sprintf("newer ordinary candidate %d", i), nil)
	}
	fixture.reserve(t, target, time.Now().UTC().Add(time.Hour))

	response := fixture.sessionStart(t, context.Background(), 1)
	require.Len(t, response.GetMemories(), 1)
	assert.NotEqual(t, target.ID, response.GetMemories()[0].GetId())
}

func TestGetSessionStartContext_ContinuitySlotDoesNotChangeNonVNextSelection(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_ENABLED", "")
	t.Setenv("ENGRAM_CONTINUITY_SLOT_ENABLED", "true")
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")
	fixture := newSessionStartContinuityFixture(t)
	target := fixture.createMemory(t, fixture.project, "reserved target outside vnext", nil)
	ordinary := fixture.createMemory(t, fixture.project, "newer ordinary candidate", nil)
	fixture.reserve(t, target, time.Now().UTC().Add(time.Hour))

	response := fixture.sessionStart(t, context.Background(), 1)
	require.Len(t, response.GetMemories(), 1)
	assert.Equal(t, ordinary.ID, response.GetMemories()[0].GetId())
}

func countSessionStartMemoryID(ids []int64, targetID int64) int {
	count := 0
	for _, id := range ids {
		if id == targetID {
			count++
		}
	}
	return count
}
