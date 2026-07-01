package worker

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/thebtf/engram/internal/auth"
	experiencehistory "github.com/thebtf/engram/internal/experience"
	"github.com/thebtf/engram/internal/principalmemory"
	"github.com/thebtf/engram/pkg/cognitive"
	"github.com/thebtf/engram/pkg/models"
)

const experienceProjectionFetchLimit = 500

type experiencePrincipalQueryService interface {
	Query(ctx context.Context, req principalmemory.PrincipalMemoryQueryRequest) (*principalmemory.PrincipalMemoryQueryResult, error)
}

type memoryExperienceProvider struct {
	querySvc experiencePrincipalQueryService
}

func newMemoryExperienceProvider(querySvc experiencePrincipalQueryService) *memoryExperienceProvider {
	return &memoryExperienceProvider{querySvc: querySvc}
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
	if p == nil || p.querySvc == nil {
		return nil, fmt.Errorf("experience provider: principal memory query service is not configured")
	}
	project := strings.TrimSpace(request.Project)
	if project == "" {
		return experiencehistory.NewService(nil), nil
	}
	caller, callerIsAdmin := principalMemoryQueryCallerFromContext(ctx)
	ownerPrincipal := strings.TrimSpace(request.Principal)
	ownerKind := ""
	if ownerPrincipal != "" && caller.Principal == ownerPrincipal {
		ownerKind = caller.PrincipalKind
	}
	result, err := p.querySvc.Query(ctx, principalmemory.PrincipalMemoryQueryRequest{
		Project:            project,
		Caller:             caller,
		CallerIsAdmin:      callerIsAdmin,
		OwnerPrincipal:     ownerPrincipal,
		OwnerPrincipalKind: ownerKind,
		Query:              strings.TrimSpace(request.Query),
		Domain:             strings.TrimSpace(request.Domain),
		Limit:              experienceProjectionFetchLimit,
	})
	if err != nil {
		return nil, err
	}
	return experiencehistory.NewService(memoriesToExperienceResponses(principalQueryMemories(result))), nil
}

func principalMemoryQueryCallerFromContext(ctx context.Context) (principalmemory.PrincipalRef, bool) {
	id, ok := auth.IdentityFrom(ctx)
	if !ok {
		return principalmemory.PrincipalRef{}, false
	}
	principal, kind, hasOwner := id.MemoryOwner()
	if !hasOwner {
		if id.IsAdmin() {
			return principalmemory.PrincipalRef{Principal: "system", PrincipalKind: "service"}, true
		}
		return principalmemory.PrincipalRef{}, id.IsAdmin()
	}
	return principalmemory.PrincipalRef{Principal: principal, PrincipalKind: kind}, id.IsAdmin()
}

func principalQueryMemories(result *principalmemory.PrincipalMemoryQueryResult) []*models.Memory {
	if result == nil || len(result.Items) == 0 {
		return nil
	}
	memories := make([]*models.Memory, 0, len(result.Items))
	for _, item := range result.Items {
		memories = append(memories, item.Memory())
	}
	return memories
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
