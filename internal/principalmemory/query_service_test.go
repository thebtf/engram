package principalmemory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/pkg/models"
)

func TestPrincipalMemoryQueryService_FiltersHiddenRowsAndKeepsBoundedShape(t *testing.T) {
	store := &fakePrincipalMemoryStore{
		rows: []*models.Memory{
			{
				ID:                 101,
				Project:            "project-a",
				Content:            "private alice implementation detail",
				OwnerPrincipal:     "agent/alice",
				OwnerPrincipalKind: "agent",
				AgentVisibility:    models.AgentVisibilityPrivate,
				Domain:             "operator-console",
			},
			{
				ID:                 102,
				Project:            "project-a",
				Content:            "shared alice note",
				Tags:               []string{"semantic"},
				OwnerPrincipal:     "agent/alice",
				OwnerPrincipalKind: "agent",
				AgentVisibility:    models.AgentVisibilityShared,
				Domain:             "operator-console",
				Confidence:         0.8,
				CreatedAt:          time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC),
			},
			{
				ID:      103,
				Project: "project-a",
				Content: "legacy row without principal metadata",
				Domain:  "operator-console",
			},
		},
	}
	svc := NewPrincipalMemoryQueryService(store, &fakeAuditLogger{})

	result, err := svc.Query(context.Background(), PrincipalMemoryQueryRequest{
		Project:            "project-a",
		Caller:             PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		Domain:             "operator-console",
		Limit:              2,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "agent/alice", result.Principal)
	assert.Equal(t, "agent", result.PrincipalKind)
	assert.Equal(t, "project-a", result.Project)
	assert.Equal(t, "operator-console", result.Domain)
	assert.Equal(t, "principal_memory_query", result.Audit.Action)
	assert.False(t, result.Audit.Durable)
	require.Len(t, result.Items, 2, "visible output must be bounded by request limit")
	assert.Equal(t, 1, result.HiddenCount)
	assert.Equal(t, "not_required", result.AuditStatus)
	assert.Equal(t, int64(102), result.Items[0].ID)
	assert.Equal(t, "agent/alice", result.Items[0].OwnerPrincipal)
	assert.Equal(t, []string{"semantic"}, result.Items[0].Tags)
	assert.Equal(t, 0.8, result.Items[0].Confidence)
	assert.Equal(t, time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC), result.Items[0].CreatedAt)
	assert.Equal(t, int64(103), result.Items[1].ID)
	assert.Equal(t, "", result.Items[1].OwnerPrincipal, "legacy no-principal rows stay visible")

	assert.Equal(t, "project-a", store.project)
	assert.Equal(t, "agent/alice", store.opts.OwnerPrincipal)
	assert.Equal(t, "agent", store.opts.OwnerPrincipalKind)
	assert.Equal(t, "operator-console", store.opts.Domain)
	assert.GreaterOrEqual(t, store.opts.Limit, 2, "service may over-fetch to compute hidden_count but must bound output")
}

func TestPrincipalMemoryQueryService_AdminPrivateWideningRequiresAudit(t *testing.T) {
	memID := int64(201)
	store := &fakePrincipalMemoryStore{
		rows: []*models.Memory{
			{
				ID:                 memID,
				Project:            "project-a",
				Content:            "private alice memory",
				OwnerPrincipal:     "agent/alice",
				OwnerPrincipalKind: "agent",
				AgentVisibility:    models.AgentVisibilityPrivate,
				Domain:             "operator-console",
			},
		},
	}
	audit := &fakeAuditLogger{}
	svc := NewPrincipalMemoryQueryService(store, audit)

	result, err := svc.Query(context.Background(), PrincipalMemoryQueryRequest{
		Project:            "project-a",
		Caller:             PrincipalRef{Principal: "operator/oleg", PrincipalKind: "human"},
		CallerIsAdmin:      true,
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		Domain:             "operator-console",
		Limit:              1,
		SourceSessionID:    "session-42",
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Items, 1)
	assert.Equal(t, "written", result.AuditStatus)
	assert.Equal(t, "principal_memory_query", result.Audit.Action)
	assert.True(t, result.Audit.Durable)
	require.Len(t, audit.entries, 1)
	assert.Equal(t, "principal_memory_private_read", audit.entries[0].Action)
	assert.Equal(t, "operator/oleg", audit.entries[0].Actor)
	assert.Equal(t, "session-42", audit.entries[0].SourceSessionID)
	require.NotNil(t, audit.entries[0].MemoryID)
	assert.Equal(t, memID, *audit.entries[0].MemoryID)
}

func TestPrincipalMemoryQueryService_AdminPrivateWideningFailsClosedOnAuditError(t *testing.T) {
	store := &fakePrincipalMemoryStore{
		rows: []*models.Memory{
			{
				ID:                 301,
				Project:            "project-a",
				Content:            "private alice memory",
				OwnerPrincipal:     "agent/alice",
				OwnerPrincipalKind: "agent",
				AgentVisibility:    models.AgentVisibilityPrivate,
				Domain:             "operator-console",
			},
		},
	}
	svc := NewPrincipalMemoryQueryService(store, &fakeAuditLogger{err: errors.New("audit unavailable")})

	result, err := svc.Query(context.Background(), PrincipalMemoryQueryRequest{
		Project:            "project-a",
		Caller:             PrincipalRef{Principal: "operator/oleg", PrincipalKind: "human"},
		CallerIsAdmin:      true,
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		Domain:             "operator-console",
		Limit:              1,
	})
	require.Error(t, err)
	assert.Nil(t, result, "private widening must fail closed when durable audit cannot be written")
}

func TestPrincipalMemoryQueryService_IncludePrivateNonAdminCrossPrincipalFailsBeforeStore(t *testing.T) {
	store := &fakePrincipalMemoryStore{}
	svc := NewPrincipalMemoryQueryService(store, &fakeAuditLogger{})

	result, err := svc.Query(context.Background(), PrincipalMemoryQueryRequest{
		Project:            "project-a",
		Caller:             PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		IncludePrivate:     true,
		Limit:              1,
	})

	require.ErrorIs(t, err, ErrCrossPrincipalPrivateDenied)
	assert.Nil(t, result)
	assert.False(t, store.called, "include_private denial must happen before the store query")
}

type fakePrincipalMemoryStore struct {
	rows    []*models.Memory
	project string
	opts    gormdb.ListOptions
	called  bool
}

func (f *fakePrincipalMemoryStore) ListPrincipalMemory(ctx context.Context, project string, opts gormdb.ListOptions) ([]*models.Memory, error) {
	f.called = true
	f.project = project
	f.opts = opts
	return f.rows, nil
}

type fakeAuditLogger struct {
	err     error
	entries []gormdb.AuditLogEntry
}

func (f *fakeAuditLogger) Log(ctx context.Context, entry gormdb.AuditLogEntry) error {
	if f.err != nil {
		return f.err
	}
	f.entries = append(f.entries, entry)
	return nil
}
