package gorm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lib/pq"
	"github.com/thebtf/engram/internal/scope"
)

const retainedRecordWorkLimit = 200

// RetainedRecordPosition records consumed work, not a count of visible results.
type RetainedRecordPosition struct {
	BeforeID     int64  `json:"b"`
	PendingID    int64  `json:"p,omitempty"`
	AfterWitness string `json:"w,omitempty"`
}

type RetainedRecordSelection struct {
	ID        int64
	Reference string
}

func retainedRecordTable(kind string) (string, error) {
	switch kind {
	case "memory":
		return "memories", nil
	case "document":
		return "versioned_documents", nil
	case "issue":
		return "issues", nil
	default:
		return "", fmt.Errorf("unsupported retained record kind")
	}
}

// RetainedRecordSelections proposes a finite native window, then verifies a finite
// witness set. Bodies are read only after this consumed prefix is established.
func (s *SessionStore) RetainedRecordSelections(ctx context.Context, membership *ProjectConsolidationStore, kind, text string, limit int, position RetainedRecordPosition, caller scope.KeycardContext, opts scope.MemoryVisibilityOptions) ([]RetainedRecordSelection, *RetainedRecordPosition, error) {
	table, err := retainedRecordTable(kind)
	if err != nil || limit < 1 || limit > retainedRecordWorkLimit {
		return nil, nil, fmt.Errorf("invalid retained record selection")
	}
	if err := membership.available(); err != nil {
		return nil, nil, err
	}
	window := s.db.WithContext(ctx).Table(table + " AS m").Where("m.id > 0")
	if position.PendingID > 0 {
		window = window.Where("m.id <= ?", position.PendingID)
	} else if position.BeforeID > 0 {
		window = window.Where("m.id < ?", position.BeforeID)
	}
	var nativeIDs []int64
	if err := window.Order("m.id DESC").Limit(retainedRecordWorkLimit+1).Pluck("m.id", &nativeIDs).Error; err != nil {
		return nil, nil, err
	}
	moreNative := len(nativeIDs) > retainedRecordWorkLimit
	if moreNative {
		nativeIDs = nativeIDs[:retainedRecordWorkLimit]
	}
	selected := make([]RetainedRecordSelection, 0, limit)
	if len(nativeIDs) == 0 {
		return selected, nil, nil
	}
	eligible := s.db.WithContext(ctx).Table(table+" AS m").Where("m.id IN ?", nativeIDs)
	if kind == "memory" {
		eligible = memoryReaderPrivacy(eligible.Where("m.deleted_at IS NULL"), caller, opts)
	}
	if text != "" {
		literal := "%" + escapeSQLLike(strings.ToLower(text)) + "%"
		switch kind {
		case "memory":
			eligible = eligible.Where("LOWER(m.content) LIKE ? ESCAPE '\\'", literal)
		case "document":
			eligible = eligible.Where("LOWER(m.path) LIKE ? ESCAPE '\\' OR LOWER(m.content) LIKE ? ESCAPE '\\'", literal, literal)
		case "issue":
			eligible = eligible.Where("LOWER(m.title) LIKE ? ESCAPE '\\' OR LOWER(m.body) LIKE ? ESCAPE '\\'", literal, literal)
		}
	}
	var eligibleIDs []int64
	if err := eligible.Order("m.id DESC").Pluck("m.id", &eligibleIDs).Error; err != nil {
		return nil, nil, err
	}
	type witness struct {
		ConsolidationMembership `gorm:"embedded"`
		NativeID                int64
	}
	var witnesses []witness
	if len(eligibleIDs) > 0 {
		query := `SELECT m.*, n.id AS native_id FROM project_consolidation_memberships m
			JOIN unnest(?::bigint[]) AS n(id) ON m.row_key->>'id' = n.id::text
			WHERE m.table_name = ? AND m.project_id IS NULL AND m.unresolved_reference <> ''
			AND octet_length(m.reference) <= 128
			AND (n.id <> ? OR m.reference > ?)
			ORDER BY n.id DESC, m.reference ASC LIMIT 201`
		if err := s.db.WithContext(ctx).Raw(query, pq.Array(eligibleIDs), table, position.PendingID, position.AfterWitness).Scan(&witnesses).Error; err != nil {
			return nil, nil, err
		}
	}
	work := witnesses[:min(len(witnesses), retainedRecordWorkLimit)]
	valid := make(map[string]bool, len(work))
	rows := make([]ConsolidationMembership, 0, len(work))
	columns := make(map[string]bool)
	if len(work) > 0 {
		var names []string
		if err := s.db.WithContext(ctx).Raw(`SELECT a.attname FROM pg_attribute a
			WHERE a.attrelid = to_regclass(?) AND a.attnum > 0 AND NOT a.attisdropped`, quoted(table)).Scan(&names).Error; err != nil {
			return nil, nil, err
		}
		for _, name := range names {
			columns[name] = true
		}
	}
	for _, member := range work {
		// SQL text extraction is only a proposal: numeric JSON keys remain typed.
		var key map[string]json.RawMessage
		var id int64
		if json.Unmarshal(member.RowKey, &key) != nil || json.Unmarshal(key["id"], &id) != nil || id != member.NativeID || id < 1 {
			continue
		}
		knownKey := true
		for name := range key {
			if !columns[name] {
				knownKey = false
				break
			}
		}
		if !knownKey {
			continue
		}
		valid[member.Reference] = true
		rows = append(rows, member.ConsolidationMembership)
	}
	if err := membership.validateUnresolvedWitnesses(ctx, rows, func(row *ConsolidationMembership, _ error) error {
		delete(valid, row.Reference)
		return nil
	}); err != nil {
		return nil, nil, err
	}
	index := 0
	next := position
	for i, id := range nativeIDs {
		// Higher-ID lookahead still leaves every lower native ID unprocessed.
		if len(witnesses) > len(work) && witnesses[len(work)].NativeID > id {
			return selected, &next, nil
		}
		lastWitness := ""
		if id == position.PendingID {
			lastWitness = position.AfterWitness
		}
		reference := ""
		for index < len(work) && work[index].NativeID == id {
			member := work[index]
			lastWitness = member.Reference
			if reference == "" && valid[member.Reference] {
				reference = member.UnresolvedReference
			}
			index++
		}
		if reference == "" && len(witnesses) > len(work) && witnesses[len(work)].NativeID == id {
			next.PendingID, next.AfterWitness = id, lastWitness
			return selected, &next, nil
		}
		next = RetainedRecordPosition{BeforeID: id}
		if reference != "" {
			selected = append(selected, RetainedRecordSelection{ID: id, Reference: reference})
			if len(selected) == limit {
				if i+1 < len(nativeIDs) || moreNative {
					return selected, &next, nil
				}
				return selected, nil, nil
			}
		}
	}
	if moreNative {
		return selected, &next, nil
	}
	return selected, nil, nil
}
