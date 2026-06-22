package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thebtf/engram/internal/auth"
	dbgorm "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
)

func TestStoreMemoryDomainRegistry_WarnRejectAndCompatibility(t *testing.T) {
	project := "test-mcp-domain-registry-" + uuid.NewString()
	env := newMCPDomainWriteTestEnv(t, project)
	domains := dbgorm.NewDomainOwnerStore(env.store)

	t.Run("missing row preserves current behavior", func(t *testing.T) {
		domain := "test-mcp-domain-missing-" + uuid.NewString()
		out, err := storeMCPMemoryWithDomain(t, env.srv, project, domain, "agent/bob")
		require.NoError(t, err)

		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &body))
		assert.NotContains(t, body, "domain_warning")
		assert.Equal(t, float64(1), float64(countMCPMemoriesByDomain(t, env.store, project, domain)))
	})

	t.Run("off allows cross owner", func(t *testing.T) {
		domain := "test-mcp-domain-off-" + uuid.NewString()
		_, err := domains.Upsert(context.Background(), &dbgorm.DomainOwner{
			Domain:             domain,
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			Mode:               dbgorm.DomainOwnerModeOff,
		})
		require.NoError(t, err)

		_, err = storeMCPMemoryWithDomain(t, env.srv, project, domain, "agent/bob")
		require.NoError(t, err)
		assert.Equal(t, int64(1), countMCPMemoriesByDomain(t, env.store, project, domain))
	})

	t.Run("same owner allows without warning", func(t *testing.T) {
		domain := "test-mcp-domain-same-" + uuid.NewString()
		_, err := domains.Upsert(context.Background(), &dbgorm.DomainOwner{
			Domain:             domain,
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			Mode:               dbgorm.DomainOwnerModeWarn,
		})
		require.NoError(t, err)

		out, err := storeMCPMemoryWithDomain(t, env.srv, project, domain, "agent/alice")
		require.NoError(t, err)

		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &body))
		assert.NotContains(t, body, "domain_warning")
	})

	t.Run("warn allows with structured warning", func(t *testing.T) {
		domain := "test-mcp-domain-warn-" + uuid.NewString()
		_, err := domains.Upsert(context.Background(), &dbgorm.DomainOwner{
			Domain:             domain,
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			Mode:               dbgorm.DomainOwnerModeWarn,
		})
		require.NoError(t, err)

		out, err := storeMCPMemoryWithDomain(t, env.srv, project, domain, "agent/bob")
		require.NoError(t, err)

		var body map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &body))
		warning, ok := body["domain_warning"].(map[string]any)
		require.True(t, ok, "warn mode response must include structured domain_warning")
		assert.Equal(t, principalmemory.DomainWriteWarningCrossOwner, warning["code"])
		assert.Equal(t, principalmemory.AuditStatusWritten, body["domain_audit_status"])
		assert.Equal(t, int64(1), countMCPMemoriesByDomain(t, env.store, project, domain))
	})

	t.Run("reject denies before persistence", func(t *testing.T) {
		domain := "test-mcp-domain-reject-" + uuid.NewString()
		_, err := domains.Upsert(context.Background(), &dbgorm.DomainOwner{
			Domain:             domain,
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			Mode:               dbgorm.DomainOwnerModeReject,
		})
		require.NoError(t, err)

		_, err = storeMCPMemoryWithDomain(t, env.srv, project, domain, "agent/bob")
		require.Error(t, err)
		assert.ErrorIs(t, err, principalmemory.ErrDomainWriteRejected)
		assert.Equal(t, int64(0), countMCPMemoriesByDomain(t, env.store, project, domain))
	})
}

func TestStoreMemoryDomainRegistry_AuditFailureBlocksBeforePersistence(t *testing.T) {
	project := "test-mcp-domain-registry-audit-" + uuid.NewString()
	env := newMCPDomainWriteTestEnv(t, project)
	env.srv.SetDomainRegistryService(&fakeMCPDomainRegistryService{err: errors.New("domain write audit: unavailable")})

	domain := "test-mcp-domain-audit-" + uuid.NewString()
	_, err := storeMCPMemoryWithDomain(t, env.srv, project, domain, "agent/bob")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "domain registry check failed")
	assert.Equal(t, int64(0), countMCPMemoriesByDomain(t, env.store, project, domain))
}

func TestStoreMemoryDomainRegistry_InvalidWriterKindRejectsBeforePersistence(t *testing.T) {
	project := "test-mcp-domain-registry-kind-" + uuid.NewString()
	env := newMCPDomainWriteTestEnv(t, project)

	domain := "test-mcp-domain-kind-" + uuid.NewString()
	args := mustJSON(t, map[string]any{
		"content": "domain governed memory",
		"project": project,
		"domain":  domain,
	})
	id := auth.ClientWithPrincipal("read-write", "keycard-domain-test", "daemon/bob", auth.PrincipalKind("daemon"))
	ctx := auth.WithIdentity(context.Background(), id)

	_, err := env.srv.handleStoreMemory(ctx, args)

	require.Error(t, err)
	assert.Equal(t, int64(0), countMCPMemoriesByDomain(t, env.store, project, domain))
}

func newMCPDomainWriteTestEnv(t *testing.T, project string) t007TestEnv {
	t.Helper()
	env := newMemoryServerForT007(t, project)
	domains := dbgorm.NewDomainOwnerStore(env.store)
	env.srv.SetDomainRegistryService(principalmemory.NewDomainRegistryService(
		domains,
		dbgorm.NewAuditStore(env.store.DB),
	))
	t.Cleanup(func() {
		require.NoError(t, env.store.DB.WithContext(context.Background()).Exec("DELETE FROM memory_domain_owners WHERE domain LIKE 'test-mcp-domain-%'").Error)
		require.NoError(t, env.store.DB.WithContext(context.Background()).Exec("DELETE FROM audit_log WHERE action IN (?, ?)", principalmemory.AuditActionDomainWriteWarn, principalmemory.AuditActionDomainWriteReject).Error)
	})
	return env
}

func storeMCPMemoryWithDomain(t *testing.T, srv *Server, project, domain, principal string) (string, error) {
	t.Helper()
	args := mustJSON(t, map[string]any{
		"content": "domain governed memory",
		"project": project,
		"domain":  domain,
	})
	id := auth.ClientWithPrincipal("read-write", "keycard-domain-test", principal, auth.PrincipalKindAgent)
	ctx := auth.WithIdentity(context.Background(), id)
	return srv.handleStoreMemory(ctx, args)
}

func countMCPMemoriesByDomain(t *testing.T, store *dbgorm.Store, project, domain string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, store.DB.Model(&dbgorm.Memory{}).Where("project = ? AND domain = ? AND deleted_at IS NULL", project, domain).Count(&count).Error)
	return count
}

type fakeMCPDomainRegistryService struct {
	decision *principalmemory.DomainWriteDecision
	err      error
}

func (f *fakeMCPDomainRegistryService) CheckWrite(ctx context.Context, req principalmemory.DomainWriteCheckRequest) (*principalmemory.DomainWriteDecision, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.decision != nil {
		return f.decision, nil
	}
	return &principalmemory.DomainWriteDecision{Allowed: true}, nil
}
