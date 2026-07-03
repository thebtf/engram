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
			},
			{
				ID:                 102,
				Project:            "project-a",
				Content:            "shared alice note",
				Tags:               []string{"semantic"},
				OwnerPrincipal:     "agent/alice",
				OwnerPrincipalKind: "agent",
				AgentVisibility:    models.AgentVisibilityShared,
				Confidence:         0.8,
				CreatedAt:          time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC),
			},
			{
				ID:      103,
				Project: "project-a",
				Content: "legacy row without principal metadata",
			},
		},
	}
	svc := NewPrincipalMemoryQueryService(store, &fakeAuditLogger{})

	result, err := svc.Query(context.Background(), PrincipalMemoryQueryRequest{
		Project:            "project-a",
		Caller:             PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		Limit:              2,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "agent/alice", result.Principal)
	assert.Equal(t, "agent", result.PrincipalKind)
	assert.Equal(t, "project-a", result.Project)
	assert.Empty(t, result.Domain)
	assert.Equal(t, "principal_memory_query", result.Audit.Action)
	assert.False(t, result.Audit.Durable)
	require.Len(t, result.Items, 1, "visible output must be bounded by request limit")
	assert.Equal(t, 2, result.HiddenCount)
	assert.Equal(t, "not_required", result.AuditStatus)
	assert.Equal(t, int64(102), result.Items[0].ID)
	assert.Equal(t, "agent/alice", result.Items[0].OwnerPrincipal)
	assert.Equal(t, []string{"semantic"}, result.Items[0].Tags)
	assert.Equal(t, 0.8, result.Items[0].Confidence)
	assert.Equal(t, time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC), result.Items[0].CreatedAt)

	assert.Equal(t, "project-a", store.project)
	assert.Equal(t, "agent/alice", store.opts.OwnerPrincipal)
	assert.Equal(t, "agent", store.opts.OwnerPrincipalKind)
	assert.Empty(t, store.opts.Domain)
	assert.GreaterOrEqual(t, store.opts.Limit, 2, "service may over-fetch to compute hidden_count but must bound output")
}

func TestPrincipalMemoryQueryService_PagesPastHiddenRows(t *testing.T) {
	hiddenRows := make([]*models.Memory, 0, 51)
	for i := int64(0); i < 51; i++ {
		hiddenRows = append(hiddenRows, &models.Memory{
			ID:                 400 + i,
			Project:            "project-a",
			Content:            "private alice memory",
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			AgentVisibility:    models.AgentVisibilityPrivate,
		})
	}
	store := &fakePrincipalMemoryStore{
		batches: [][]*models.Memory{
			hiddenRows,
			{
				{
					ID:                 501,
					Project:            "project-a",
					Content:            "shared alice memory",
					OwnerPrincipal:     "agent/alice",
					OwnerPrincipalKind: "agent",
					AgentVisibility:    models.AgentVisibilityShared,
				},
			},
		},
	}
	svc := NewPrincipalMemoryQueryService(store, &fakeAuditLogger{})

	result, err := svc.Query(context.Background(), PrincipalMemoryQueryRequest{
		Project:            "project-a",
		Caller:             PrincipalRef{Principal: "agent/bob", PrincipalKind: "agent"},
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		Limit:              1,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Items, 1)
	assert.Equal(t, int64(501), result.Items[0].ID)
	assert.Equal(t, 51, result.HiddenCount)
	require.Len(t, store.calls, 2, "service must continue paging when a full hidden batch does not fill the visible limit")
	assert.Equal(t, 0, store.calls[0].Offset)
	assert.Equal(t, 51, store.calls[1].Offset)
}

func TestPrincipalMemoryQueryService_HidesDomainOwnedRowsFromMismatchedCaller(t *testing.T) {
	store := &fakePrincipalMemoryStore{
		rows: []*models.Memory{
			{
				ID:                 601,
				Project:            "project-a",
				Content:            "shared bob domain memory",
				OwnerPrincipal:     "agent/bob",
				OwnerPrincipalKind: "agent",
				AgentVisibility:    models.AgentVisibilityShared,
				Domain:             "memory-lab",
			},
		},
	}
	svc := NewPrincipalMemoryQueryService(store, &fakeAuditLogger{})

	result, err := svc.Query(context.Background(), PrincipalMemoryQueryRequest{
		Project:            "project-a",
		Caller:             PrincipalRef{Principal: "agent/alice", PrincipalKind: "agent"},
		OwnerPrincipal:     "agent/bob",
		OwnerPrincipalKind: "agent",
		Domain:             "memory-lab",
		Limit:              1,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Empty(t, result.Items)
	assert.Equal(t, 1, result.HiddenCount)
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
		IncludePrivate:     true,
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

func TestPrincipalMemoryQueryService_AdminPrivateWideningMustBeExplicit(t *testing.T) {
	store := &fakePrincipalMemoryStore{
		rows: []*models.Memory{
			{
				ID:                 251,
				Project:            "project-a",
				Content:            "private alice memory",
				OwnerPrincipal:     "agent/alice",
				OwnerPrincipalKind: "agent",
				AgentVisibility:    models.AgentVisibilityPrivate,
			},
			{
				ID:                 252,
				Project:            "project-a",
				Content:            "shared alice memory",
				OwnerPrincipal:     "agent/alice",
				OwnerPrincipalKind: "agent",
				AgentVisibility:    models.AgentVisibilityShared,
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
		Limit:              2,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.Items, 1)
	assert.Equal(t, int64(252), result.Items[0].ID)
	assert.Equal(t, 1, result.HiddenCount)
	assert.Equal(t, AuditStatusNotRequired, result.AuditStatus)
	assert.False(t, result.Audit.Durable)
	assert.Empty(t, audit.entries, "no durable private-read audit should be written when private widening was not explicitly requested")
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
		IncludePrivate:     true,
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

func TestPrincipalMemoryQueryService_IDBoundedProjectionIncludesSupersededRows(t *testing.T) {
	store := &statusFilteringPrincipalMemoryStore{rows: []*models.Memory{
		{
			ID:                 701,
			Project:            "project-a",
			Content:            "superseded provenance row",
			Status:             "superseded",
			OwnerPrincipal:     "agent/alice",
			OwnerPrincipalKind: "agent",
			AgentVisibility:    models.AgentVisibilityShared,
			Domain:             "release",
		},
	}}
	svc := NewPrincipalMemoryQueryService(store, &fakeAuditLogger{})

	result, err := svc.Query(context.Background(), PrincipalMemoryQueryRequest{
		Project:           "project-a",
		Caller:            PrincipalRef{Principal: "agent/alice", PrincipalKind: "agent"},
		IDs:               []int64{701},
		IncludeSuperseded: true,
		Limit:             1,
	})

	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.Equal(t, int64(701), result.Items[0].ID)
}

func TestPrincipalMemoryQueryService_IDBoundedProjectionPassesIncludeExpired(t *testing.T) {
	store := &fakePrincipalMemoryStore{rows: []*models.Memory{{
		ID:                 702,
		Project:            "project-a",
		Content:            "expired provenance row",
		Status:             "superseded",
		OwnerPrincipal:     "agent/alice",
		OwnerPrincipalKind: "agent",
		AgentVisibility:    models.AgentVisibilityShared,
		Domain:             "release",
	}}}
	svc := NewPrincipalMemoryQueryService(store, &fakeAuditLogger{})

	result, err := svc.Query(context.Background(), PrincipalMemoryQueryRequest{
		Project:           "project-a",
		Caller:            PrincipalRef{Principal: "agent/alice", PrincipalKind: "agent"},
		IDs:               []int64{702},
		IncludeSuperseded: true,
		IncludeExpired:    true,
		Limit:             1,
	})

	require.NoError(t, err)
	require.Len(t, result.Items, 1)
	assert.True(t, store.called)
	assert.True(t, store.opts.IncludeExpired)
}

type statusFilteringPrincipalMemoryStore struct {
	rows []*models.Memory
}

func (f *statusFilteringPrincipalMemoryStore) ListPrincipalMemory(_ context.Context, project string, opts gormdb.ListOptions) ([]*models.Memory, error) {
	idSet := make(map[int64]struct{}, len(opts.IDs))
	for _, id := range opts.IDs {
		idSet[id] = struct{}{}
	}
	result := make([]*models.Memory, 0, len(f.rows))
	for _, row := range f.rows {
		if row == nil || row.Project != project {
			continue
		}
		if len(idSet) > 0 {
			if _, ok := idSet[row.ID]; !ok {
				continue
			}
		}
		if opts.IncludeSuperseded {
			if row.Status != "active" && row.Status != "superseded" {
				continue
			}
		} else if row.Status != "active" {
			continue
		}
		result = append(result, row)
	}
	return result, nil
}

type fakePrincipalMemoryStore struct {
	rows    []*models.Memory
	batches [][]*models.Memory
	project string
	opts    gormdb.ListOptions
	calls   []gormdb.ListOptions
	called  bool
}

func (f *fakePrincipalMemoryStore) ListPrincipalMemory(ctx context.Context, project string, opts gormdb.ListOptions) ([]*models.Memory, error) {
	f.called = true
	f.project = project
	f.opts = opts
	f.calls = append(f.calls, opts)
	if len(f.batches) > 0 {
		idx := len(f.calls) - 1
		if idx >= len(f.batches) {
			return nil, nil
		}
		return f.batches[idx], nil
	}
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
