package mcp

// audit_helpers.go — fire-and-forget audit emission helpers for MCP mutation paths.
//
// Design decisions (Milestone D T002/T003):
//   - Audit calls happen at the handler layer (not the store layer) per the task contract.
//   - All helpers are gated on ENGRAM_VNEXT_ENABLED and nil-guarded.
//   - All writes are fire-and-forget: spawned as goroutines so a slow DB write never
//     fails or slows the mutation response (NFR-D4).
//   - The server exposes a private auditWriter interface so unit tests can inject
//     a mock without requiring a live DB (testability gate from T002 TDD spec).

import (
	"context"
	"encoding/json"
	"os"

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
// Fire-and-forget: called from a goroutine so it never blocks the MCP response.
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
	go func() {
		entry := gorm.AuditLogEntry{
			MemoryID:        &memID,
			Action:          "create",
			Actor:           actor,
			SourceSessionID: actor,
			AfterState:      afterState,
		}
		if err := aw.Log(context.Background(), entry); err != nil {
			log.Error().Err(err).Int64("memory_id", memID).Msg("audit: create log failed")
		}
	}()
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
	go func() {
		entry := gorm.AuditLogEntry{
			MemoryID:        &memID,
			Action:          "update",
			Actor:           actor,
			SourceSessionID: actor,
			BeforeState:     beforeState,
			AfterState:      afterState,
		}
		if err := aw.Log(context.Background(), entry); err != nil {
			log.Error().Err(err).Int64("memory_id", memID).Msg("audit: update log failed")
		}
	}()
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
	go func() {
		entry := gorm.AuditLogEntry{
			MemoryID:        &memID,
			Action:          "delete",
			Actor:           actor,
			SourceSessionID: actor,
			BeforeState:     beforeState,
		}
		if err := aw.Log(context.Background(), entry); err != nil {
			log.Error().Err(err).Int64("memory_id", memID).Msg("audit: delete log failed")
		}
	}()
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
	go func() {
		entry := gorm.AuditLogEntry{
			MemoryID:        &memID,
			Action:          "supersede",
			Actor:           actor,
			SourceSessionID: actor,
			BeforeState:     beforeState,
		}
		if err := aw.Log(context.Background(), entry); err != nil {
			log.Error().Err(err).Int64("memory_id", memID).Msg("audit: supersede log failed")
		}
	}()
}
