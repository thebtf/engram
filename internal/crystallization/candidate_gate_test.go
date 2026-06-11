// Package crystallization — unit tests for RouteDecision flag-flip ON-direction guard.
// Uses in-memory mocks so no database connection is required.
package crystallization

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/pkg/models"
)

// --- mock implementations ---

type mockCandidateWriter struct {
	existing    *models.CrystallizationCandidate // returned by GetByFingerprint when non-nil
	created     *models.CrystallizationCandidate // returned by Create
	createCalls int
}

func (m *mockCandidateWriter) Create(_ context.Context, c *models.CrystallizationCandidate) (*models.CrystallizationCandidate, error) {
	m.createCalls++
	if m.created != nil {
		return m.created, nil
	}
	c.ID = 42
	return c, nil
}

func (m *mockCandidateWriter) GetByFingerprint(_ context.Context, _ string) (*models.CrystallizationCandidate, error) {
	return m.existing, nil
}

type mockMemChecker struct {
	memories []*models.Memory // returned when tag matches
	tagSeen  string
}

func (m *mockMemChecker) ListBySourceAgentAndTag(_ context.Context, _, _, tag string) ([]*models.Memory, error) {
	m.tagSeen = tag
	return m.memories, nil
}

// --- tests ---

// TestRouteDecision_FlagOn_NoDuplicate_CreatesCandidate verifies the happy path:
// flag ON, no existing candidate or memory → new candidate created.
func TestRouteDecision_FlagOn_NoDuplicate_CreatesCandidate(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	writer := &mockCandidateWriter{}
	checker := &mockMemChecker{}

	result, err := RouteDecision(
		context.Background(),
		ExtractedDecision{Text: "decided to use Redis for caching"},
		"sess-001",
		"proj-A",
		writer,
		checker,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.UsedCandidatePath)
	assert.False(t, result.Duplicate)
	assert.Equal(t, 1, writer.createCalls, "Create must be called once on cache miss")
}

// TestRouteDecision_FlagOn_PendingCandidateExists_ReturnsDuplicate verifies that
// an existing pending candidate with the same fingerprint triggers Duplicate=true.
func TestRouteDecision_FlagOn_PendingCandidateExists_ReturnsDuplicate(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	writer := &mockCandidateWriter{
		existing: &models.CrystallizationCandidate{ID: 7, Status: models.CandidateStatusPending},
	}
	checker := &mockMemChecker{}

	result, err := RouteDecision(
		context.Background(),
		ExtractedDecision{Text: "decided to use Redis for caching"},
		"sess-001",
		"proj-A",
		writer,
		checker,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Duplicate)
	assert.Equal(t, 0, writer.createCalls, "Create must NOT be called when pending candidate exists")
}

// TestRouteDecision_FlagOn_MemoryWithFpTagExists_ReturnsDuplicate verifies
// MAJOR finding 4 — flag-flip ON-direction guard:
// when a memory with fp:<fingerprint> tag already exists (written via legacy path
// while flag was OFF), RouteDecision must return Duplicate=true and not create a
// new pending candidate.
func TestRouteDecision_FlagOn_MemoryWithFpTagExists_ReturnsDuplicate(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	// No pending candidate exists.
	writer := &mockCandidateWriter{}
	// But a memory with the fp-tag does exist (legacy path wrote it while flag was OFF).
	checker := &mockMemChecker{
		memories: []*models.Memory{{ID: 999}},
	}

	result, err := RouteDecision(
		context.Background(),
		ExtractedDecision{Text: "decided to use Redis for caching"},
		"sess-001",
		"proj-A",
		writer,
		checker,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.True(t, result.Duplicate,
		"existing fp-tag memory must trigger Duplicate=true (flag-flip ON guard)")
	assert.Equal(t, 0, writer.createCalls,
		"Create must NOT be called when memory with fp-tag already exists")
	// Verify the fp-tag format matches what the legacy path stores.
	assert.True(t, len(checker.tagSeen) > 3 && checker.tagSeen[:3] == "fp:",
		"memChecker must be called with a fp: tag prefix, got: %q", checker.tagSeen)
}

// TestRouteDecision_FlagOn_NilMemChecker_SkipsMemoryCheck verifies backward
// compatibility: nil memChecker must not panic and must continue to create the candidate.
func TestRouteDecision_FlagOn_NilMemChecker_SkipsMemoryCheck(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	writer := &mockCandidateWriter{}

	result, err := RouteDecision(
		context.Background(),
		ExtractedDecision{Text: "decided to use Redis for caching"},
		"sess-001",
		"proj-A",
		writer,
		nil, // backward-compatible nil checker
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.False(t, result.Duplicate)
	assert.Equal(t, 1, writer.createCalls)
}

// TestFingerprintUnification_CandidateMatchesLegacyFormat verifies MAJOR finding 4:
// the candidate fingerprint format (16-char hex, sha256(session+":"+content)[:16])
// matches the legacy crystallizationFingerprint() algorithm so that fp-tag values
// stored in memories are directly comparable with candidate fingerprint column values.
//
// This is tested at the models layer by verifying the shape of the computed fingerprint
// matches what handlers_hooks.go would produce (16 lowercase hex chars, colon separator).
func TestFingerprintUnification_CandidateMatchesLegacyFormat(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "true")

	sessID := "sess-unify-001"
	content := "decided to use event sourcing for audit trail"

	c, err := models.NewCrystallizationCandidate(
		sessID,
		content,
		"rule",
		models.CandidateOptions{},
	)
	require.NoError(t, err)

	fp := c.Fingerprint
	require.Len(t, fp, 16, "unified fingerprint must be 16 hex chars (matching legacy algo)")
	require.Regexp(t, `^[0-9a-f]{16}$`, fp, "fingerprint must be lowercase hex")

	// The fp-tag format stored in memories is "fp:" + fingerprint.
	// Verify the mock checker would receive exactly that tag.
	writer := &mockCandidateWriter{}
	checker := &mockMemChecker{memories: []*models.Memory{{ID: 1}}}
	_, err = RouteDecision(
		context.Background(),
		ExtractedDecision{Text: content},
		sessID,
		"proj-unify",
		writer,
		checker,
	)
	require.NoError(t, err)
	require.Equal(t, "fp:"+fp, checker.tagSeen,
		"RouteDecision must pass fp:<fingerprint> to the memory checker")
}

// TestRouteDecision_FlagOff_ReturnsNil verifies that flag OFF returns nil (legacy path signal).
func TestRouteDecision_FlagOff_ReturnsNil(t *testing.T) {
	t.Setenv("ENGRAM_VNEXT_F_ENABLED", "false")

	writer := &mockCandidateWriter{}
	result, err := RouteDecision(
		context.Background(),
		ExtractedDecision{Text: "decided to use Redis for caching"},
		"sess-001",
		"proj-A",
		writer,
		nil,
	)
	assert.NoError(t, err)
	assert.Nil(t, result, "flag-OFF must return nil to signal legacy path")
	assert.Equal(t, 0, writer.createCalls)
}
