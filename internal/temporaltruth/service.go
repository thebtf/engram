package temporaltruth

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/thebtf/engram/pkg/cognitive"
)

const (
	DefaultHistoryLimit = 5
	MaxHistoryLimit     = 10
)

// Record is one selected-fact truth value in the bounded temporal truth seam.
type Record struct {
	ValidUntil            *time.Time
	InvalidatedAt         *time.Time
	FactID                string
	FactClass             string
	Project               string
	Value                 string
	InvalidationRationale string
	Provenance            []cognitive.TemporalTruthProvenance
	ValidFrom             time.Time
}

// RecordStore loads selected temporal truth records for one bounded query.
type RecordStore interface {
	LoadSelectedRecords(ctx context.Context, request cognitive.TemporalTruthQueryRequest) ([]Record, error)
}

// Service answers temporal truth queries from explicit selected records.
type Service struct {
	records []Record
	store   RecordStore
}

var _ cognitive.TemporalTruthProvider = (*Service)(nil)

// NewService creates an in-memory selected-fact temporal truth provider.
func NewService(records []Record) *Service {
	return &Service{records: cloneRecords(records)}
}

// NewStoreBackedService creates a selected-fact temporal truth provider backed
// by a record store.
func NewStoreBackedService(store RecordStore) *Service {
	return &Service{store: store}
}

// QueryTemporalTruth returns current truth plus bounded prior validity context
// for a selected fact. It does not traverse general memory graphs.
func (s *Service) QueryTemporalTruth(ctx context.Context, request cognitive.TemporalTruthQueryRequest) (cognitive.TemporalTruthResponse, error) {
	if s == nil {
		return cognitive.TemporalTruthResponse{}, fmt.Errorf("temporal truth service is not configured")
	}
	if err := ctx.Err(); err != nil {
		return cognitive.TemporalTruthResponse{}, err
	}
	factID := strings.TrimSpace(request.FactID)
	if factID == "" {
		return cognitive.TemporalTruthResponse{}, fmt.Errorf("fact_id is required")
	}

	matches, err := s.selectedRecords(ctx, request)
	if err != nil {
		return cognitive.TemporalTruthResponse{}, err
	}
	if len(matches) == 0 {
		return cognitive.TemporalTruthResponse{
			State: cognitive.TemporalTruthNotSelected,
			Scope: cognitive.TemporalTruthScope{
				FactID:    factID,
				FactClass: strings.TrimSpace(request.FactClass),
				Project:   strings.TrimSpace(request.Project),
				Selected:  false,
				Rationale: "fact is outside the selected temporal truth scope",
			},
		}, nil
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return matches[i].ValidFrom.Before(matches[j].ValidFrom)
	})
	queryClock := time.Now().UTC()
	visibleMatches := visibleRecordsAt(matches, queryClock)
	history := entriesFromRecords(visibleMatches)
	limit := normalizeLimit(request.Limit)
	if len(history) > limit {
		history = history[len(history)-limit:]
	}
	scopeRecord := matches[len(matches)-1]
	if len(visibleMatches) > 0 {
		scopeRecord = visibleMatches[len(visibleMatches)-1]
	}
	response := cognitive.TemporalTruthResponse{
		Scope:           scopeFromRecord(scopeRecord, true, "selected high-value evolving fact"),
		State:           cognitive.TemporalTruthUnknown,
		History:         history,
		ProvenanceChain: provenanceChain(history),
	}
	if nowEntry, ok := currentEntry(visibleMatches, queryClock); ok {
		response.State = cognitive.TemporalTruthFound
		response.TrueNow = &nowEntry
	} else {
		response.Scope.Rationale = "selected fact has no current truth value"
	}
	if request.AsOf != nil {
		if thenEntry, ok := entryAt(visibleMatches, *request.AsOf); ok {
			response.TrueThen = &thenEntry
		}
	}
	return response, nil
}

func (s *Service) selectedRecords(ctx context.Context, request cognitive.TemporalTruthQueryRequest) ([]Record, error) {
	source := s.records
	if s.store != nil {
		loaded, err := s.store.LoadSelectedRecords(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("temporal truth load_selected_records: %w", err)
		}
		source = loaded
	}
	factID := strings.TrimSpace(request.FactID)
	factClass := strings.TrimSpace(request.FactClass)
	project := strings.TrimSpace(request.Project)
	matches := make([]Record, 0)
	for _, record := range source {
		if record.FactID != factID {
			continue
		}
		if factClass != "" && record.FactClass != factClass {
			continue
		}
		if project != "" && record.Project != "" && record.Project != project {
			continue
		}
		matches = append(matches, cloneRecord(record))
	}
	return matches, nil
}

func currentEntry(records []Record, now time.Time) (cognitive.TemporalTruthEntry, bool) {
	if len(records) == 0 {
		return cognitive.TemporalTruthEntry{}, false
	}
	return entryAt(records, now)
}

func visibleRecordsAt(records []Record, at time.Time) []Record {
	visible := make([]Record, 0, len(records))
	for _, record := range records {
		if record.ValidFrom.After(at) {
			continue
		}
		visible = append(visible, record)
	}
	return visible
}

func entryAt(records []Record, when time.Time) (cognitive.TemporalTruthEntry, bool) {
	for i := len(records) - 1; i >= 0; i-- {
		record := records[i]
		if when.Before(record.ValidFrom) {
			continue
		}
		if record.ValidUntil != nil && !when.Before(*record.ValidUntil) {
			continue
		}
		return entryFromRecord(record), true
	}
	return cognitive.TemporalTruthEntry{}, false
}

func entriesFromRecords(records []Record) []cognitive.TemporalTruthEntry {
	entries := make([]cognitive.TemporalTruthEntry, 0, len(records))
	for _, record := range records {
		entries = append(entries, entryFromRecord(record))
	}
	return entries
}

func provenanceChain(history []cognitive.TemporalTruthEntry) []cognitive.TemporalTruthProvenance {
	total := 0
	for _, entry := range history {
		total += len(entry.Provenance)
	}
	chain := make([]cognitive.TemporalTruthProvenance, 0, total)
	for _, entry := range history {
		chain = append(chain, entry.Provenance...)
	}
	return chain
}

func entryFromRecord(record Record) cognitive.TemporalTruthEntry {
	return cognitive.TemporalTruthEntry{
		Value:                 record.Value,
		ValidFrom:             record.ValidFrom,
		ValidUntil:            cloneTimePtr(record.ValidUntil),
		InvalidatedAt:         cloneTimePtr(record.InvalidatedAt),
		InvalidationRationale: record.InvalidationRationale,
		Provenance:            append([]cognitive.TemporalTruthProvenance(nil), record.Provenance...),
	}
}

func scopeFromRecord(record Record, selected bool, rationale string) cognitive.TemporalTruthScope {
	return cognitive.TemporalTruthScope{
		FactID:    record.FactID,
		FactClass: record.FactClass,
		Project:   record.Project,
		Selected:  selected,
		Rationale: rationale,
	}
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return DefaultHistoryLimit
	}
	if limit > MaxHistoryLimit {
		return MaxHistoryLimit
	}
	return limit
}

func cloneRecords(records []Record) []Record {
	cloned := make([]Record, 0, len(records))
	for _, record := range records {
		cloned = append(cloned, cloneRecord(record))
	}
	return cloned
}

func cloneRecord(record Record) Record {
	record.ValidUntil = cloneTimePtr(record.ValidUntil)
	record.InvalidatedAt = cloneTimePtr(record.InvalidatedAt)
	record.Provenance = append([]cognitive.TemporalTruthProvenance(nil), record.Provenance...)
	return record
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
