package mcp

// audit_helpers.go — fire-and-forget audit emission helpers for MCP mutation paths.
//
// Design decisions (Milestone D T002/T003):
//   - Audit calls happen at the handler layer (not the store layer) per the task contract.
//   - All helpers are gated on ENGRAM_VNEXT_ENABLED and nil-guarded.
//   - All writes are fire-and-forget via runAuditAsync, which adds a 10s timeout,
//     panic recovery, and structured error logging so a slow/panicking DB write never
//     crashes the process or leaks goroutines (Finding 4).
//   - The server exposes a private auditWriter interface so unit tests can inject
//     a mock without requiring a live DB (testability gate from T002 TDD spec).

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/rs/zerolog/log"
	gorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

// auditWriter is the minimal interface the audit helpers call.
// *gorm.AuditStore satisfies it; tests inject a mock via setTestAuditWriter.
type auditWriter interface {
	Log(ctx context.Context, entry gorm.AuditLogEntry) error
}

// effectiveAuditWriter returns the active auditWriter for s.
// Precedence: testAuditWriter (set in tests) → concrete auditStore.
func (s *Server) effectiveAuditWriter() auditWriter {
	if s.testAuditWriter != nil {
		return s.testAuditWriter
	}
	if s.auditStore != nil {
		return s.auditStore
	}
	return nil
}

// isAuditEnabled returns true when ENGRAM_VNEXT_ENABLED="true".
func isAuditEnabled() bool {
	return os.Getenv("ENGRAM_VNEXT_ENABLED") == "true"
}

// runAuditAsync runs fn in a goroutine with a 10-second detached context,
// panic recovery, and structured error logging. All async audit writes MUST
// use this helper instead of bare go func() to avoid process-crashing panics
// and goroutine leaks from wedged DB calls (Finding 4).
func runAuditAsync(label string, memID int64, fn func(ctx context.Context) error) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error().
					Str("audit_label", label).
					Int64("memory_id", memID).
					Interface("panic", r).
					Msg("audit: goroutine panic recovered")
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := fn(ctx); err != nil {
			log.Error().
				Err(err).
				Str("audit_label", label).
				Int64("memory_id", memID).
				Msg("audit: async write failed")
		}
	}()
}

// marshalState JSON-encodes a memory for before/after audit state. Errors are
// logged and a nil is returned so the audit entry still lands without state.
func marshalState(m *models.Memory) *json.RawMessage {
	if m == nil {
		return nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		log.Error().Err(err).Int64("memory_id", m.ID).Msg("audit: failed to marshal memory state")
		return nil
	}
	raw := json.RawMessage(b)
	return &raw
}

// logAuditCreate emits a create audit event after a successful memory creation.
// Fire-and-forget via runAuditAsync (timeout + panic recovery).
func logAuditCreate(ctx context.Context, s *Server, created *models.Memory, actor string) {
	if !isAuditEnabled() {
		return
	}
	aw := s.effectiveAuditWriter()
	if aw == nil {
		return
	}
	memID := created.ID
	afterState := marshalState(created)
	// Finding 3: SourceSessionID carries the raw session ID (empty when absent);
	// actor is the human-readable fallback used for display in the Actor field.
	sessionID := sessionFromContext(ctx)
	runAuditAsync("create", memID, func(ctx context.Context) error {
		return aw.Log(ctx, gorm.AuditLogEntry{
			MemoryID:        &memID,
			Action:          "create",
			Actor:           actor,
			SourceSessionID: sessionID,
			AfterState:      afterState,
		})
	})
}

// logAuditEdit emits an update audit event after a successful memory edit.
func logAuditEdit(ctx context.Context, s *Server, before, after *models.Memory, actor string) {
	if !isAuditEnabled() {
		return
	}
	aw := s.effectiveAuditWriter()
	if aw == nil {
		return
	}
	memID := before.ID
	beforeState := marshalState(before)
	afterState := marshalState(after)
	sessionID := sessionFromContext(ctx)
	runAuditAsync("update", memID, func(ctx context.Context) error {
		return aw.Log(ctx, gorm.AuditLogEntry{
			MemoryID:        &memID,
			Action:          "update",
			Actor:           actor,
			SourceSessionID: sessionID,
			BeforeState:     beforeState,
			AfterState:      afterState,
		})
	})
}

// logAuditDelete emits a delete audit event after a successful memory deletion.
func logAuditDelete(ctx context.Context, s *Server, mem *models.Memory, actor string) {
	if !isAuditEnabled() {
		return
	}
	aw := s.effectiveAuditWriter()
	if aw == nil {
		return
	}
	memID := mem.ID
	beforeState := marshalState(mem)
	sessionID := sessionFromContext(ctx)
	runAuditAsync("delete", memID, func(ctx context.Context) error {
		return aw.Log(ctx, gorm.AuditLogEntry{
			MemoryID:        &memID,
			Action:          "delete",
			Actor:           actor,
			SourceSessionID: sessionID,
			BeforeState:     beforeState,
		})
	})
}

// logAuditSupersede emits a supersede audit event when a memory is superseded.
func logAuditSupersede(ctx context.Context, s *Server, superseded *models.Memory, actor string) {
	if !isAuditEnabled() {
		return
	}
	aw := s.effectiveAuditWriter()
	if aw == nil {
		return
	}
	memID := superseded.ID
	beforeState := marshalState(superseded)
	sessionID := sessionFromContext(ctx)
	runAuditAsync("supersede", memID, func(ctx context.Context) error {
		return aw.Log(ctx, gorm.AuditLogEntry{
			MemoryID:        &memID,
			Action:          "supersede",
			Actor:           actor,
			SourceSessionID: sessionID,
			BeforeState:     beforeState,
		})
	})
}
