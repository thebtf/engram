package worker

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/thebtf/engram/internal/db/gorm"
	experiencehistory "github.com/thebtf/engram/internal/experience"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

const experienceProjectionFetchLimit = 500

type experienceMemoryStore interface {
	ListPrincipalMemory(ctx context.Context, project string, opts gorm.ListOptions) ([]*models.Memory, error)
}

type memoryExperienceProvider struct {
	memoryStore experienceMemoryStore
}

func newMemoryExperienceProvider(memoryStore experienceMemoryStore) *memoryExperienceProvider {
	return &memoryExperienceProvider{memoryStore: memoryStore}
}

func (p *memoryExperienceProvider) QueryExperience(ctx context.Context, request cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, error) {
	service, err := p.serviceForRequest(ctx, request)
	if err != nil {
		return nil, err
	}
	return service.QueryExperience(ctx, request)
}

func (p *memoryExperienceProvider) QueryExperienceWithArchiveEvidence(ctx context.Context, request cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, []experiencehistory.ArchiveEvidenceEntry, error) {
	service, err := p.serviceForRequest(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	return service.QueryExperienceWithArchiveEvidence(ctx, request)
}

func (p *memoryExperienceProvider) QueryExperienceDetail(ctx context.Context, request experiencehistory.HistoryDetailRequest) (cognitive.ExperienceResponse, []experiencehistory.ArchiveEvidenceEntry, bool, error) {
	service, err := p.serviceForRequest(ctx, cognitive.ExperienceQueryRequest{
		Project:               request.Project,
		Principal:             request.Principal,
		Domain:                request.Domain,
		Query:                 request.ExperienceID,
		CurrentContext:        request.CurrentContext,
		ArchiveTriggerClasses: request.ArchiveTriggerClasses,
		Limit:                 experiencehistory.MaxQueryLimit,
	})
	if err != nil {
		return cognitive.ExperienceResponse{}, nil, false, err
	}
	return service.QueryExperienceDetail(ctx, request)
}

func (p *memoryExperienceProvider) serviceForRequest(ctx context.Context, request cognitive.ExperienceQueryRequest) (*experiencehistory.Service, error) {
	if p == nil || p.memoryStore == nil {
		return nil, fmt.Errorf("experience provider: memory store is not configured")
	}
	project := strings.TrimSpace(request.Project)
	if project == "" {
		return experiencehistory.NewService(nil), nil
	}
	memories, err := p.memoryStore.ListPrincipalMemory(ctx, project, gorm.ListOptions{
		OwnerPrincipal: strings.TrimSpace(request.Principal),
		Domain:         strings.TrimSpace(request.Domain),
		Limit:          experienceProjectionFetchLimit,
	})
	if err != nil {
		return nil, err
	}
	return experiencehistory.NewService(memoriesToExperienceResponses(memories)), nil
}

func memoriesToExperienceResponses(memories []*models.Memory) []cognitive.ExperienceResponse {
	responses := make([]cognitive.ExperienceResponse, 0, len(memories))
	for _, memory := range memories {
		if memory == nil || strings.TrimSpace(memory.Content) == "" {
			continue
		}
		responses = append(responses, memoryToExperienceResponse(memory))
	}
	return responses
}

func memoryToExperienceResponse(memory *models.Memory) cognitive.ExperienceResponse {
	attribution := cognitive.ExperienceSourceAttribution{
		Kind:      "memory",
		ID:        strconv.FormatInt(memory.ID, 10),
		Project:   memory.Project,
		CreatedAt: memory.CreatedAt,
	}
	if len(memory.SourceSessions) > 0 {
		attribution.SessionID = memory.SourceSessions[0]
	}
	return cognitive.ExperienceResponse{
		Source:        cognitive.ExperienceSourceProjection,
		StorageOrigin: cognitive.ExperienceSourceProjection,
		Situation:     "stored memory",
		TimeSpan: cognitive.ExperienceTimeSpan{
			StartedAt: memory.CreatedAt,
			EndedAt:   memory.UpdatedAt,
		},
		Outcome: memory.Status,
		Lesson:  memory.Content,
		Applicability: cognitive.ExperienceApplicability{
			State:           cognitive.ExperienceApplicabilityUncertain,
			Rationale:       "projected from stored memory; current_context decides applicability",
			RequiredContext: []string{"current_context"},
			Confidence:      "low",
		},
		Provenance:        []cognitive.ExperienceSourceAttribution{attribution},
		SourceAttribution: []cognitive.ExperienceSourceAttribution{attribution},
	}
}
