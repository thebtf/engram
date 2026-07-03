package worker

import (
	"context"
	"fmt"
	"strings"
	"time"

	gormdb "github.com/thebtf/engram/internal/db/gorm"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/internal/temporaltruth"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

type temporalTruthStore interface {
	LoadStoredRecords(ctx context.Context, request cognitive.TemporalTruthQueryRequest) ([]gormdb.TemporalTruthStoredRecord, error)
	RefreshProject(ctx context.Context, project string) (gormdb.TemporalTruthAdmissionResult, error)
}

type temporalTruthPrincipalQueryService interface {
	Query(ctx context.Context, req principalmemory.PrincipalMemoryQueryRequest) (*principalmemory.PrincipalMemoryQueryResult, error)
}

type memoryTemporalTruthProvider struct {
	store    temporalTruthStore
	querySvc temporalTruthPrincipalQueryService
}

var _ cognitive.TemporalTruthProvider = (*memoryTemporalTruthProvider)(nil)

func newMemoryTemporalTruthProvider(store temporalTruthStore, querySvc temporalTruthPrincipalQueryService) *memoryTemporalTruthProvider {
	return &memoryTemporalTruthProvider{store: store, querySvc: querySvc}
}

func (p *memoryTemporalTruthProvider) QueryTemporalTruth(ctx context.Context, request cognitive.TemporalTruthQueryRequest) (cognitive.TemporalTruthResponse, error) {
	service, err := p.serviceForRequest(ctx, request)
	if err != nil {
		return cognitive.TemporalTruthResponse{}, err
	}
	return service.QueryTemporalTruth(ctx, request)
}

func (p *memoryTemporalTruthProvider) RefreshProject(ctx context.Context, project string) (gormdb.TemporalTruthAdmissionResult, error) {
	if p == nil || p.store == nil {
		return gormdb.TemporalTruthAdmissionResult{}, fmt.Errorf("temporal truth provider: store is not configured")
	}
	return p.store.RefreshProject(ctx, strings.TrimSpace(project))
}

func (p *memoryTemporalTruthProvider) serviceForRequest(ctx context.Context, request cognitive.TemporalTruthQueryRequest) (*temporaltruth.Service, error) {
	if p == nil || p.store == nil {
		return nil, fmt.Errorf("temporal truth provider: store is not configured")
	}
	if p.querySvc == nil {
		return nil, fmt.Errorf("temporal truth provider: principal memory query service is not configured")
	}
	stored, err := p.store.LoadStoredRecords(ctx, request)
	if err != nil {
		return nil, err
	}
	projected, err := p.projectVisibleRecords(ctx, request, stored)
	if err != nil {
		return nil, err
	}
	return temporaltruth.NewService(projected), nil
}

func (p *memoryTemporalTruthProvider) projectVisibleRecords(ctx context.Context, request cognitive.TemporalTruthQueryRequest, stored []gormdb.TemporalTruthStoredRecord) ([]temporaltruth.Record, error) {
	if len(stored) == 0 {
		return nil, nil
	}
	project := strings.TrimSpace(request.Project)
	if project == "" {
		project = strings.TrimSpace(stored[0].Project)
	}
	ids := temporalTruthSourceMemoryIDs(stored)
	visibleByID := make(map[int64]*models.Memory, len(ids))
	if len(ids) > 0 {
		caller, callerIsAdmin := principalMemoryQueryCallerFromContext(ctx)
		result, err := p.querySvc.Query(ctx, principalmemory.PrincipalMemoryQueryRequest{
			Project:           project,
			Caller:            caller,
			CallerIsAdmin:     callerIsAdmin,
			IDs:               ids,
			IncludeSuperseded: true,
			Limit:             len(ids),
		})
		if err != nil {
			return nil, err
		}
		for _, item := range result.Items {
			mem := item.Memory()
			if mem == nil {
				continue
			}
			visibleByID[mem.ID] = mem
		}
	}
	records := make([]temporaltruth.Record, 0, len(stored))
	for _, row := range stored {
		provenance := temporalTruthVisibleProvenance(row, visibleByID)
		if len(provenance) == 0 {
			continue
		}
		records = append(records, temporaltruth.Record{
			FactID:                row.FactID,
			FactClass:             row.FactClass,
			Project:               row.Project,
			Value:                 row.Value,
			ValidFrom:             row.ValidFrom,
			ValidUntil:            cloneTemporalTruthTime(row.ValidUntil),
			InvalidatedAt:         cloneTemporalTruthTime(row.InvalidatedAt),
			InvalidationRationale: row.InvalidationRationale,
			Provenance:            provenance,
		})
	}
	return records, nil
}

func temporalTruthSourceMemoryIDs(stored []gormdb.TemporalTruthStoredRecord) []int64 {
	if len(stored) == 0 {
		return nil
	}
	seen := make(map[int64]struct{})
	ids := make([]int64, 0)
	for _, row := range stored {
		for _, id := range row.SourceMemoryIDs {
			if id <= 0 {
				continue
			}
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids
}

func temporalTruthVisibleProvenance(row gormdb.TemporalTruthStoredRecord, visibleByID map[int64]*models.Memory) []cognitive.TemporalTruthProvenance {
	if len(row.SourceMemoryIDs) == 0 {
		return nil
	}
	provenance := make([]cognitive.TemporalTruthProvenance, 0, len(row.SourceMemoryIDs))
	for _, id := range row.SourceMemoryIDs {
		mem := visibleByID[id]
		if mem == nil {
			continue
		}
		entry := cognitive.TemporalTruthProvenance{
			Kind:       "memory",
			ID:         fmt.Sprintf("memory:%d", mem.ID),
			Project:    mem.Project,
			ObservedAt: mem.CreatedAt,
		}
		if len(mem.SourceSessions) > 0 {
			entry.SessionID = mem.SourceSessions[0]
		}
		provenance = append(provenance, entry)
	}
	return provenance
}

func cloneTemporalTruthTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
