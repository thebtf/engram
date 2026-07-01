package experience

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/thebtf/engram/pkg/cognitive"
)

// HistoryState is the honest state exposed by first-class experience/history
// read adapters. It deliberately distinguishes empty, gated, blocked, and error
// states so callers do not infer silent hot-memory fallback behavior.
type HistoryState string

const (
	HistoryStateLive                 HistoryState = "live"
	HistoryStateEmptyResults         HistoryState = "empty_results"
	HistoryStateEmptyArchive         HistoryState = "empty_archive"
	HistoryStateGated                HistoryState = "gated"
	HistoryStateBlockedApplicability HistoryState = "blocked_applicability"
	HistoryStateError                HistoryState = "error"
)

// HistoryReadResponse is the first-class read contract used by MCP and REST
// adapters. Results are bounded by the service provider and remain separate
// from ordinary hot-memory retrieval.
type HistoryReadResponse struct {
	State        HistoryState                     `json:"state"`
	QueryEcho    cognitive.ExperienceQueryRequest `json:"query_echo"`
	Results      []HistoryResult                  `json:"results"`
	Count        int                              `json:"count"`
	Limit        int                              `json:"limit"`
	TriggerTrace TriggerTrace                     `json:"trigger_trace"`
	ArchiveTrace ArchiveTrace                     `json:"archive_trace"`
	Freshness    string                           `json:"freshness"`
	GeneratedAt  time.Time                        `json:"generated_at"`
	Error        string                           `json:"error,omitempty"`
}

// HistoryDetailRequest scopes a read-only detail lookup for one projected
// experience id. The id may be the adapter experience_id or a raw provenance id.
type HistoryDetailRequest struct {
	Project               string                                    `json:"project"`
	Principal             string                                    `json:"principal,omitempty"`
	Domain                string                                    `json:"domain,omitempty"`
	ExperienceID          string                                    `json:"experience_id"`
	CurrentContext        string                                    `json:"current_context,omitempty"`
	ArchiveTriggerClasses []cognitive.ExperienceArchiveTriggerClass `json:"archive_trigger_classes,omitempty"`
}

// HistoryDetailResponse is the read-only detail contract for one experience.
type HistoryDetailResponse struct {
	State                 HistoryState                      `json:"state"`
	ExperienceID          string                            `json:"experience_id"`
	ExperienceDetail      *HistoryResult                    `json:"experience_detail,omitempty"`
	ApplicabilityEvidence cognitive.ExperienceApplicability `json:"applicability_evidence"`
	ArchiveTrace          ArchiveTrace                      `json:"archive_trace"`
	ProvenanceRefs        []string                          `json:"provenance_refs"`
	StorageOrigin         cognitive.ExperienceSource        `json:"storage_origin,omitempty"`
	Freshness             string                            `json:"freshness"`
	GeneratedAt           time.Time                         `json:"generated_at"`
	Error                 string                            `json:"error,omitempty"`
}

// HistoryResult is a bounded lesson row plus enough detail to render the
// CR-009 read surface without inferring applicability or storage shape locally.
type HistoryResult struct {
	ExperienceID          string                                    `json:"experience_id"`
	State                 HistoryState                              `json:"state"`
	Source                cognitive.ExperienceSource                `json:"source"`
	StorageOrigin         cognitive.ExperienceSource                `json:"storage_origin"`
	Situation             string                                    `json:"situation"`
	TimeSpan              cognitive.ExperienceTimeSpan              `json:"time_span"`
	Decision              string                                    `json:"decision"`
	Action                string                                    `json:"action"`
	Outcome               string                                    `json:"outcome"`
	Revision              string                                    `json:"revision"`
	Reversal              string                                    `json:"reversal"`
	Lesson                string                                    `json:"lesson"`
	ApplicabilityOutcome  cognitive.ExperienceApplicabilityState    `json:"applicability_outcome"`
	Applicability         cognitive.ExperienceApplicability         `json:"applicability"`
	AntiApplicability     []cognitive.ExperienceAntiApplicability   `json:"anti_applicability"`
	Provenance            []cognitive.ExperienceSourceAttribution   `json:"provenance"`
	SourceAttribution     []cognitive.ExperienceSourceAttribution   `json:"source_attribution"`
	ProvenanceRefs        []string                                  `json:"provenance_refs"`
	ArchiveTriggerClasses []cognitive.ExperienceArchiveTriggerClass `json:"archive_trigger_classes"`
}

// TriggerTrace records why archive search was or was not eligible for a read.
type TriggerTrace struct {
	TriggerClasses        []cognitive.ExperienceArchiveTriggerClass `json:"trigger_classes"`
	NamedTriggerProvided  bool                                      `json:"named_trigger_provided"`
	ArchiveEligible       bool                                      `json:"archive_eligible"`
	ExplicitArchiveLookup bool                                      `json:"explicit_archive_lookup"`
}

// ArchiveTrace is the bounded archive resurfacing evidence surfaced to adapters.
type ArchiveTrace struct {
	TriggerClasses             []cognitive.ExperienceArchiveTriggerClass `json:"trigger_classes"`
	ArchiveUsed                bool                                      `json:"archive_used"`
	ResultCap                  int                                       `json:"result_cap"`
	SurfacedCount              int                                       `json:"surfaced_count"`
	BlockedByAntiApplicability bool                                      `json:"blocked_by_anti_applicability"`
	EvidenceRefs               []string                                  `json:"evidence_refs"`
	Status                     string                                    `json:"status"`
	Reason                     string                                    `json:"reason"`
}

type archiveEvidenceProvider interface {
	QueryExperienceWithArchiveEvidence(ctx context.Context, request cognitive.ExperienceQueryRequest) ([]cognitive.ExperienceResponse, []ArchiveEvidenceEntry, error)
}

type experienceDetailProvider interface {
	QueryExperienceDetail(ctx context.Context, request HistoryDetailRequest) (cognitive.ExperienceResponse, []ArchiveEvidenceEntry, bool, error)
}

// ReadHistory executes a bounded first-class experience/history read over the
// supplied provider. It never calls hot-memory retrieval and never enables
// archive search unless request.ArchiveTriggerClasses contains a named trigger.
func ReadHistory(ctx context.Context, provider cognitive.ExperienceProvider, request cognitive.ExperienceQueryRequest, now time.Time) (HistoryReadResponse, error) {
	request = normalizeHistoryRequest(request)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	triggers, err := NormalizeArchiveTriggerClasses(request.ArchiveTriggerClasses)
	if err != nil {
		return HistoryReadResponse{}, err
	}
	request.ArchiveTriggerClasses = triggers
	limit := normalizeExperienceLimit(request.Limit, len(triggers) > 0)
	response := HistoryReadResponse{
		State:        HistoryStateLive,
		QueryEcho:    request,
		Results:      []HistoryResult{},
		Limit:        limit,
		TriggerTrace: triggerTrace(triggers),
		ArchiveTrace: skippedArchiveTrace(triggers, limit),
		Freshness:    "live",
		GeneratedAt:  now,
	}
	if provider == nil {
		response.State = HistoryStateGated
		response.Error = "experience provider not configured"
		response.ArchiveTrace.Status = "gated"
		response.ArchiveTrace.Reason = response.Error
		return response, nil
	}
	if request.Project == "" {
		return response, fmt.Errorf("experience_history.read requires project")
	}
	if !hasHistoryQueryTerms(request) && len(triggers) == 0 {
		return response, fmt.Errorf("experience_history.read requires query text or a named archive trigger")
	}

	var items []cognitive.ExperienceResponse
	if evidenceProvider, ok := provider.(archiveEvidenceProvider); ok && evidenceProvider != nil {
		var evidence []ArchiveEvidenceEntry
		items, evidence, err = evidenceProvider.QueryExperienceWithArchiveEvidence(ctx, request)
		response.ArchiveTrace = archiveTraceFromEvidence(evidence, triggers, limit)
	} else {
		items, err = provider.QueryExperience(ctx, request)
		response.ArchiveTrace = skippedArchiveTrace(triggers, limit)
	}
	if err != nil {
		response.State = HistoryStateError
		response.Error = err.Error()
		return response, nil
	}

	response.Results = make([]HistoryResult, 0, len(items))
	for _, item := range items {
		response.Results = append(response.Results, historyResultFromExperience(item))
	}
	response.Count = len(response.Results)
	response.State = historyStateFor(response.Results, response.ArchiveTrace)
	return response, nil
}

// ReadHistoryDetail loads one experience detail through the same first-class
// provider, preserving archive trace and anti-applicability state.
func ReadHistoryDetail(ctx context.Context, provider cognitive.ExperienceProvider, request HistoryDetailRequest, now time.Time) (HistoryDetailResponse, error) {
	request.Project = strings.TrimSpace(request.Project)
	request.Principal = strings.TrimSpace(request.Principal)
	request.Domain = strings.TrimSpace(request.Domain)
	request.ExperienceID = strings.TrimSpace(request.ExperienceID)
	request.CurrentContext = strings.TrimSpace(request.CurrentContext)
	if now.IsZero() {
		now = time.Now().UTC()
	}
	triggers, err := NormalizeArchiveTriggerClasses(request.ArchiveTriggerClasses)
	if err != nil {
		return HistoryDetailResponse{}, err
	}
	request.ArchiveTriggerClasses = triggers
	response := HistoryDetailResponse{
		State:        HistoryStateLive,
		ExperienceID: request.ExperienceID,
		ArchiveTrace: skippedArchiveTrace(triggers, MaxQueryLimit),
		Freshness:    "live",
		GeneratedAt:  now,
	}
	if request.ExperienceID == "" {
		return response, fmt.Errorf("experience_history.detail requires experience_id")
	}
	if provider == nil {
		response.State = HistoryStateGated
		response.Error = "experience provider not configured"
		response.ArchiveTrace.Status = "gated"
		response.ArchiveTrace.Reason = response.Error
		return response, nil
	}
	if request.Project == "" {
		return response, fmt.Errorf("experience_history.detail requires project")
	}
	if detailProvider, ok := provider.(experienceDetailProvider); ok && detailProvider != nil {
		item, evidence, found, err := detailProvider.QueryExperienceDetail(ctx, request)
		response.ArchiveTrace = archiveTraceFromEvidence(evidence, triggers, MaxQueryLimit)
		if err != nil {
			response.State = HistoryStateError
			response.Error = err.Error()
			return response, nil
		}
		if found {
			result := historyResultFromExperience(item)
			response.State = detailStateFor(result, response.ArchiveTrace)
			if result.ExperienceID != "" {
				response.ExperienceID = result.ExperienceID
			}
			response.ExperienceDetail = &result
			response.ApplicabilityEvidence = result.Applicability
			response.ProvenanceRefs = append([]string(nil), result.ProvenanceRefs...)
			response.StorageOrigin = result.StorageOrigin
			return response, nil
		}
		response.State = historyStateFor(nil, response.ArchiveTrace)
		if response.State != HistoryStateGated && response.State != HistoryStateError {
			response.State = HistoryStateEmptyResults
			response.Error = "experience detail not found"
		}
		return response, nil
	}

	read, err := ReadHistory(ctx, provider, cognitive.ExperienceQueryRequest{
		Project:               request.Project,
		Principal:             request.Principal,
		Domain:                request.Domain,
		Query:                 request.ExperienceID,
		CurrentContext:        request.CurrentContext,
		ArchiveTriggerClasses: request.ArchiveTriggerClasses,
		Limit:                 MaxQueryLimit,
	}, now)
	response.State = read.State
	response.ArchiveTrace = read.ArchiveTrace
	response.Freshness = read.Freshness
	response.GeneratedAt = read.GeneratedAt
	response.Error = read.Error
	if err != nil {
		return response, err
	}
	for _, result := range read.Results {
		if historyResultMatchesID(result, request.ExperienceID) {
			matched := result
			response.State = detailStateFor(result, response.ArchiveTrace)
			if result.ExperienceID != "" {
				response.ExperienceID = result.ExperienceID
			}
			response.ExperienceDetail = &matched
			response.ApplicabilityEvidence = result.Applicability
			response.ProvenanceRefs = append([]string(nil), result.ProvenanceRefs...)
			response.StorageOrigin = result.StorageOrigin
			return response, nil
		}
	}
	if response.State != HistoryStateGated && response.State != HistoryStateError {
		response.State = HistoryStateEmptyResults
		response.Error = "experience detail not found"
	}
	return response, nil
}

func normalizeHistoryRequest(request cognitive.ExperienceQueryRequest) cognitive.ExperienceQueryRequest {
	request.Project = strings.TrimSpace(request.Project)
	request.Principal = strings.TrimSpace(request.Principal)
	request.Domain = strings.TrimSpace(request.Domain)
	request.Query = strings.TrimSpace(request.Query)
	request.CurrentContext = strings.TrimSpace(request.CurrentContext)
	request.Situation = strings.TrimSpace(request.Situation)
	request.Decision = strings.TrimSpace(request.Decision)
	request.Action = strings.TrimSpace(request.Action)
	request.Outcome = strings.TrimSpace(request.Outcome)
	request.Revision = strings.TrimSpace(request.Revision)
	request.Reversal = strings.TrimSpace(request.Reversal)
	return request
}

func hasHistoryQueryTerms(request cognitive.ExperienceQueryRequest) bool {
	return request.Query != "" || request.CurrentContext != "" || request.Situation != "" || request.Decision != "" || request.Action != "" || request.Outcome != "" || request.Revision != "" || request.Reversal != ""
}

func triggerTrace(triggers []cognitive.ExperienceArchiveTriggerClass) TriggerTrace {
	return TriggerTrace{
		TriggerClasses:        append([]cognitive.ExperienceArchiveTriggerClass(nil), triggers...),
		NamedTriggerProvided:  len(triggers) > 0,
		ArchiveEligible:       len(triggers) > 0,
		ExplicitArchiveLookup: slices.Contains(triggers, cognitive.ExperienceArchiveTriggerExplicitLookup),
	}
}

func skippedArchiveTrace(triggers []cognitive.ExperienceArchiveTriggerClass, limit int) ArchiveTrace {
	if len(triggers) == 0 {
		return ArchiveTrace{
			TriggerClasses: []cognitive.ExperienceArchiveTriggerClass{},
			ArchiveUsed:    false,
			ResultCap:      archiveLimit(limit),
			Status:         "archive_skipped",
			Reason:         "no named archive trigger supplied",
		}
	}
	return ArchiveTrace{
		TriggerClasses: append([]cognitive.ExperienceArchiveTriggerClass(nil), triggers...),
		ArchiveUsed:    false,
		ResultCap:      archiveLimit(limit),
		Status:         "archive_trace_unavailable",
		Reason:         "experience provider did not expose archive evidence",
		EvidenceRefs:   archiveTriggerEvidenceRefs(triggers),
	}
}

func archiveTraceFromEvidence(evidence []ArchiveEvidenceEntry, triggers []cognitive.ExperienceArchiveTriggerClass, limit int) ArchiveTrace {
	if len(evidence) == 0 {
		return skippedArchiveTrace(triggers, limit)
	}
	entry := evidence[len(evidence)-1]
	return ArchiveTrace{
		TriggerClasses:             append([]cognitive.ExperienceArchiveTriggerClass(nil), entry.TriggerClasses...),
		ArchiveUsed:                entry.ExperienceRetrievalRan,
		ResultCap:                  entry.RequestedLimit,
		SurfacedCount:              entry.Returned,
		BlockedByAntiApplicability: entry.AntiApplicabilityBlocked,
		EvidenceRefs:               append([]string(nil), entry.EvidenceRefs...),
		Status:                     entry.Status,
		Reason:                     entry.Reason,
	}
}

func detailStateFor(result HistoryResult, archiveTrace ArchiveTrace) HistoryState {
	if archiveTrace.Status == "archive_unavailable" || archiveTrace.Status == "archive_trace_unavailable" {
		return HistoryStateGated
	}
	return result.State
}

func historyStateFor(results []HistoryResult, archiveTrace ArchiveTrace) HistoryState {
	if archiveTrace.Status == "archive_unavailable" || archiveTrace.Status == "archive_trace_unavailable" {
		return HistoryStateGated
	}
	if len(results) == 0 {
		if archiveTrace.ArchiveUsed || len(archiveTrace.TriggerClasses) > 0 {
			return HistoryStateEmptyArchive
		}
		return HistoryStateEmptyResults
	}
	for _, result := range results {
		if result.ApplicabilityOutcome == cognitive.ExperienceApplicabilityBlocked {
			return HistoryStateBlockedApplicability
		}
	}
	return HistoryStateLive
}

func historyResultFromExperience(item cognitive.ExperienceResponse) HistoryResult {
	storageOrigin := item.StorageOrigin
	if storageOrigin == "" {
		storageOrigin = item.Source
	}
	state := HistoryStateLive
	if item.Applicability.State == cognitive.ExperienceApplicabilityBlocked {
		state = HistoryStateBlockedApplicability
	}
	return HistoryResult{
		ExperienceID:          experienceID(item),
		State:                 state,
		Source:                item.Source,
		StorageOrigin:         storageOrigin,
		Situation:             item.Situation,
		TimeSpan:              item.TimeSpan,
		Decision:              item.Decision,
		Action:                item.Action,
		Outcome:               item.Outcome,
		Revision:              item.Revision,
		Reversal:              item.Reversal,
		Lesson:                item.Lesson,
		ApplicabilityOutcome:  item.Applicability.State,
		Applicability:         item.Applicability,
		AntiApplicability:     append([]cognitive.ExperienceAntiApplicability(nil), item.AntiApplicability...),
		Provenance:            append([]cognitive.ExperienceSourceAttribution(nil), item.Provenance...),
		SourceAttribution:     append([]cognitive.ExperienceSourceAttribution(nil), item.SourceAttribution...),
		ProvenanceRefs:        provenanceRefs(item),
		ArchiveTriggerClasses: append([]cognitive.ExperienceArchiveTriggerClass(nil), item.ArchiveTriggerClasses...),
	}
}

func experienceID(item cognitive.ExperienceResponse) string {
	for _, attribution := range item.SourceAttribution {
		if ref := attributionRef(attribution); ref != "" {
			return ref
		}
	}
	for _, attribution := range item.Provenance {
		if ref := attributionRef(attribution); ref != "" {
			return ref
		}
	}
	return ""
}

func experienceMatchesID(item cognitive.ExperienceResponse, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if experienceID(item) == id || slices.Contains(provenanceRefs(item), id) {
		return true
	}
	for _, attribution := range item.SourceAttribution {
		if attribution.ID == id || attributionRef(attribution) == id {
			return true
		}
	}
	for _, attribution := range item.Provenance {
		if attribution.ID == id || attributionRef(attribution) == id {
			return true
		}
	}
	return false
}

func historyResultMatchesID(result HistoryResult, id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if result.ExperienceID == id || slices.Contains(result.ProvenanceRefs, id) {
		return true
	}
	for _, attribution := range result.SourceAttribution {
		if attribution.ID == id || attributionRef(attribution) == id {
			return true
		}
	}
	for _, attribution := range result.Provenance {
		if attribution.ID == id || attributionRef(attribution) == id {
			return true
		}
	}
	return false
}

func provenanceRefs(item cognitive.ExperienceResponse) []string {
	refs := make([]string, 0, len(item.Provenance)+len(item.SourceAttribution))
	for _, attribution := range item.Provenance {
		refs = appendUniqueString(refs, attributionRef(attribution))
	}
	for _, attribution := range item.SourceAttribution {
		refs = appendUniqueString(refs, attributionRef(attribution))
	}
	return refs
}

func attributionRef(attribution cognitive.ExperienceSourceAttribution) string {
	kind := strings.TrimSpace(attribution.Kind)
	id := strings.TrimSpace(attribution.ID)
	if kind != "" && id != "" {
		return kind + ":" + id
	}
	if id != "" {
		return id
	}
	if sessionID := strings.TrimSpace(attribution.SessionID); sessionID != "" {
		return "agent_session_state:" + sessionID
	}
	return ""
}
