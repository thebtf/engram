package gorm

// purge_store_test.go — unit and integration tests for PurgeStore.
//
// Tests that call openTestDB require a live PostgreSQL database via DATABASE_DSN.
// They are skipped automatically when DATABASE_DSN is absent (CI environment).
// To run them locally: set DATABASE_DSN to a valid Postgres DSN before running tests.
// See docs/PRODUCTION-TESTING-PLAYBOOK.md for the full live-DB test workflow.
//
// RESIDUAL RISK (finding 5): the advisory-lock behaviour and RowsAffected-based
// receipt counts are only exercisable against a live Postgres. The pure-unit tests
// below cover validation logic and nil-DB guards; concurrent-writer correctness
// is accepted as a known coverage gap pending CI Postgres infrastructure.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

// TestPurgeStore_PurgeProject_EmptyProject verifies that an empty project name is rejected.
// No DATABASE_DSN required — validation fires before any DB call.
func TestPurgeStore_PurgeProject_EmptyProject(t *testing.T) {
	// openTestDB skips when DATABASE_DSN is absent; use a nil-DB store for pure-validation test.
	ps := &PurgeStore{db: nil}
	_, err := ps.PurgeProject(context.Background(), "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project must not be empty")
}

// TestPurgeStore_PurgeProject_WhitespaceOnlyProject verifies that a whitespace-only
// project name is rejected (trimmed to empty). No DATABASE_DSN required.
func TestPurgeStore_PurgeProject_WhitespaceOnlyProject(t *testing.T) {
	ps := &PurgeStore{db: nil}
	_, err := ps.PurgeProject(context.Background(), "   ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project must not be empty",
		"whitespace-only project must be rejected after trimming")
}

// TestPurgeStore_PurgeProject_TabWhitespace verifies tab+space project name is rejected.
func TestPurgeStore_PurgeProject_TabWhitespace(t *testing.T) {
	ps := &PurgeStore{db: nil}
	_, err := ps.PurgeProject(context.Background(), "\t \n")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "project must not be empty")
}

// TestPurgeStore_PurgeProject_DeletesMemoriesAndRules seeds memories and behavioral rules
// for a project, then verifies that purge hard-deletes them and returns correct counts.
// Requires DATABASE_DSN; skips otherwise.
func TestPurgeStore_PurgeProject_DeletesMemoriesRulesAndAttentionEvents(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	const purgeProj = "test-purge-store"

	// Cleanup any leftover rows from a prior failed run.
	db.Exec(`DELETE FROM attention_events WHERE project = ?`, purgeProj)
	db.Exec(`DELETE FROM memories WHERE project = ?`, purgeProj)
	db.Exec(`DELETE FROM behavioral_rules WHERE project = ?`, purgeProj)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	brs := NewBehavioralRulesStore(store)
	aes := NewAttentionEventStore(db)
	ps := NewPurgeStore(store)
	ctx := context.Background()

	// Seed 3 memories.
	for i := 0; i < 3; i++ {
		_, err := ms.Create(ctx, &models.Memory{
			Project: purgeProj,
			Content: "purge test memory",
		})
		require.NoError(t, err)
	}

	// Seed 2 behavioral rules.
	proj := purgeProj
	for i := 0; i < 2; i++ {
		_, err := brs.Create(ctx, &models.BehavioralRule{
			Project: &proj,
			Content: "purge test rule",
		})
		require.NoError(t, err)
	}

	_, err := aes.Create(ctx, cognitive.AttentionEventRecord{
		Project:        purgeProj,
		SessionID:      "session-purge",
		SourceTurnHash: validAttentionHash("a"),
		DerivedIntent:  "directive policy reporting",
		AgentConfirmed: true,
		Horizon:        "project",
		PrivacyClass:   "internal",
	})
	require.NoError(t, err)

	// Purge.
	receipt, err := ps.PurgeProject(ctx, purgeProj)
	require.NoError(t, err)

	assert.Equal(t, purgeProj, receipt.Project)
	assert.Equal(t, int64(3), receipt.MemoryCount, "receipt must reflect pre-purge memory count")
	assert.Equal(t, int64(2), receipt.RuleCount, "receipt must reflect pre-purge rule count")
	assert.Equal(t, int64(1), receipt.AttentionEventCount, "receipt must count deleted attention_events rows")
	assert.False(t, receipt.PurgedAt.IsZero(), "PurgedAt must be set")

	// Verify memories are gone.
	var count int64
	db.Model(&Memory{}).Where("project = ?", purgeProj).Count(&count)
	assert.Equal(t, int64(0), count, "all memories must be deleted after purge")

	// Verify rules are gone.
	db.Model(&BehavioralRule{}).Where("project = ?", purgeProj).Count(&count)
	assert.Equal(t, int64(0), count, "all rules must be deleted after purge")

	// Verify attention events are gone.
	db.Table("attention_events").Where("project = ?", purgeProj).Count(&count)
	assert.Equal(t, int64(0), count, "all attention_events rows must be deleted after purge")

	// Verify purge audit row was written.
	var auditCount int64
	db.Table("audit_log").Where("action = 'purge'").Count(&auditCount)
	assert.GreaterOrEqual(t, auditCount, int64(1), "at least one purge audit row must exist")
}

// TestPurgeStore_PurgeProject_DoesNotTouchOtherProject verifies that purging project A
// leaves project B untouched.
func TestPurgeStore_PurgeProject_DoesNotTouchOtherProject(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	const purgeProj = "test-purge-isolation-a"
	const safeProj = "test-purge-isolation-b"

	db.Exec(`DELETE FROM attention_events WHERE project IN (?, ?)`, purgeProj, safeProj)
	db.Exec(`DELETE FROM memories WHERE project IN (?, ?)`, purgeProj, safeProj)
	db.Exec(`DELETE FROM behavioral_rules WHERE project IN (?, ?)`, purgeProj, safeProj)
	defer db.Exec(`DELETE FROM attention_events WHERE project IN (?, ?)`, purgeProj, safeProj)
	defer db.Exec(`DELETE FROM memories WHERE project IN (?, ?)`, purgeProj, safeProj)
	defer db.Exec(`DELETE FROM behavioral_rules WHERE project IN (?, ?)`, purgeProj, safeProj)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	aes := NewAttentionEventStore(db)
	ps := NewPurgeStore(store)
	ctx := context.Background()

	// Seed one memory in each project.
	_, err := ms.Create(ctx, &models.Memory{Project: purgeProj, Content: "to be purged"})
	require.NoError(t, err)
	safe, err := ms.Create(ctx, &models.Memory{Project: safeProj, Content: "must survive"})
	require.NoError(t, err)

	_, err = aes.Create(ctx, cognitive.AttentionEventRecord{
		Project:        purgeProj,
		SessionID:      "session-a",
		SourceTurnHash: validAttentionHash("b"),
		DerivedIntent:  "directive policy",
		AgentConfirmed: true,
		Horizon:        "project",
		PrivacyClass:   "internal",
	})
	require.NoError(t, err)
	_, err = aes.Create(ctx, cognitive.AttentionEventRecord{
		Project:        safeProj,
		SessionID:      "session-b",
		SourceTurnHash: validAttentionHash("c"),
		DerivedIntent:  "directive captured",
		AgentConfirmed: true,
		Horizon:        "project",
		PrivacyClass:   "internal",
	})
	require.NoError(t, err)

	// Purge only purgeProj.
	receipt, err := ps.PurgeProject(ctx, purgeProj)
	require.NoError(t, err)
	assert.Equal(t, int64(1), receipt.AttentionEventCount, "purge receipt must only count purged project's attention_events")

	// Safe project memory must still exist.
	var count int64
	db.Model(&Memory{}).Where("project = ?", safeProj).Count(&count)
	assert.Equal(t, int64(1), count, "other project memories must not be deleted")

	// Safe project attention events must still exist.
	db.Table("attention_events").Where("project = ?", purgeProj).Count(&count)
	assert.Equal(t, int64(0), count, "purged project's attention_events must be deleted")
	db.Table("attention_events").Where("project = ?", safeProj).Count(&count)
	assert.Equal(t, int64(1), count, "other project attention_events must not be deleted")

	// Verify via Get too.
	fetched, err := ms.Get(ctx, safe.ID)
	require.NoError(t, err)
	assert.Equal(t, safeProj, fetched.Project)
}

// TestPurgeStore_PurgeProject_DeletesContinuitySlotsByProjectOrTarget verifies
// that the purge transaction clears continuity slots both for the purged
// project and for any slot that targets one of its memories before deleting
// memories. A slot for another project survives unless its target is purged.
func TestPurgeStore_PurgeProject_DeletesContinuitySlotsByProjectOrTarget(t *testing.T) {
	db, closeDB := openTestDB(t)
	t.Cleanup(closeDB)

	ctx := context.Background()
	suffix := time.Now().UnixNano()
	purgeProject := fmt.Sprintf("test-purge-continuity-a-%d", suffix)
	safeProject := fmt.Sprintf("test-purge-continuity-b-%d", suffix)
	targetSlotProject := fmt.Sprintf("test-purge-continuity-target-%d", suffix)
	projectSlotProject := fmt.Sprintf("test-purge-continuity-project-%d", suffix)

	cleanup := func() {
		require.NoError(t, db.Exec(`
			DELETE FROM project_continuity_slots
			WHERE project IN (?, ?, ?, ?)
			   OR memory_id IN (SELECT id FROM memories WHERE project IN (?, ?, ?))`,
			purgeProject, safeProject, targetSlotProject, projectSlotProject,
			purgeProject, safeProject, projectSlotProject,
		).Error)
		require.NoError(t, db.Exec(
			`DELETE FROM memories WHERE project IN (?, ?, ?)`,
			purgeProject, safeProject, projectSlotProject,
		).Error)
	}
	cleanup()
	t.Cleanup(cleanup)

	store := &Store{DB: db}
	memoryStore := NewMemoryStore(store)
	purgeStore := NewPurgeStore(store)
	slotStore := NewContinuitySlotStore(db)

	purgeMemory, err := memoryStore.Create(ctx, &models.Memory{Project: purgeProject, Content: "purge continuity target"})
	require.NoError(t, err)
	safeMemory, err := memoryStore.Create(ctx, &models.Memory{Project: safeProject, Content: "safe continuity target"})
	require.NoError(t, err)
	_, err = memoryStore.Create(ctx, &models.Memory{Project: projectSlotProject, Content: "project-key purge memory"})
	require.NoError(t, err)

	expiresAt := time.Now().UTC().Add(time.Hour)
	for _, slot := range []ContinuitySlot{
		{Project: purgeProject, MemoryID: purgeMemory.ID, ExpiresAt: expiresAt, AuthorityDomain: "test", AuthorityOwnerPrincipal: "agent:test", AuthorityOwnerPrincipalKind: "agent"},
		{Project: targetSlotProject, MemoryID: purgeMemory.ID, ExpiresAt: expiresAt, AuthorityDomain: "test", AuthorityOwnerPrincipal: "agent:test", AuthorityOwnerPrincipalKind: "agent"},
		{Project: safeProject, MemoryID: safeMemory.ID, ExpiresAt: expiresAt, AuthorityDomain: "test", AuthorityOwnerPrincipal: "agent:test", AuthorityOwnerPrincipalKind: "agent"},
		{Project: projectSlotProject, MemoryID: safeMemory.ID, ExpiresAt: expiresAt, AuthorityDomain: "test", AuthorityOwnerPrincipal: "agent:test", AuthorityOwnerPrincipalKind: "agent"},
	} {
		require.NoError(t, slotStore.Upsert(ctx, slot))
	}

	receipt, err := purgeStore.PurgeProject(ctx, purgeProject)
	require.NoError(t, err)
	assert.Equal(t, int64(1), receipt.MemoryCount)

	var count int64
	require.NoError(t, db.Model(&Memory{}).Where("project = ?", purgeProject).Count(&count).Error)
	assert.Zero(t, count, "purged project memories must be deleted")
	require.NoError(t, db.Model(&ContinuitySlot{}).Where("project IN ?", []string{purgeProject, targetSlotProject}).Count(&count).Error)
	assert.Zero(t, count, "slots for the project or its memory target must be deleted")

	_, err = purgeStore.PurgeProject(ctx, projectSlotProject)
	require.NoError(t, err)
	require.NoError(t, db.Model(&ContinuitySlot{}).Where("project = ?", projectSlotProject).Count(&count).Error)
	assert.Zero(t, count, "a slot for the purged project must be deleted even when it targets another project")

	require.NoError(t, db.Model(&Memory{}).Where("id = ?", safeMemory.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "unrelated project memory must survive")
	var safeSlot ContinuitySlot
	require.NoError(t, db.Where("project = ?", safeProject).First(&safeSlot).Error)
	assert.Equal(t, safeMemory.ID, safeSlot.MemoryID, "unrelated project slot must survive")
}

// TestPurgeStore_PurgeProject_DoesNotTouchCredentials verifies that credentials for the
// purged project are NOT deleted (credentials are a vault concern, separate table).
func TestPurgeStore_PurgeProject_DoesNotTouchCredentials(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	const purgeProj = "test-purge-creds"
	db.Exec(`DELETE FROM memories WHERE project = ?`, purgeProj)
	defer db.Exec(`DELETE FROM credentials WHERE project = ?`, purgeProj)

	store := &Store{DB: db}
	cs := NewCredentialStore(store)
	ps := NewPurgeStore(store)
	ctx := context.Background()

	// Seed a credential for the project.
	_, err := cs.Create(ctx, &models.Credential{
		Project:                  purgeProj,
		Key:                      "test-purge-cred-key",
		EncryptedSecret:          []byte("fake-encrypted-secret"),
		EncryptionKeyFingerprint: "test-fingerprint",
	})
	require.NoError(t, err)

	var credsBefore int64
	db.Model(&Credential{}).Where("project = ?", purgeProj).Count(&credsBefore)
	require.Equal(t, int64(1), credsBefore, "setup: credential must exist before purge")

	// Purge the project (no memories — tests the credentials-only path).
	_, err = ps.PurgeProject(ctx, purgeProj)
	require.NoError(t, err)

	// Credentials must remain intact.
	var credsAfter int64
	db.Model(&Credential{}).Where("project = ?", purgeProj).Count(&credsAfter)
	assert.Equal(t, int64(1), credsAfter, "credentials must NOT be deleted by purge")
}

// TestPurgeStore_PurgeProject_EdgesDeletedAndCounted seeds memories with knowledge_edges,
// then verifies that edge count is captured and edges are deleted.
func TestPurgeStore_PurgeProject_EdgesDeletedAndCounted(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	const purgeProj = "test-purge-edges"
	db.Exec(`DELETE FROM memories WHERE project = ?`, purgeProj)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ps := NewPurgeStore(store)
	ctx := context.Background()

	// Seed 2 memories.
	m1, err := ms.Create(ctx, &models.Memory{Project: purgeProj, Content: "edge source"})
	require.NoError(t, err)
	m2, err := ms.Create(ctx, &models.Memory{Project: purgeProj, Content: "edge target"})
	require.NoError(t, err)

	// Insert an edge directly (avoids importing graph package).
	db.Exec(
		`INSERT INTO knowledge_edges (source_id, target_id, edge_type, weight, reasoning, source_session_id) VALUES (?, ?, 'related_to', 1.0, '', '')`,
		m1.ID, m2.ID,
	)

	receipt, err := ps.PurgeProject(ctx, purgeProj)
	require.NoError(t, err)

	assert.Equal(t, int64(2), receipt.MemoryCount)
	assert.Equal(t, int64(1), receipt.EdgeCount, "edge must be counted before deletion")

	// Verify edges gone.
	var edgeCount int64
	db.Table("knowledge_edges").
		Where("source_id IN ? OR target_id IN ?", []int64{m1.ID, m2.ID}, []int64{m1.ID, m2.ID}).
		Count(&edgeCount)
	assert.Equal(t, int64(0), edgeCount, "edges must be deleted")
}

// TestPurgeStore_T009_Integration is the T009 acceptance test.
// It seeds ≥3 memories with graph edges + behavioral rules for "test-purge" plus
// a credential and a memory in a DIFFERENT project.
// After purge: zero memories/rules for test-purge; edges gone; other project
// untouched; credentials untouched; audit_log has one purge row with correct receipt.
func TestPurgeStore_T009_Integration(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	const purgeProj = "test-purge-t009"
	const safeProj = "test-purge-t009-safe"

	// Pre-clean; defer post-clean.
	db.Exec(`DELETE FROM memories WHERE project IN (?, ?)`, purgeProj, safeProj)
	db.Exec(`DELETE FROM behavioral_rules WHERE project IN (?, ?)`, purgeProj, safeProj)
	db.Exec(`DELETE FROM credentials WHERE project = ?`, purgeProj)
	db.Exec(`DELETE FROM audit_log WHERE action = 'purge'`)
	defer func() {
		db.Exec(`DELETE FROM memories WHERE project IN (?, ?)`, purgeProj, safeProj)
		db.Exec(`DELETE FROM behavioral_rules WHERE project IN (?, ?)`, purgeProj, safeProj)
		db.Exec(`DELETE FROM credentials WHERE project = ?`, purgeProj)
		db.Exec(`DELETE FROM audit_log WHERE action = 'purge'`)
	}()

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	brs := NewBehavioralRulesStore(store)
	cs := NewCredentialStore(store)
	ps := NewPurgeStore(store)
	ctx := context.Background()

	// --- Seed purge-project: 3 memories ---
	var memIDs []int64
	for i := 0; i < 3; i++ {
		m, err := ms.Create(ctx, &models.Memory{
			Project: purgeProj,
			Content: "t009 test memory",
		})
		require.NoError(t, err)
		memIDs = append(memIDs, m.ID)
	}

	// Insert knowledge_edges: first↔second and second↔third.
	db.Exec(
		`INSERT INTO knowledge_edges (source_id, target_id, edge_type, weight, reasoning, source_session_id) VALUES (?, ?, 'related_to', 1.0, '', '')`,
		memIDs[0], memIDs[1],
	)
	db.Exec(
		`INSERT INTO knowledge_edges (source_id, target_id, edge_type, weight, reasoning, source_session_id) VALUES (?, ?, 'related_to', 1.0, '', '')`,
		memIDs[1], memIDs[2],
	)

	// Seed 2 behavioral rules.
	proj := purgeProj
	for i := 0; i < 2; i++ {
		_, err := brs.Create(ctx, &models.BehavioralRule{
			Project: &proj,
			Content: "t009 test rule",
		})
		require.NoError(t, err)
	}

	// Seed a credential (must survive purge — vault concern).
	_, err := cs.Create(ctx, &models.Credential{
		Project:                  purgeProj,
		Key:                      "t009-cred-key",
		EncryptedSecret:          []byte("fake-encrypted"),
		EncryptionKeyFingerprint: "t009-fp",
	})
	require.NoError(t, err)

	// --- Seed safe-project: 1 memory ---
	safeMemory, err := ms.Create(ctx, &models.Memory{Project: safeProj, Content: "safe memory"})
	require.NoError(t, err)

	// --- Execute purge ---
	receipt, err := ps.PurgeProject(ctx, purgeProj)
	require.NoError(t, err)

	// --- Verify receipt ---
	assert.Equal(t, purgeProj, receipt.Project)
	assert.Equal(t, int64(3), receipt.MemoryCount, "receipt: 3 memories")
	assert.Equal(t, int64(2), receipt.RuleCount, "receipt: 2 rules")
	assert.Equal(t, int64(2), receipt.EdgeCount, "receipt: 2 edges")
	assert.False(t, receipt.PurgedAt.IsZero())

	// --- Zero memories/rules for purge-project ---
	var count int64
	db.Model(&Memory{}).Where("project = ?", purgeProj).Count(&count)
	assert.Equal(t, int64(0), count, "memories must be deleted")

	db.Model(&BehavioralRule{}).Where("project = ?", purgeProj).Count(&count)
	assert.Equal(t, int64(0), count, "rules must be deleted")

	// --- Edges gone ---
	db.Table("knowledge_edges").
		Where("source_id IN ? OR target_id IN ?", memIDs, memIDs).
		Count(&count)
	assert.Equal(t, int64(0), count, "edges must be deleted")

	// --- Other project untouched ---
	db.Model(&Memory{}).Where("project = ?", safeProj).Count(&count)
	assert.Equal(t, int64(1), count, "safe project memory must remain")
	fetched, err := ms.Get(ctx, safeMemory.ID)
	require.NoError(t, err)
	assert.Equal(t, safeProj, fetched.Project)

	// --- Credentials untouched ---
	db.Model(&Credential{}).Where("project = ?", purgeProj).Count(&count)
	assert.Equal(t, int64(1), count, "credential must NOT be deleted by purge")

	// --- audit_log has purge row with correct receipt ---
	var entries []AuditLogEntry
	db.Where("action = 'purge'").Find(&entries)
	require.GreaterOrEqual(t, len(entries), 1, "audit_log must have at least one purge entry")
	// Find the one for this project.
	found := false
	for _, e := range entries {
		assert.Nil(t, e.MemoryID, "purge audit row memory_id must be NULL")
		if e.AfterState != nil {
			var r PurgeReceipt
			if jsonErr := jsonUnmarshal(*e.AfterState, &r); jsonErr == nil && r.Project == purgeProj {
				found = true
				assert.Equal(t, int64(3), r.MemoryCount)
				assert.Equal(t, int64(2), r.RuleCount)
			}
		}
	}
	assert.True(t, found, "audit_log must contain the purge receipt for %q", purgeProj)
}

// TestPurgeStore_PurgeProject_CitationLogDeletedAndCounted verifies that citation_log
// rows referencing the project's memories are deleted and counted.
// Requires DATABASE_DSN; skips otherwise.
func TestPurgeStore_PurgeProject_CitationLogDeletedAndCounted(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	const purgeProj = "test-purge-citation"
	db.Exec(`DELETE FROM memories WHERE project = ?`, purgeProj)
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, purgeProj)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ps := NewPurgeStore(store)
	ctx := context.Background()

	m, err := ms.Create(ctx, &models.Memory{Project: purgeProj, Content: "citation test"})
	require.NoError(t, err)

	// Insert a citation_log row referencing the memory.
	db.Exec(
		`INSERT INTO citation_log (session_id, memory_id, cited, match_type) VALUES ('sess-1', ?, true, 'exact')`,
		m.ID,
	)

	receipt, err := ps.PurgeProject(ctx, purgeProj)
	require.NoError(t, err)

	assert.Equal(t, int64(1), receipt.CitationCount, "receipt must count deleted citation_log rows")
	assert.Equal(t, int64(1), receipt.MemoryCount)

	// citation_log rows must be gone.
	var count int64
	db.Table("citation_log").Where("memory_id = ?", m.ID).Count(&count)
	assert.Equal(t, int64(0), count, "citation_log rows must be deleted")
}

// TestPurgeStore_PurgeProject_ContentChunksDeletedAndCounted verifies that content_chunks
// rows referencing the project's memories are deleted and counted.
// Requires DATABASE_DSN; skips otherwise.
func TestPurgeStore_PurgeProject_ContentChunksDeletedAndCounted(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	const purgeProj = "test-purge-chunks"
	db.Exec(`DELETE FROM memories WHERE project = ?`, purgeProj)
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, purgeProj)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ps := NewPurgeStore(store)
	ctx := context.Background()

	m, err := ms.Create(ctx, &models.Memory{Project: purgeProj, Content: "chunk test"})
	require.NoError(t, err)

	// Insert content_chunks rows (embedding is nullable when vectorscale absent).
	db.Exec(
		`INSERT INTO content_chunks (memory_id, seq, text, model) VALUES (?, 0, 'chunk text', 'test-model')`,
		m.ID,
	)
	db.Exec(
		`INSERT INTO content_chunks (memory_id, seq, text, model) VALUES (?, 1, 'chunk text 2', 'test-model')`,
		m.ID,
	)

	receipt, err := ps.PurgeProject(ctx, purgeProj)
	require.NoError(t, err)

	assert.Equal(t, int64(2), receipt.ChunkCount, "receipt must count deleted content_chunks rows")

	var count int64
	db.Table("content_chunks").Where("memory_id = ?", m.ID).Count(&count)
	assert.Equal(t, int64(0), count, "content_chunks rows must be deleted")
}

// TestPurgeStore_PurgeProject_PromotionLogDeletedAndCounted verifies that promotion_log
// rows referencing the project's memories are deleted and counted.
// Requires DATABASE_DSN; skips otherwise.
func TestPurgeStore_PurgeProject_PromotionLogDeletedAndCounted(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	const purgeProj = "test-purge-promotion"
	db.Exec(`DELETE FROM memories WHERE project = ?`, purgeProj)
	defer db.Exec(`DELETE FROM memories WHERE project = ?`, purgeProj)

	store := &Store{DB: db}
	ms := NewMemoryStore(store)
	ps := NewPurgeStore(store)
	ctx := context.Background()

	m, err := ms.Create(ctx, &models.Memory{Project: purgeProj, Content: "promotion test"})
	require.NoError(t, err)

	// Insert a promotion_log row.
	db.Exec(
		`INSERT INTO promotion_log (memory_id, from_tier, to_tier, reason) VALUES (?, 'semantic', 'core', 'test')`,
		m.ID,
	)

	receipt, err := ps.PurgeProject(ctx, purgeProj)
	require.NoError(t, err)

	assert.Equal(t, int64(1), receipt.PromotionCount, "receipt must count deleted promotion_log rows")

	var count int64
	db.Table("promotion_log").Where("memory_id = ?", m.ID).Count(&count)
	assert.Equal(t, int64(0), count, "promotion_log rows must be deleted")
}

// TestPurgeStore_PurgeProject_ZeroRowsSucceeds verifies that purging a project with
// no data succeeds and returns all-zero counts. Idempotent re-purge is a legitimate
// caller pattern — do NOT fail on zero rows.
// Requires DATABASE_DSN; skips otherwise.
func TestPurgeStore_PurgeProject_ZeroRowsSucceeds(t *testing.T) {
	db, cleanup := openTestDB(t)
	defer cleanup()

	const purgeProj = "test-purge-zero-rows-project-that-does-not-exist"
	// Ensure truly empty.
	db.Exec(`DELETE FROM memories WHERE project = ?`, purgeProj)
	db.Exec(`DELETE FROM behavioral_rules WHERE project = ?`, purgeProj)

	ps := NewPurgeStore(&Store{DB: db})
	receipt, err := ps.PurgeProject(context.Background(), purgeProj)
	require.NoError(t, err, "zero-row purge must succeed (idempotent)")

	assert.Equal(t, int64(0), receipt.MemoryCount)
	assert.Equal(t, int64(0), receipt.RuleCount)
	assert.Equal(t, int64(0), receipt.EdgeCount)
	assert.Equal(t, int64(0), receipt.CitationCount)
	assert.Equal(t, int64(0), receipt.ChunkCount)
	assert.Equal(t, int64(0), receipt.PromotionCount)
	assert.Equal(t, purgeProj, receipt.Project)
	assert.False(t, receipt.PurgedAt.IsZero(), "PurgedAt must be set even for zero-row purge")
}

// TestPurgeStore_PurgeProject_WaitsForConcurrentContinuitySet verifies that a
// purge locks target memories before clearing continuity slots. This keeps a
// concurrent continuity set from committing a new RESTRICT-FK slot after purge
// has already passed its slot delete.
func TestPurgeStore_PurgeProject_WaitsForConcurrentContinuitySet(t *testing.T) {
	db, closeDB := openTestDB(t)
	t.Cleanup(closeDB)
	// A dedicated connection makes the purge's blocked SQL observable without
	// confusing it with the setter or the test observer.
	purgeDB := openCandidateTestDB(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	purgeProject := fmt.Sprintf("test-purge-continuity-race-%d", suffix)
	safeProject := fmt.Sprintf("test-purge-continuity-race-safe-%d", suffix)

	t.Cleanup(func() {
		require.NoError(t, db.Exec(`DELETE FROM project_continuity_slots WHERE project IN (?, ?)`, purgeProject, safeProject).Error)
		require.NoError(t, db.Exec(`DELETE FROM memories WHERE project IN (?, ?)`, purgeProject, safeProject).Error)
	})

	memoryStore := NewMemoryStore(&Store{DB: db})
	slotStore := NewContinuitySlotStore(db)
	purgeStore := NewPurgeStore(&Store{DB: purgeDB})
	target, err := memoryStore.Create(ctx, &models.Memory{Project: purgeProject, Content: "purge race target"})
	require.NoError(t, err)
	safe, err := memoryStore.Create(ctx, &models.Memory{Project: safeProject, Content: "purge race safe"})
	require.NoError(t, err)
	require.NoError(t, slotStore.Upsert(ctx, ContinuitySlot{
		Project:                     safeProject,
		MemoryID:                    safe.ID,
		ExpiresAt:                   time.Now().UTC().Add(time.Hour),
		AuthorityDomain:             "test",
		AuthorityOwnerPrincipal:     "agent:test",
		AuthorityOwnerPrincipalKind: "agent",
	}))

	setTx := db.WithContext(ctx).Begin()
	require.NoError(t, setTx.Error)
	defer func() { _ = setTx.Rollback().Error }()
	lockedTarget, err := memoryStore.GetCurrentForSnapshotTx(ctx, setTx, target.ID)
	require.NoError(t, err)

	purgeResult := make(chan error, 1)
	go func() { _, err := purgeStore.PurgeProject(ctx, purgeProject); purgeResult <- err }()

	// The unfixed implementation reaches DELETE memories while the setter holds
	// its target lock. Completing the set at that point deterministically exposes
	// the old SQLSTATE 23503 RESTRICT-FK failure as RED evidence.
	if purgeRaceStatementObserved(t, db, 250*time.Millisecond, "DELETE FROM memories WHERE project =") {
		t.Log("observed legacy purge waiting on target delete; completing concurrent set")
		require.NoError(t, slotStore.UpsertTx(ctx, setTx, ContinuitySlot{
			Project:                     purgeProject,
			MemoryID:                    lockedTarget.ID,
			ExpiresAt:                   time.Now().UTC().Add(time.Hour),
			AuthorityDomain:             "test",
			AuthorityOwnerPrincipal:     "agent:test",
			AuthorityOwnerPrincipalKind: "agent",
		}))
		require.NoError(t, setTx.Commit().Error)
		select {
		case err := <-purgeResult:
			require.NoError(t, err, "purge must not fail with the concurrent continuity slot's RESTRICT FK")
		case <-ctx.Done():
			t.Fatal("purge did not finish after concurrent set committed")
		}
		return
	}

	require.True(t, purgeRaceStatementObserved(t, db, time.Second, "SELECT 1 FROM memories WHERE project ="), "purge did not attempt target-memory FOR UPDATE")
	select {
	case err := <-purgeResult:
		t.Fatalf("purge completed before the setter committed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, slotStore.UpsertTx(ctx, setTx, ContinuitySlot{
		Project:                     purgeProject,
		MemoryID:                    lockedTarget.ID,
		ExpiresAt:                   time.Now().UTC().Add(time.Hour),
		AuthorityDomain:             "test",
		AuthorityOwnerPrincipal:     "agent:test",
		AuthorityOwnerPrincipalKind: "agent",
	}))
	require.NoError(t, setTx.Commit().Error)

	select {
	case err := <-purgeResult:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("purge did not finish after concurrent set committed")
	}

	var count int64
	require.NoError(t, db.Model(&Memory{}).Where("id = ?", target.ID).Count(&count).Error)
	assert.Zero(t, count, "purge target must be deleted")
	require.NoError(t, db.Model(&ContinuitySlot{}).Where("project = ?", purgeProject).Count(&count).Error)
	assert.Zero(t, count, "purge target slot must be deleted")
	require.NoError(t, db.Model(&Memory{}).Where("id = ?", safe.ID).Count(&count).Error)
	assert.Equal(t, int64(1), count, "unrelated memory must survive")
	require.NoError(t, db.Model(&ContinuitySlot{}).Where("project = ?", safeProject).Count(&count).Error)
	assert.Equal(t, int64(1), count, "unrelated slot must survive")
}

func purgeRaceStatementObserved(t *testing.T, db *gorm.DB, timeout time.Duration, statementPrefix string) bool {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		var count int64
		require.NoError(t, db.Raw(`
			SELECT count(*)
			FROM pg_stat_activity
			WHERE application_name = ?
			  AND state = 'active'
			  AND wait_event_type = 'Lock'
			  AND query LIKE ?
		`, candidateTestDBApplicationName, statementPrefix+"%").Scan(&count).Error)
		if count > 0 {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-ticker.C:
		}
	}
}

// TestPurgeStore_ReceiptFields_AllPresent verifies PurgeReceipt has all required fields
// for the extended receipt (citation, chunk, promotion). No DATABASE_DSN required.
func TestPurgeStore_ReceiptFields_AllPresent(t *testing.T) {
	r := PurgeReceipt{
		Project:        "p",
		MemoryCount:    1,
		RuleCount:      2,
		EdgeCount:      3,
		AuditCount:     4,
		CitationCount:  5,
		ChunkCount:     6,
		PromotionCount: 7,
	}
	b, err := json.Marshal(r)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"citation_count":5`)
	assert.Contains(t, s, `"chunk_count":6`)
	assert.Contains(t, s, `"promotion_count":7`)
}

// jsonUnmarshal is a local alias to avoid importing encoding/json at package level
// (it is already imported by purge_store.go; this keeps the test self-contained).
func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
