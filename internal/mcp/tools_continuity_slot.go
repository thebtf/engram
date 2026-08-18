package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/thebtf/engram/internal/auth"
	"github.com/thebtf/engram/internal/config"
	gormstore "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/scope"
	"github.com/thebtf/engram/pkg/models"
	gormlib "gorm.io/gorm"
)

const continuitySlotAuthorizationError = "continuity_slot: authorization denied"

var errContinuitySlotTransactionAuthorization = errors.New(continuitySlotAuthorizationError)

func continuitySlotEnabled() bool {
	return os.Getenv(config.EnvContinuitySlotEnabled) == "true" && vnextEnabled()
}

func continuitySlotTool() Tool {
	return Tool{
		Name: "continuity_slot", Description: "Set or clear the one expiring continuity designation for the current canonical project.", tier: tierUseful,
		InputSchema: map[string]any{"type": "object", "required": []string{"action", "project"}, "properties": map[string]any{
			"action":  map[string]any{"type": "string", "enum": []string{"set", "clear"}},
			"project": map[string]any{"type": "string"}, "memory_id": map[string]any{"type": "integer"}, "expires_at": map[string]any{"type": "string"},
		}},
	}
}

func (s *Server) handleContinuitySlot(ctx context.Context, args json.RawMessage) (string, error) {
	if !continuitySlotEnabled() {
		return "", fmt.Errorf("continuity_slot requires ENGRAM_CONTINUITY_SLOT_ENABLED=true and ENGRAM_VNEXT_ENABLED=true")
	}
	values, err := parseArgs(args)
	if err != nil {
		return "", err
	}
	action, present, err := optionalStringArg(values, "action")
	if err != nil {
		return "", fmt.Errorf("continuity_slot: %w", err)
	}
	action = strings.TrimSpace(action)
	if !present || (action != "set" && action != "clear") {
		return "", fmt.Errorf("continuity_slot: action must be set or clear")
	}
	project, err := continuitySlotProject(ctx, values)
	if err != nil {
		return "", err
	}
	caller, actor, ok := continuitySlotCaller(ctx)
	if !ok {
		return "", errors.New(continuitySlotAuthorizationError)
	}
	var memoryID int64
	var expiresAt time.Time
	if action == "set" {
		memoryID, expiresAt, err = continuitySlotSetParams(values)
		if err != nil {
			return "", err
		}
	}
	if s.memoryStore == nil || s.continuitySlotStore == nil || s.auditStore == nil {
		return "", fmt.Errorf("continuity_slot: required stores are not wired")
	}
	if action == "clear" {
		return s.clearContinuitySlot(ctx, project, caller, actor)
	}
	return s.setContinuitySlot(ctx, project, caller, actor, memoryID, expiresAt)
}

func continuitySlotProject(ctx context.Context, values map[string]any) (string, error) {
	project, present, err := optionalStringArg(values, "project")
	if err != nil {
		return "", fmt.Errorf("continuity_slot: %w", err)
	}
	project = strings.TrimSpace(project)
	if !present || project == "" {
		return "", fmt.Errorf("continuity_slot: project is required")
	}
	if canonical := strings.TrimSpace(projectFromContext(ctx)); canonical == "" || project != canonical {
		return "", errors.New(continuitySlotAuthorizationError)
	}
	return project, nil
}

func continuitySlotCaller(ctx context.Context) (scope.KeycardContext, string, bool) {
	identity, ok := auth.IdentityFrom(ctx)
	if !ok || identity.Source != auth.SourceClient || identity.Role != auth.RoleReadWrite || strings.TrimSpace(identity.Principal) == "" || !auth.IsValidPrincipalKind(identity.PrincipalKind) {
		return scope.KeycardContext{}, "", false
	}
	kind := string(identity.PrincipalKind)
	return scope.KeycardContext{WorkstationID: identity.WorkstationID(), SessionID: sessionFromContext(ctx), Principal: identity.Principal, PrincipalKind: kind}, fmt.Sprintf("principal:%s:%s", kind, identity.Principal), true
}

func continuitySlotSetParams(values map[string]any) (int64, time.Time, error) {
	memoryID, err := requireInt64Arg(values, "memory_id")
	if err != nil || memoryID <= 0 {
		return 0, time.Time{}, fmt.Errorf("continuity_slot: memory_id must be a positive integer")
	}
	raw, present, err := optionalStringArg(values, "expires_at")
	if err != nil || !present || !strings.HasSuffix(strings.TrimSpace(raw), "Z") {
		return 0, time.Time{}, fmt.Errorf("continuity_slot: expires_at must be an RFC3339 UTC timestamp")
	}
	expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil || !expiresAt.After(time.Now().UTC()) {
		return 0, time.Time{}, fmt.Errorf("continuity_slot: expires_at must be in the future")
	}
	return memoryID, expiresAt.UTC(), nil
}

func (s *Server) setContinuitySlot(ctx context.Context, project string, caller scope.KeycardContext, actor string, memoryID int64, expiresAt time.Time) (string, error) {
	err := s.memoryStore.GetDB().WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		memory, err := s.memoryStore.GetCurrentForSnapshotTx(ctx, tx, memoryID)
		if err != nil || !continuitySlotTargetAllowed(memory, project, caller) {
			return errContinuitySlotTransactionAuthorization
		}
		slot := gormstore.ContinuitySlot{Project: project, MemoryID: memory.ID, ExpiresAt: expiresAt, AuthorityDomain: memory.Domain, AuthorityOwnerPrincipal: memory.OwnerPrincipal, AuthorityOwnerPrincipalKind: memory.OwnerPrincipalKind}
		if err := s.continuitySlotStore.UpsertTx(ctx, tx, slot); err != nil {
			return err
		}
		return s.auditStore.LogTx(ctx, tx, continuitySlotAuditEntry(ctx, "set", actor, project, &memory.ID, &expiresAt))
	})
	if errors.Is(err, errContinuitySlotTransactionAuthorization) {
		return "", errors.New(continuitySlotAuthorizationError)
	}
	if err != nil {
		return "", fmt.Errorf("continuity_slot: mutation failed: %w", err)
	}
	return continuitySlotResult("set")
}

func continuitySlotTargetAllowed(memory *models.Memory, project string, caller scope.KeycardContext) bool {
	if memory == nil || memory.Project != project || memory.Status != "active" || strings.TrimSpace(memory.Domain) == "" {
		return false
	}
	opts := scope.MemoryVisibilityOptions{ApplyPrivacyScope: os.Getenv("ENGRAM_VNEXT_F_ENABLED") == "true"}
	return scope.ResolveMemory(caller, memory, opts) && scope.ResolveMemoryManage(caller, memory)
}

func (s *Server) clearContinuitySlot(ctx context.Context, project string, caller scope.KeycardContext, actor string) (string, error) {
	err := s.memoryStore.GetDB().WithContext(ctx).Transaction(func(tx *gormlib.DB) error {
		slot, err := s.continuitySlotStore.GetTx(ctx, tx, project)
		if errors.Is(err, gormlib.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if !continuitySlotStoredAuthorityAllowed(caller, slot) {
			return errContinuitySlotTransactionAuthorization
		}
		memoryID := slot.MemoryID
		if _, err := s.continuitySlotStore.ClearTx(ctx, tx, project); err != nil {
			return err
		}
		return s.auditStore.LogTx(ctx, tx, continuitySlotAuditEntry(ctx, "clear", actor, project, &memoryID, &slot.ExpiresAt))
	})
	if errors.Is(err, errContinuitySlotTransactionAuthorization) {
		return "", errors.New(continuitySlotAuthorizationError)
	}
	if err != nil {
		return "", fmt.Errorf("continuity_slot: mutation failed: %w", err)
	}
	return continuitySlotResult("clear")
}

func continuitySlotStoredAuthorityAllowed(caller scope.KeycardContext, slot *gormstore.ContinuitySlot) bool {
	return slot != nil && strings.TrimSpace(slot.AuthorityDomain) != "" && scope.ResolveMemoryManage(caller, &models.Memory{Domain: slot.AuthorityDomain, OwnerPrincipal: slot.AuthorityOwnerPrincipal, OwnerPrincipalKind: slot.AuthorityOwnerPrincipalKind})
}

func continuitySlotAuditEntry(ctx context.Context, action, actor, project string, memoryID *int64, expiresAt *time.Time) gormstore.AuditLogEntry {
	return gormstore.AuditLogEntry{MemoryID: memoryID, Action: "continuity_slot_" + action, Actor: actor, SourceSessionID: sessionFromContext(ctx), Reason: fmt.Sprintf("project=%q action=%s target_id=%d expires_at=%s", project, action, *memoryID, expiresAt.UTC().Format(time.RFC3339Nano))}
}

func continuitySlotResult(action string) (string, error) {
	result, err := json.Marshal(map[string]string{"action": action, "status": "ok"})
	return string(result), err
}
