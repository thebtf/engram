package gorm

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/thebtf/engram/internal/temporaltruth"
	"github.com/thebtf/engram/pkg/cognitive"
)

const temporalTruthOpenEndedSentinelRFC3339 = "9999-12-31T23:59:59Z"

var temporalTruthOpenEndedSentinel = mustParseTemporalTruthSentinel()

func mustParseTemporalTruthSentinel() time.Time {
	parsed, err := time.Parse(time.RFC3339, temporalTruthOpenEndedSentinelRFC3339)
	if err != nil {
		panic(err)
	}
	return parsed.UTC()
}

type temporalTruthRecordRow struct {
	CreatedAt             time.Time  `gorm:"column:created_at;autoCreateTime"`
	UpdatedAt             time.Time  `gorm:"column:updated_at;autoUpdateTime"`
	ValidFrom             time.Time  `gorm:"column:valid_from;type:timestamptz;not null;default:now()"`
	ValidUntil            *time.Time `gorm:"column:valid_until;type:timestamptz;not null;default:'9999-12-31T23:59:59Z'"`
	InvalidatedAt         *time.Time `gorm:"column:invalidated_at;type:timestamptz"`
	FactID                string     `gorm:"column:fact_id;type:text;not null"`
	FactClass             string     `gorm:"column:fact_class;type:text;not null"`
	Project               string     `gorm:"column:project;type:text;not null"`
	Value                 string     `gorm:"column:value;type:text;not null"`
	InvalidationRationale string     `gorm:"column:invalidation_rationale;type:text;not null;default:''"`
	SourceMemoryIDs       Int64Array `gorm:"column:source_memory_ids;type:bigint[];not null"`
	ID                    int64      `gorm:"primaryKey;autoIncrement"`
}

func (temporalTruthRecordRow) TableName() string { return "temporal_truth_records" }

// TemporalTruthStoredRecord is the admitted temporal row shape before
// fail-closed provenance projection turns source_memory_ids into visible
// TemporalTruthProvenance entries.
type TemporalTruthStoredRecord struct {
	FactID                string
	FactClass             string
	Project               string
	Value                 string
	InvalidationRationale string
	ValidFrom             time.Time
	ValidUntil            *time.Time
	InvalidatedAt         *time.Time
	SourceMemoryIDs       []int64
}

// TemporalTruthAdmissionResult reports what entered or stayed out during the
// bounded off-hot-path admission refresh.
type TemporalTruthAdmissionResult struct {
	Project             string
	AdmittedFacts       int
	AdmittedRecords     int
	ExcludedSingleWrite int
	ExcludedUnsupported int
}

// TemporalTruthStore persists the dedicated temporal_truth_records admission
// substrate and loads selected records for the dormant temporal service.
type TemporalTruthStore struct {
	db *gorm.DB
}

var _ temporaltruth.RecordStore = (*TemporalTruthStore)(nil)

// NewTemporalTruthStore creates a PostgreSQL-backed store for selected temporal
// truth rows.
func NewTemporalTruthStore(db *gorm.DB) *TemporalTruthStore {
	return &TemporalTruthStore{db: db}
}

// LoadStoredRecords loads the currently-admitted raw temporal rows for one
// selected fact before provenance projection.
func (s *TemporalTruthStore) LoadStoredRecords(ctx context.Context, request cognitive.TemporalTruthQueryRequest) ([]TemporalTruthStoredRecord, error) {
	rows, err := s.loadStoredRecordRows(ctx, request)
	if err != nil {
		return nil, err
	}
	return temporalTruthRowsToStoredRecords(rows), nil
}

// LoadSelectedRecords loads the currently-admitted rows for one selected fact.
// Future-dated rows stay out until DB NOW() reaches their valid_from boundary.
func (s *TemporalTruthStore) LoadSelectedRecords(ctx context.Context, request cognitive.TemporalTruthQueryRequest) ([]temporaltruth.Record, error) {
	stored, err := s.LoadStoredRecords(ctx, request)
	if err != nil {
		return nil, err
	}
	return temporalTruthStoredRecordsToRecords(stored), nil
}

func (s *TemporalTruthStore) loadStoredRecordRows(ctx context.Context, request cognitive.TemporalTruthQueryRequest) ([]temporalTruthRecordRow, error) {
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	factID := strings.TrimSpace(request.FactID)
	if factID == "" {
		return nil, nil
	}
	query := s.db.WithContext(ctx).
		Model(&temporalTruthRecordRow{}).
		Where("fact_id = ?", factID).
		Where("valid_from <= NOW()").
		Order("valid_from ASC, id ASC")
	if factClass := strings.TrimSpace(request.FactClass); factClass != "" {
		query = query.Where("fact_class = ?", factClass)
	}
	if project := strings.TrimSpace(request.Project); project != "" {
		query = query.Where("project = ?", project)
	}
	var rows []temporalTruthRecordRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("temporal truth load_selected_records %q: %w", factID, err)
	}
	return rows, nil
}

// RefreshProject rebuilds the additive temporal_truth_records rows for one
// project from the live supersession chain. This pass is explicit and off the
// hot write-path by design.
func (s *TemporalTruthStore) RefreshProject(ctx context.Context, project string) (TemporalTruthAdmissionResult, error) {
	result := TemporalTruthAdmissionResult{Project: strings.TrimSpace(project)}
	if err := s.requireDB(); err != nil {
		return result, err
	}
	if result.Project == "" {
		return result, fmt.Errorf("temporal truth refresh_project: project is required")
	}
	memories, err := s.loadProjectMemories(ctx, result.Project)
	if err != nil {
		return result, err
	}
	rows, result := buildTemporalTruthRows(memories, result.Project)
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("project = ?", result.Project).Delete(&temporalTruthRecordRow{}).Error; err != nil {
			return fmt.Errorf("delete project rows: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		if err := tx.Create(&rows).Error; err != nil {
			return fmt.Errorf("insert project rows: %w", err)
		}
		return nil
	}); err != nil {
		return result, fmt.Errorf("temporal truth refresh_project %q: %w", result.Project, err)
	}
	return result, nil
}

func (s *TemporalTruthStore) requireDB() error {
	if s == nil || s.db == nil {
		return fmt.Errorf("temporal truth store is not initialized")
	}
	return nil
}

func (s *TemporalTruthStore) loadProjectMemories(ctx context.Context, project string) ([]Memory, error) {
	var rows []Memory
	if err := s.db.WithContext(ctx).
		Where("project = ? AND deleted_at IS NULL", project).
		Where("status IN ('active','superseded')").
		Order("created_at ASC, id ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("temporal truth load_project_memories %q: %w", project, err)
	}
	return rows, nil
}

func buildTemporalTruthRows(memories []Memory, project string) ([]temporalTruthRecordRow, TemporalTruthAdmissionResult) {
	result := TemporalTruthAdmissionResult{Project: project}
	if len(memories) == 0 {
		return nil, result
	}
	rowsByID := make(map[int64]*Memory, len(memories))
	for i := range memories {
		rowsByID[memories[i].ID] = &memories[i]
	}
	chains := make(map[int64][]*Memory)
	orderedRoots := make([]int64, 0)
	for i := range memories {
		rootID := temporalTruthRootID(&memories[i], rowsByID)
		if _, seen := chains[rootID]; !seen {
			orderedRoots = append(orderedRoots, rootID)
		}
		chains[rootID] = append(chains[rootID], &memories[i])
	}
	rows := make([]temporalTruthRecordRow, 0)
	for _, rootID := range orderedRoots {
		chain := chains[rootID]
		sort.SliceStable(chain, func(i, j int) bool {
			left := temporalTruthMemoryValidFrom(chain[i])
			right := temporalTruthMemoryValidFrom(chain[j])
			if !left.Equal(right) {
				return left.Before(right)
			}
			if !chain[i].CreatedAt.Equal(chain[j].CreatedAt) {
				return chain[i].CreatedAt.Before(chain[j].CreatedAt)
			}
			return chain[i].ID < chain[j].ID
		})
		chain = collapseTemporalTruthChainByValidFrom(chain)
		if len(chain) < 2 {
			result.ExcludedSingleWrite++
			continue
		}
		factClass, ok := deriveTemporalTruthFactClass(chain[len(chain)-1].EpistemicType, chain[len(chain)-1].Domain)
		if !ok {
			result.ExcludedUnsupported++
			continue
		}
		result.AdmittedFacts++
		for i, memory := range chain {
			var successor *Memory
			if i+1 < len(chain) {
				successor = chain[i+1]
			}
			rows = append(rows, temporalTruthRowFromMemory(memory, successor, factClass, rootID))
			result.AdmittedRecords++
		}
	}
	return rows, result
}

func collapseTemporalTruthChainByValidFrom(chain []*Memory) []*Memory {
	if len(chain) < 2 {
		return chain
	}
	collapsed := make([]*Memory, 0, len(chain))
	for i := 0; i < len(chain); {
		chosen := chain[i]
		validFrom := temporalTruthMemoryValidFrom(chosen)
		j := i + 1
		for j < len(chain) {
			nextValidFrom := temporalTruthMemoryValidFrom(chain[j])
			if !nextValidFrom.Equal(validFrom) {
				break
			}
			chosen = chain[j]
			j++
		}
		collapsed = append(collapsed, chosen)
		i = j
	}
	return collapsed
}

func temporalTruthRootID(row *Memory, rowsByID map[int64]*Memory) int64 {
	if row == nil {
		return 0
	}
	current := row
	seen := make(map[int64]struct{}, 4)
	for current.SupersedesID != nil {
		if _, dup := seen[current.ID]; dup {
			break
		}
		seen[current.ID] = struct{}{}
		parent := rowsByID[*current.SupersedesID]
		if parent == nil {
			break
		}
		current = parent
	}
	return current.ID
}

func temporalTruthMemoryValidFrom(row *Memory) time.Time {
	if row != nil && row.ValidFrom != nil && !row.ValidFrom.IsZero() {
		return row.ValidFrom.UTC()
	}
	if row != nil && !row.CreatedAt.IsZero() {
		return row.CreatedAt.UTC()
	}
	if row != nil && !row.UpdatedAt.IsZero() {
		return row.UpdatedAt.UTC()
	}
	return time.Unix(0, 0).UTC()
}

func temporalTruthRowFromMemory(row *Memory, successor *Memory, factClass string, rootID int64) temporalTruthRecordRow {
	validUntil := temporalTruthMemoryValidUntil(row, successor)
	invalidatedAt := temporalTruthInvalidatedAt(row, successor, validUntil)
	return temporalTruthRecordRow{
		FactID:                strconv.FormatInt(rootID, 10),
		FactClass:             factClass,
		Project:               strings.TrimSpace(row.Project),
		Value:                 row.Content,
		ValidFrom:             temporalTruthMemoryValidFrom(row),
		ValidUntil:            validUntil,
		InvalidatedAt:         invalidatedAt,
		InvalidationRationale: temporalTruthInvalidationRationale(row, successor),
		SourceMemoryIDs:       Int64Array{row.ID},
	}
}

func temporalTruthMemoryValidUntil(row *Memory, successor *Memory) *time.Time {
	if temporalTruthHasExplicitValidUntil(row) {
		value := row.ValidUntil.UTC()
		return &value
	}
	if successor != nil {
		value := temporalTruthMemoryValidFrom(successor)
		return &value
	}
	if row != nil && row.SupersededBy != nil && !row.UpdatedAt.IsZero() {
		value := row.UpdatedAt.UTC()
		return &value
	}
	value := temporalTruthOpenEndedSentinel
	return &value
}

func temporalTruthHasExplicitValidUntil(row *Memory) bool {
	return row != nil && row.ValidUntil != nil && !row.ValidUntil.IsZero() && !isTemporalTruthOpenEnded(*row.ValidUntil)
}

func temporalTruthInvalidatedAt(row *Memory, successor *Memory, validUntil *time.Time) *time.Time {
	if validUntil == nil || isTemporalTruthOpenEnded(*validUntil) {
		return nil
	}
	if (row == nil || row.SupersededBy == nil) && (successor == nil || temporalTruthHasExplicitValidUntil(row)) {
		return nil
	}
	value := validUntil.UTC()
	return &value
}

func temporalTruthInvalidationRationale(row *Memory, successor *Memory) string {
	if row != nil && row.SupersededBy != nil {
		return fmt.Sprintf("superseded by memory %d", *row.SupersededBy)
	}
	if successor != nil && !temporalTruthHasExplicitValidUntil(row) {
		return fmt.Sprintf("superseded by memory %d", successor.ID)
	}
	return ""
}

func isTemporalTruthOpenEnded(value time.Time) bool {
	return value.UTC().Equal(temporalTruthOpenEndedSentinel)
}

func temporalTruthReadValidUntil(value *time.Time) *time.Time {
	if value == nil || isTemporalTruthOpenEnded(*value) {
		return nil
	}
	clone := value.UTC()
	return &clone
}

func temporalTruthReadInvalidatedAt(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := value.UTC()
	return &clone
}

func temporalTruthRowsToStoredRecords(rows []temporalTruthRecordRow) []TemporalTruthStoredRecord {
	stored := make([]TemporalTruthStoredRecord, 0, len(rows))
	for _, row := range rows {
		stored = append(stored, TemporalTruthStoredRecord{
			FactID:                strings.TrimSpace(row.FactID),
			FactClass:             strings.TrimSpace(row.FactClass),
			Project:               strings.TrimSpace(row.Project),
			Value:                 row.Value,
			InvalidationRationale: strings.TrimSpace(row.InvalidationRationale),
			ValidFrom:             row.ValidFrom.UTC(),
			ValidUntil:            temporalTruthReadValidUntil(row.ValidUntil),
			InvalidatedAt:         temporalTruthReadInvalidatedAt(row.InvalidatedAt),
			SourceMemoryIDs:       append([]int64(nil), row.SourceMemoryIDs...),
		})
	}
	return stored
}

func temporalTruthStoredRecordsToRecords(stored []TemporalTruthStoredRecord) []temporaltruth.Record {
	records := make([]temporaltruth.Record, 0, len(stored))
	for _, row := range stored {
		records = append(records, temporaltruth.Record{
			FactID:                strings.TrimSpace(row.FactID),
			FactClass:             strings.TrimSpace(row.FactClass),
			Project:               strings.TrimSpace(row.Project),
			Value:                 row.Value,
			ValidFrom:             row.ValidFrom.UTC(),
			ValidUntil:            temporalTruthReadValidUntil(row.ValidUntil),
			InvalidatedAt:         temporalTruthReadInvalidatedAt(row.InvalidatedAt),
			InvalidationRationale: strings.TrimSpace(row.InvalidationRationale),
			Provenance:            temporalTruthProvenanceFromSourceMemoryIDs(row.Project, row.ValidFrom, row.SourceMemoryIDs),
		})
	}
	return records
}

func temporalTruthProvenanceFromSourceMemoryIDs(project string, observedAt time.Time, sourceMemoryIDs []int64) []cognitive.TemporalTruthProvenance {
	if len(sourceMemoryIDs) == 0 {
		return nil
	}
	out := make([]cognitive.TemporalTruthProvenance, 0, len(sourceMemoryIDs))
	for _, id := range sourceMemoryIDs {
		out = append(out, cognitive.TemporalTruthProvenance{
			Kind:       "memory",
			ID:         fmt.Sprintf("memory:%d", id),
			Project:    strings.TrimSpace(project),
			ObservedAt: observedAt.UTC(),
		})
	}
	return out
}

func deriveTemporalTruthFactClass(epistemicType, domain string) (string, bool) {
	key := temporalTruthFactClassKey(epistemicType, domain)
	factClass, ok := temporalTruthStarterAllowlist[key]
	return factClass, ok
}

func temporalTruthFactClassKey(epistemicType, domain string) string {
	return strings.ToLower(strings.TrimSpace(epistemicType)) + "|" + strings.ToLower(strings.TrimSpace(domain))
}

// Conservative starter allowlist for the two selected fact classes already
// named in the CR-004 temporal scope proof. PM review can narrow this further.
var temporalTruthStarterAllowlist = map[string]string{
	temporalTruthFactClassKey("decision", "deploy"):  "deployment_setting",
	temporalTruthFactClassKey("decision", "release"): "release_policy",
}
